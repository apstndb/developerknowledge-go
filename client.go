package dkapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	Mode    AuthMode
	Timeout time.Duration
	// AllowedOrigin is the hierarchical ASCII HTTP(S) origin accepted by
	// constructor-created clients. It defaults to DefaultV1BaseURL.
	AllowedOrigin string
	// TokenSource overrides ADC discovery. When set, CredentialsPath is ignored.
	TokenSource TokenSourceFunc
	// QuotaProjectID explicitly sets the x-goog-user-project header for ADC
	// clients. It takes precedence over GOOGLE_CLOUD_QUOTA_PROJECT and
	// credentials-file metadata.
	QuotaProjectID string
	// CredentialsPath returns an explicit ADC file path. When set, the path is
	// evaluated once and must be readable; ADC discovery does not fall back.
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

// NewAuthenticatedHTTPClient prefers an environment API key unless ADC is
// required. Initial requests are restricted to AuthConfig.AllowedOrigin, which
// defaults to DefaultV1BaseURL, and redirects must remain on that origin.
func NewAuthenticatedHTTPClient(ctx context.Context, cfg AuthConfig) (*http.Client, string, error) {
	if cfg.Mode != AuthRequireADC {
		if apiKey := APIKeyFromEnv(); apiKey != "" {
			origin, err := authAllowedOrigin(cfg)
			if err != nil {
				return nil, "", err
			}
			client := &http.Client{
				Transport: &originGuardTransport{Base: http.DefaultTransport, Origin: origin},
				Timeout:   cfg.Timeout,
			}
			client.CheckRedirect = sameOriginRedirectPolicy(client.CheckRedirect)
			return client, apiKey, nil
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
	case "authorized_user":
		return true
	default:
		return false
	}
}

func tokenSourceAndMetadataFromCredentialsFile(
	ctx context.Context,
	path string,
) (oauth2.TokenSource, ADCCredentialsMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ADCCredentialsMetadata{}, err
	}
	return tokenSourceAndMetadataFromCredentialsJSON(ctx, data)
}

func tokenSourceAndMetadataFromCredentialsJSON(
	ctx context.Context,
	data []byte,
) (oauth2.TokenSource, ADCCredentialsMetadata, error) {
	creds, err := google.CredentialsFromJSON(ctx, data, CloudPlatformScope)
	if err != nil {
		return nil, ADCCredentialsMetadata{}, err
	}

	var metadata ADCCredentialsMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, ADCCredentialsMetadata{}, err
	}
	return creds.TokenSource, metadata, nil
}

func optionalCredentialsFile(
	ctx context.Context,
	path string,
) (oauth2.TokenSource, ADCCredentialsMetadata, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		ts, metadata, parseErr := tokenSourceAndMetadataFromCredentialsJSON(ctx, data)
		return ts, metadata, true, parseErr
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, ADCCredentialsMetadata{}, false, fmt.Errorf("read ADC credentials %q: %w", path, err)
	}
	if _, lstatErr := os.Lstat(path); lstatErr == nil {
		return nil, ADCCredentialsMetadata{}, false, fmt.Errorf("read ADC credentials %q: %w", path, err)
	} else if !errors.Is(lstatErr, fs.ErrNotExist) {
		return nil, ADCCredentialsMetadata{}, false, fmt.Errorf("inspect ADC credentials %q: %w", path, lstatErr)
	}

	for parentPath := filepath.Dir(path); ; parentPath = filepath.Dir(parentPath) {
		parent, parentErr := os.Stat(parentPath)
		switch {
		case parentErr == nil && !parent.IsDir():
			return nil, ADCCredentialsMetadata{}, false, fmt.Errorf("read ADC credentials %q: %w", path, err)
		case parentErr == nil:
			return nil, ADCCredentialsMetadata{}, false, nil
		case !errors.Is(parentErr, fs.ErrNotExist):
			return nil, ADCCredentialsMetadata{}, false, fmt.Errorf("inspect ADC credentials parent %q: %w", parentPath, parentErr)
		}
		if _, lstatErr := os.Lstat(parentPath); lstatErr == nil {
			return nil, ADCCredentialsMetadata{}, false, fmt.Errorf("read ADC credentials %q: %w", path, err)
		} else if !errors.Is(lstatErr, fs.ErrNotExist) {
			return nil, ADCCredentialsMetadata{}, false, fmt.Errorf("inspect ADC credentials parent %q: %w", parentPath, lstatErr)
		}
		if filepath.Dir(parentPath) == parentPath {
			return nil, ADCCredentialsMetadata{}, false, nil
		}
	}
}

