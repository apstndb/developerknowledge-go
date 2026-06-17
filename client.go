package dkapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	DefaultV1BaseURL   = "https://developerknowledge.googleapis.com/v1"
	DefaultHTTPTimeout = time.Minute
)

type AuthMode int

const (
	AuthPreferAPIKey AuthMode = iota
	AuthRequireADC
)

type TokenSourceFunc func(context.Context, ...string) (oauth2.TokenSource, error)

var DefaultTokenSource TokenSourceFunc = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
	return google.DefaultTokenSource(ctx, scopes...)
}

type ADCCredentialsMetadata struct {
	Type           string `json:"type"`
	QuotaProjectID string `json:"quota_project_id"`
}

type AuthConfig struct {
	Mode            AuthMode
	Timeout         time.Duration
	TokenSource     TokenSourceFunc
	CredentialsPath func() string
}

func APIKeyFromEnv() string {
	if key := os.Getenv("DEVELOPERKNOWLEDGE_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
		return key
	}
	return ""
}

func NewAuthenticatedHTTPClient(ctx context.Context, cfg AuthConfig) (*http.Client, string, error) {
	if cfg.Mode != AuthRequireADC {
		if apiKey := APIKeyFromEnv(); apiKey != "" {
			return &http.Client{Timeout: cfg.Timeout}, apiKey, nil
		}
	}

	client, err := NewADCHTTPClient(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	return client, "", nil
}

func NewADCHTTPClient(ctx context.Context, cfg AuthConfig) (*http.Client, error) {
	tokenSource := cfg.TokenSource
	if tokenSource == nil {
		tokenSource = DefaultTokenSource
	}
	ts, err := tokenSource(ctx, CloudPlatformScope)
	if err != nil {
		if cfg.Mode == AuthPreferAPIKey {
			return nil, fmt.Errorf("set DEVELOPERKNOWLEDGE_API_KEY or GOOGLE_API_KEY, or configure ADC with 'gcloud auth application-default login': %w", err)
		}
		return nil, fmt.Errorf("get credentials: %w (run 'gcloud auth application-default login')", err)
	}

	client := oauth2.NewClient(ctx, ts)
	if cfg.Timeout > 0 {
		client.Timeout = cfg.Timeout
	}

	quotaProject, metadata := ResolveQuotaProjectID(cfg.CredentialsPath)
	if quotaProject == "" && metadata.Type == "authorized_user" {
		return nil, fmt.Errorf("ADC requires a quota project; run 'gcloud auth application-default set-quota-project <project-id>' or set GOOGLE_CLOUD_QUOTA_PROJECT")
	}
	if quotaProject != "" {
		baseTransport := client.Transport
		if baseTransport == nil {
			baseTransport = http.DefaultTransport
		}
		client.Transport = &QuotaProjectTransport{
			Base:    baseTransport,
			Project: quotaProject,
		}
	}
	return client, nil
}

func DefaultCredentialsPath() string {
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return path
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}
	return DefaultADCCredentialsPath(runtime.GOOS, homeDir, os.Getenv("APPDATA"))
}

func DefaultADCCredentialsPath(goos, homeDir, appData string) string {
	if goos == "windows" {
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "gcloud", "application_default_credentials.json")
	}
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, ".config", "gcloud", "application_default_credentials.json")
}

func LoadADCCredentialsMetadata(credentialsPath func() string) ADCCredentialsMetadata {
	if credentialsPath == nil {
		credentialsPath = DefaultCredentialsPath
	}
	path := credentialsPath()
	if path == "" {
		return ADCCredentialsMetadata{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ADCCredentialsMetadata{}
	}
	var cfg ADCCredentialsMetadata
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ADCCredentialsMetadata{}
	}
	return cfg
}

func ResolveQuotaProjectID(credentialsPath func() string) (string, ADCCredentialsMetadata) {
	if p := os.Getenv("GOOGLE_CLOUD_QUOTA_PROJECT"); p != "" {
		return p, ADCCredentialsMetadata{}
	}

	cfg := LoadADCCredentialsMetadata(credentialsPath)
	return cfg.QuotaProjectID, cfg
}

type QuotaProjectTransport struct {
	Base    http.RoundTripper
	Project string
}

func (t *QuotaProjectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("x-goog-user-project", t.Project)
	return t.Base.RoundTrip(req)
}

type Document struct {
	Name        string `json:"name" yaml:"name"`
	URI         string `json:"uri" yaml:"uri"`
	Content     string `json:"content,omitempty" yaml:"content,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	DataSource  string `json:"dataSource,omitempty" yaml:"data_source,omitempty"`
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty" yaml:"update_time,omitempty"`
	View        string `json:"view,omitempty" yaml:"view,omitempty"`
}

