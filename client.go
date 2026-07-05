package dkapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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
	// MaxBatchGetDocuments is the maximum number of document names accepted by
	// documents:batchGet. Documents are returned in the same order as names.
	MaxBatchGetDocuments = 20
	// maxErrorBodyBytes caps how much of a non-2xx response body is read for errors.
	maxErrorBodyBytes = 1 << 20
	// defaultInitialRetryBackoff is the first retry wait when Retry-After is absent.
	defaultInitialRetryBackoff = time.Second
	// defaultMaxRetryBackoff caps exponential backoff between retry attempts.
	defaultMaxRetryBackoff = 30 * time.Second
	// defaultMaxRetryAfter caps Retry-After header values.
	defaultMaxRetryAfter = 60 * time.Second
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

func needsQuotaProject(credentialType string) bool {
	switch credentialType {
	case "authorized_user", "external_account":
		return true
	default:
		return false
	}
}

func credentialsPath(cfg AuthConfig) string {
	if cfg.CredentialsPath != nil {
		return cfg.CredentialsPath()
	}
	return DefaultCredentialsPath()
}

func tokenSourceFromCredentialsFile(ctx context.Context, path string) (oauth2.TokenSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	creds, err := google.CredentialsFromJSON(ctx, data, CloudPlatformScope)
	if err != nil {
		return nil, err
	}
	return creds.TokenSource, nil
}

func adcTokenSource(ctx context.Context, path string) (oauth2.TokenSource, error) {
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" || os.Getenv("CLOUDSDK_CONFIG") != "" {
		return tokenSourceFromCredentialsFile(ctx, path)
	}
	if _, err := os.Stat(path); err == nil {
		return tokenSourceFromCredentialsFile(ctx, path)
	}
	return DefaultTokenSource(ctx, CloudPlatformScope)
}