func resolveADCCredentials(
	ctx context.Context,
	cfg AuthConfig,
) (oauth2.TokenSource, ADCCredentialsMetadata, error) {
	if cfg.TokenSource != nil {
		ts, err := cfg.TokenSource(ctx, CloudPlatformScope)
		return ts, ADCCredentialsMetadata{}, err
	}

	if cfg.CredentialsPath != nil {
		return tokenSourceAndMetadataFromCredentialsFile(ctx, cfg.CredentialsPath())
	}

	path := DefaultCredentialsPath()
	// An explicit credentials file must fail loudly when unreadable. CLOUDSDK_CONFIG
	// only changes the well-known file location; if absent, preserve ADC fallback.
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		return tokenSourceAndMetadataFromCredentialsFile(ctx, path)
	}
	if ts, metadata, found, err := optionalCredentialsFile(ctx, path); err != nil || found {
		return ts, metadata, err
	}
	if os.Getenv("CLOUDSDK_CONFIG") != "" {
		// Read the standard well-known file ourselves so its token and quota
		// metadata remain coupled before falling back to the metadata server.
		fallbackPath := platformDefaultCredentialsPath()
		if ts, metadata, found, err := optionalCredentialsFile(ctx, fallbackPath); err != nil || found {
			return ts, metadata, err
		}
	}

	ts, err := DefaultTokenSource(ctx, CloudPlatformScope)
	return ts, ADCCredentialsMetadata{}, err
}

// NewADCHTTPClient constructs an OAuth-authenticated HTTP client. Initial
// requests are restricted to AuthConfig.AllowedOrigin, which defaults to
// DefaultV1BaseURL, and redirects must remain on that origin.
func NewADCHTTPClient(ctx context.Context, cfg AuthConfig) (*http.Client, error) {
	origin, err := authAllowedOrigin(cfg)
	if err != nil {
		return nil, err
	}

	ts, metadata, err := resolveADCCredentials(ctx, cfg)
	if err != nil {
		if cfg.Mode == AuthPreferAPIKey {
			return nil, fmt.Errorf("set DEVELOPERKNOWLEDGE_API_KEY or GOOGLE_API_KEY, or configure ADC with 'gcloud auth application-default login': %w", err)
		}
		return nil, fmt.Errorf("get credentials: %w (run 'gcloud auth application-default login')", err)
	}

	client := oauth2.NewClient(ctx, ts)
	client.CheckRedirect = sameOriginRedirectPolicy(client.CheckRedirect)
	if cfg.Timeout > 0 {
		client.Timeout = cfg.Timeout
	}

	quotaProject := cfg.QuotaProjectID
	if quotaProject == "" {
		quotaProject = os.Getenv("GOOGLE_CLOUD_QUOTA_PROJECT")
	}
	if quotaProject == "" {
		quotaProject = metadata.QuotaProjectID
	}
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
	client.Transport = &originGuardTransport{Base: client.Transport, Origin: origin}
	return client, nil
}

func DefaultCredentialsPath() string {
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return path
	}
	if path := os.Getenv("CLOUDSDK_CONFIG"); path != "" {
		return filepath.Join(path, "application_default_credentials.json")
	}
	return platformDefaultCredentialsPath()
}