type DocumentChunk struct {
	Parent   string    `json:"parent" yaml:"parent"`
	ID       string    `json:"id" yaml:"id"`
	Content  string    `json:"content" yaml:"content"`
	Document *Document `json:"document,omitempty" yaml:"document,omitempty"`
}

type BatchGetResponse struct {
	Documents []Document `json:"documents" yaml:"documents"`
}

type APIError struct {
	Code    int
	Status  string
	Message string
}

func (e *APIError) Error() string {
	if e.Status != "" {
		return fmt.Sprintf("API error %d (%s): %s", e.Code, e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Message)
}

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited (retry after %v)", e.RetryAfter)
	}
	return "rate limited"
}

func ParseRetryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	wait := time.Until(t)
	if wait < 0 {
		return 0
	}
	return wait
}

func CheckResponse(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &RateLimitError{RetryAfter: ParseRetryAfter(resp)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, &APIError{Code: apiErr.Error.Code, Status: apiErr.Error.Status, Message: apiErr.Error.Message}
		}
		return nil, &APIError{Code: resp.StatusCode, Message: string(body)}
	}

	return body, nil
}

type Waiter interface {
	Wait(context.Context) error
}

type Client struct {
	BaseURL       string
	APIKey        string
	HTTPClient    *http.Client
	Context       context.Context
	Limiter       Waiter
	Verbose       bool
	VerboseWriter io.Writer
	// MaxRetries is the number of additional attempts after the first request.
	MaxRetries int
}

func (c *Client) requestContext() context.Context {
	if c.Context != nil {
		return c.Context
	}
	return context.Background()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) maxAttempts() int {
	if c.MaxRetries < 0 {
		return 1
	}
	return c.MaxRetries + 1
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultV1BaseURL
}

func (c *Client) verboseWriter() io.Writer {
	if c.VerboseWriter != nil {
		return c.VerboseWriter
	}
	return os.Stderr
}

func (c *Client) DoAPIRequest(method, reqURL string, body []byte, contentType string) ([]byte, error) {
	backoff := time.Second
	ctx := c.requestContext()
	maxAttempts := c.maxAttempts()
	var retryErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if c.Limiter != nil {
			if err := c.Limiter.Wait(ctx); err != nil {
				return nil, err
			}
		}

		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, reqURL, requestBody)
		if err != nil {
			return nil, err
		}
		if c.APIKey != "" {
			req.Header.Set("x-goog-api-key", c.APIKey)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.httpClient().Do(req)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			return nil, err
		}

		if c.Verbose {
			dump, _ := httputil.DumpResponse(resp, false)
			fmt.Fprintf(c.verboseWriter(), "%s", dump)
		}

		respBody, err := CheckResponse(resp)
		var rlErr *RateLimitError
		if errors.As(err, &rlErr) {
			retryErr = err
			if attempt == maxAttempts-1 {
				break
			}
			wait := backoff
			if rlErr.RetryAfter > 0 {
				wait = rlErr.RetryAfter
			}
			if c.Verbose {
				fmt.Fprintf(c.verboseWriter(), "Rate limited, retrying after %v...\n", wait)
			}
			if err := SleepContext(ctx, wait); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		return respBody, err
	}
	return nil, retryErr
}

func (c *Client) DoGet(reqURL string) ([]byte, error) {
	return c.DoAPIRequest(http.MethodGet, reqURL, nil, "")
}

func (c *Client) DoJSONPost(reqURL string, body []byte) ([]byte, error) {
	return c.DoAPIRequest(http.MethodPost, reqURL, body, "application/json")
}

func (c *Client) BatchGetDocuments(names []string) ([]Document, error) {
	params := url.Values{}
	for _, name := range names {
		params.Add("names", name)
	}

	body, err := c.DoGet(c.baseURL() + "/documents:batchGet?" + params.Encode())
	if err != nil {
		return nil, err
	}

	var resp BatchGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Documents, nil
}

func NormalizeDocName(name string) string {
	name = strings.TrimPrefix(name, "https://")
	name = strings.TrimPrefix(name, "http://")
	if !strings.HasPrefix(name, "documents/") {
		name = "documents/" + name
	}
	return name
}

func IsBisectableDocumentError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Code < 400 || ae.Code >= 500 {
		return false
	}
	switch ae.Status {
	case "INVALID_ARGUMENT", "NOT_FOUND":
		return true
	}
	return false
}

func SleepContext(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