func NewADCHTTPClient(ctx context.Context, cfg AuthConfig) (*http.Client, error) {
	var ts oauth2.TokenSource
	var err error

	if cfg.TokenSource != nil {
		ts, err = cfg.TokenSource(ctx, CloudPlatformScope)
	} else if path := credentialsPath(cfg); path != "" {
		ts, err = adcTokenSource(ctx, path)
	} else {
		ts, err = DefaultTokenSource(ctx, CloudPlatformScope)
	}
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
	if quotaProject == "" && needsQuotaProject(metadata.Type) {
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
	if path := os.Getenv("CLOUDSDK_CONFIG"); path != "" {
		return filepath.Join(path, "application_default_credentials.json")
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
	UpdateTime         string `json:"updateTime,omitempty" yaml:"update_time,omitempty"`
	View               string `json:"view,omitempty" yaml:"view,omitempty"`
	ContentLengthBytes int64  `json:"contentLengthBytes,omitempty" yaml:"content_length_bytes,omitempty"`
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
	reader := io.Reader(resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reader = io.LimitReader(resp.Body, maxErrorBodyBytes)
	}
	body, err := io.ReadAll(reader)
	_ = resp.Body.Close()
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
	Limiter       Waiter
	Verbose       bool
	VerboseWriter io.Writer
	// MaxRetries is the number of additional attempts after the first request.
	MaxRetries int
	// MaxRetryBackoff caps exponential backoff between retries. Zero uses defaultMaxRetryBackoff.
	MaxRetryBackoff time.Duration
	// MaxRetryAfter caps Retry-After header values. Zero uses defaultMaxRetryAfter.
	MaxRetryAfter time.Duration
	// MaxRetryElapsed limits total time spent retrying. Zero means no limit.
	MaxRetryElapsed time.Duration
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

func (c *Client) maxRetryBackoff() time.Duration {
	if c.MaxRetryBackoff > 0 {
		return c.MaxRetryBackoff
	}
	return defaultMaxRetryBackoff
}

func (c *Client) maxRetryAfter() time.Duration {
	if c.MaxRetryAfter > 0 {
		return c.MaxRetryAfter
	}
	return defaultMaxRetryAfter
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func capDuration(d, max time.Duration) time.Duration {
	if max > 0 && d > max {
		return max
	}
	return d
}

// retryWaitDuration picks a wait before the next attempt using Retry-After when
// present and otherwise exponential backoff with jitter.
func retryWaitDuration(backoff, retryAfter, maxBackoff time.Duration) time.Duration {
	wait := backoff
	if retryAfter > 0 {
		wait = retryAfter
	}
	wait = capDuration(wait, maxBackoff)
	if wait <= 0 {
		return 0
	}
	// Jitter in [0.75, 1.25] to reduce synchronized retries.
	factor := 0.75 + rand.Float64()*0.5
	return capDuration(time.Duration(float64(wait)*factor), maxBackoff)
}

func (c *Client) sleepBeforeRetry(ctx context.Context, backoff, retryAfter time.Duration, nextBackoff *time.Duration) error {
	wait := retryWaitDuration(backoff, retryAfter, c.maxRetryBackoff())
	if c.Verbose && wait > 0 {
		_, _ = fmt.Fprintf(c.verboseWriter(), "Retrying after %v...\n", wait)
	}
	if err := SleepContext(ctx, wait); err != nil {
		return err
	}
	*nextBackoff = capDuration(backoff*2, c.maxRetryBackoff())
	return nil
}

func discardResponseBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
	_ = resp.Body.Close()
}

func (c *Client) DoAPIRequest(ctx context.Context, method, reqURL string, body []byte, contentType string) ([]byte, error) {
	backoff := defaultInitialRetryBackoff
	maxAttempts := c.maxAttempts()
	var lastErr error
	retryStart := time.Now()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if c.MaxRetryElapsed > 0 && attempt > 0 && time.Since(retryStart) >= c.MaxRetryElapsed {
			break
		}

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
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			if attempt == maxAttempts-1 {
				return nil, err
			}
			if err := c.sleepBeforeRetry(ctx, backoff, 0, &backoff); err != nil {
				return nil, err
			}
			continue
		}

		if c.Verbose {
			dump, _ := httputil.DumpResponse(resp, false)
			_, _ = fmt.Fprintf(c.verboseWriter(), "%s", dump)
		}

		if isRetryableStatus(resp.StatusCode) {
			retryAfter := time.Duration(0)
			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfter = capDuration(ParseRetryAfter(resp), c.maxRetryAfter())
				lastErr = &RateLimitError{RetryAfter: retryAfter}
			} else {
				lastErr = &APIError{Code: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
			}
			discardResponseBody(resp)
			if attempt == maxAttempts-1 {
				break
			}
			if err := c.sleepBeforeRetry(ctx, backoff, retryAfter, &backoff); err != nil {
				return nil, err
			}
			continue
		}

		return CheckResponse(resp)
	}
	return nil, lastErr
}

func (c *Client) DoGet(ctx context.Context, reqURL string) ([]byte, error) {
	return c.DoAPIRequest(ctx, http.MethodGet, reqURL, nil, "")
}

func (c *Client) DoJSONPost(ctx context.Context, reqURL string, body []byte) ([]byte, error) {
	return c.DoAPIRequest(ctx, http.MethodPost, reqURL, body, "application/json")
}

func (c *Client) BatchGetDocuments(ctx context.Context, names []string) ([]Document, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("batchGet requires at least one document name")
	}
	if len(names) > MaxBatchGetDocuments {
		return nil, fmt.Errorf("batchGet accepts at most %d document names, got %d", MaxBatchGetDocuments, len(names))
	}

	params := url.Values{}
	for _, name := range names {
		params.Add("names", name)
	}

	body, err := c.DoGet(ctx, c.baseURL()+"/documents:batchGet?"+params.Encode())
	if err != nil {
		return nil, err
	}

	var resp BatchGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Documents, nil
}

// BatchGetDocumentsAll fetches documents in chunks of MaxBatchGetDocuments while
// preserving the order of names. Invalid names fail the whole batch for that chunk.
func (c *Client) BatchGetDocumentsAll(ctx context.Context, names []string) ([]Document, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("batchGet requires at least one document name")
	}

	docs := make([]Document, 0, len(names))
	for start := 0; start < len(names); start += MaxBatchGetDocuments {
		end := start + MaxBatchGetDocuments
		if end > len(names) {
			end = len(names)
		}
		chunk, err := c.BatchGetDocuments(ctx, names[start:end])
		if err != nil {
			return nil, err
		}
		docs = append(docs, chunk...)
	}
	return docs, nil
}

// NormalizeDocName converts a pasted URL or short document path into a Developer
// Knowledge API resource name (documents/...). Query strings, fragments, and
// trailing slashes are stripped.
func NormalizeDocName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "documents/"
	}

	if strings.Contains(name, "://") {
		u, err := url.Parse(name)
		if err == nil && u.Host != "" {
			path := strings.TrimPrefix(u.Path, "/")
			resource := u.Host
			if path != "" {
				resource += "/" + path
			}
			if strings.HasPrefix(resource, "documents/") {
				return resource
			}
			return "documents/" + resource
		}
	}

	name = strings.TrimPrefix(name, "https://")
	name = strings.TrimPrefix(name, "http://")
	name = strings.TrimPrefix(name, "HTTPS://")
	name = strings.TrimPrefix(name, "HTTP://")

	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimSuffix(name, "/")

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