func platformDefaultCredentialsPath() string {
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

type originGuardTransport struct {
	Base   http.RoundTripper
	Origin *url.URL
}

func (t *originGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateRequestOrigin(req, t.Origin); err != nil {
		closeRequestBody(req)
		return nil, err
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func (t *originGuardTransport) CloseIdleConnections() {
	if transport, ok := t.Base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

func closeRequestBody(req *http.Request) {
	if req != nil && req.Body != nil {
		_ = req.Body.Close()
	}
}

func (t *QuotaProjectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("x-goog-user-project", t.Project)
	return t.Base.RoundTrip(req)
}

func (t *QuotaProjectTransport) CloseIdleConnections() {
	if transport, ok := t.Base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
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
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, &APIError{Code: resp.StatusCode, Status: apiErr.Error.Status, Message: apiErr.Error.Message}
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
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func sameOrigin(a, b *url.URL) bool {
	aPort := a.Port()
	if aPort == "" {
		aPort = defaultPort(a.Scheme)
	}
	bPort := b.Port()
	if bPort == "" {
		bPort = defaultPort(b.Scheme)
	}

	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		aPort == bPort
}

func sameOriginRedirectPolicy(
	previous func(*http.Request, []*http.Request) error,
) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if previous != nil {
			if err := previous(req, via); err != nil {
				return err
			}
		}
		if len(via) > 0 {
			if err := validateRequestOrigin(req, via[0].URL); err != nil {
				return fmt.Errorf("refusing redirect: %w", err)
			}
		}
		if previous == nil && len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
}

func authAllowedOrigin(cfg AuthConfig) (*url.URL, error) {
	rawURL := cfg.AllowedOrigin
	if rawURL == "" {
		rawURL = DefaultV1BaseURL
	}
	origin, err := parseAbsoluteHTTPURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid allowed origin: %w", err)
	}
	return &url.URL{Scheme: origin.Scheme, Host: origin.Host}, nil
}

func parseAbsoluteHTTPURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateHTTPURL(parsed); err != nil {
		return nil, fmt.Errorf("%w: %q", err, rawURL)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return parsed, nil
}

func validateHTTPURL(parsed *url.URL) error {
	if parsed == nil {
		return errors.New("URL is nil")
	}
	scheme := strings.ToLower(parsed.Scheme)
	invalidURL := (scheme != "http" && scheme != "https") ||
		parsed.Opaque != "" ||
		parsed.Host == "" ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		strings.Contains(parsed.Hostname(), "%") ||
		!isASCII(parsed.Hostname())
	if invalidURL {
		return errors.New("URL must be a hierarchical HTTP(S) URL without userinfo")
	}
	if port := parsed.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("URL has invalid port %q", port)
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return errors.New("URL has an empty port")
	}
	canonicalHost := canonicalAuthority(parsed)
	if !strings.EqualFold(parsed.Host, canonicalHost) {
		return fmt.Errorf("URL authority %q is not canonical %q", parsed.Host, canonicalHost)
	}
	return nil
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func canonicalAuthority(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	return host
}

func validateRequestOrigin(req *http.Request, origin *url.URL) error {
	if req == nil || req.URL == nil {
		return errors.New("request URL is nil")
	}
	if err := validateHTTPURL(req.URL); err != nil {
		return fmt.Errorf("invalid request URL: %w", err)
	}
	if !sameOrigin(origin, req.URL) {
		return fmt.Errorf("request URL origin %q is not allowed", req.URL.Host)
	}
	if req.Host == "" {
		return nil
	}
	hostURL, err := parseAbsoluteHTTPURL(req.URL.Scheme + "://" + req.Host)
	if err != nil {
		return fmt.Errorf("invalid request Host override %q: %w", req.Host, err)
	}
	if hostURL.Path != "" || hostURL.RawPath != "" || hostURL.RawQuery != "" ||
		hostURL.Fragment != "" || hostURL.RawFragment != "" || hostURL.ForceQuery {
		return fmt.Errorf("request Host override %q must be an authority only", req.Host)
	}
	canonicalHost := canonicalAuthority(hostURL)
	if !strings.EqualFold(req.Host, canonicalHost) {
		return fmt.Errorf("request Host override %q is not canonical authority %q", req.Host, canonicalHost)
	}
	if !sameOrigin(req.URL, hostURL) {
		return fmt.Errorf("request Host override %q does not match URL origin %q", req.Host, req.URL.Host)
	}
	return nil
}

func (c *Client) requestHTTPClient(reqURL string) (*http.Client, error) {
	baseURL, err := parseAbsoluteHTTPURL(c.baseURL())
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	targetURL, err := parseAbsoluteHTTPURL(reqURL)
	if err != nil {
		return nil, fmt.Errorf("invalid request URL: %w", err)
	}
	if !sameOrigin(baseURL, targetURL) {
		return nil, fmt.Errorf("request URL origin %q does not match base URL origin %q", targetURL.Host, baseURL.Host)
	}

	client := *c.httpClient()
	client.CheckRedirect = sameOriginRedirectPolicy(client.CheckRedirect)
	client.Transport = &originGuardTransport{Base: client.Transport, Origin: baseURL}
	return &client, nil
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

// DoAPIRequest sends an authenticated API request. reqURL must be absolute and
// share an origin with BaseURL; redirects to another origin are rejected.
func (c *Client) DoAPIRequest(ctx context.Context, method, reqURL string, body []byte, contentType string) ([]byte, error) {
	httpClient, err := c.requestHTTPClient(reqURL)
	if err != nil {
		return nil, err
	}

	backoff := time.Second
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

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if c.Verbose {
			dump, _ := httputil.DumpResponse(resp, false)
			_, _ = fmt.Fprintf(c.verboseWriter(), "%s", dump)
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
				_, _ = fmt.Fprintf(c.verboseWriter(), "Rate limited, retrying after %v...\n", wait)
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
// trailing slashes are stripped. URL-like inputs must be hierarchical ASCII
// HTTP(S) URLs without userinfo; empty or invalid inputs return "".
func NormalizeDocName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	lowerName := strings.ToLower(name)
	parsedCandidate, parseErr := url.Parse(name)
	explicitURLSyntax := strings.HasPrefix(lowerName, "http:") ||
		strings.HasPrefix(lowerName, "https:") ||
		strings.HasPrefix(name, "//") ||
		strings.Contains(name, "://")
	authorityClass := notAuthority
	if !explicitURLSyntax {
		authorityClass = classifySchemeLessAuthority(name)
	}
	if authorityClass == invalidAuthority {
		return ""
	}
	looksLikeURL := explicitURLSyntax ||
		(parseErr == nil && parsedCandidate.IsAbs() && authorityClass != validAuthority)
	if looksLikeURL {
		u, err := parseAbsoluteHTTPURL(name)
		if err != nil {
			return ""
		}

		host := strings.ToLower(u.Host)
		if port := u.Port(); port != "" && port == defaultPort(u.Scheme) {
			host = strings.ToLower(u.Hostname())
			if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
		}
		path := strings.TrimRight(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
		if path == "" {
			return "documents/" + host
		}
		return "documents/" + host + "/" + path
	}

	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimRight(name, "/")
	if name == "" || name == "documents" {
		return ""
	}

	if !strings.HasPrefix(name, "documents/") {
		name = "documents/" + name
	}
	return name
}

type authorityClassification uint8

const (
	notAuthority authorityClassification = iota
	validAuthority
	invalidAuthority
)

func classifySchemeLessAuthority(name string) authorityClassification {
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	authority, _, _ := strings.Cut(name, "/")
	if strings.Contains(authority, "@") {
		return invalidAuthority
	}
	if !strings.HasPrefix(authority, "[") && !strings.Contains(authority, ":") {
		return notAuthority
	}
	parsed, err := parseAbsoluteHTTPURL("http://" + authority)
	if err != nil {
		return invalidAuthority
	}
	if parsed.Port() != "" || strings.Contains(parsed.Hostname(), ":") {
		return validAuthority
	}
	return invalidAuthority
}

func IsBisectableDocumentError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Status {
	case "INVALID_ARGUMENT":
		return ae.Code == http.StatusBadRequest
	case "NOT_FOUND":
		return ae.Code == http.StatusNotFound
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
