package dkapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type closeTrackingBody struct {
	closed atomic.Int32
}

func (*closeTrackingBody) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (b *closeTrackingBody) Close() error {
	b.closed.Add(1)
	return nil
}

type countingTokenSource struct {
	calls atomic.Int32
}

func (s *countingTokenSource) Token() (*oauth2.Token, error) {
	s.calls.Add(1)
	return &oauth2.Token{AccessToken: "test-token"}, nil
}

func quotaProjectTransport(client *http.Client) (*QuotaProjectTransport, bool) {
	transport := client.Transport
	if guard, ok := transport.(*originGuardTransport); ok {
		transport = guard.Base
	}
	quotaTransport, ok := transport.(*QuotaProjectTransport)
	return quotaTransport, ok
}

func setTestHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
		t.Setenv("APPDATA", filepath.Join(dir, "AppData", "Roaming"))
		return
	}
	t.Setenv("HOME", dir)
}

func writeAuthorizedUserCredentials(t *testing.T, path, quotaProject string) {
	t.Helper()
	config := map[string]string{
		"type":          "authorized_user",
		"client_id":     "test-client-id",
		"client_secret": "test-client-secret",
		"refresh_token": "test-refresh-token",
	}
	if quotaProject != "" {
		config["quota_project_id"] = quotaProject
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBatchGetDocuments(t *testing.T) {
	t.Parallel()

	client := &Client{
		BaseURL: "https://example.test/v1",
		APIKey:  "test-key",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://example.test/v1/documents:batchGet?names=documents%2Fexample.com%2Fa&names=documents%2Fexample.com%2Fb" {
					t.Fatalf("url = %q", req.URL.String())
				}
				if got := req.Header.Get("x-goog-api-key"); got != "test-key" {
					t.Fatalf("x-goog-api-key = %q, want test-key", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"documents":[{"name":"documents/example.com/a","content":"A"}]}`)),
				}, nil
			}),
		},
	}

	docs, err := client.BatchGetDocuments(context.Background(), []string{"documents/example.com/a", "documents/example.com/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Content != "A" {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestBatchGetDocumentsRejectsTooManyNames(t *testing.T) {
	t.Parallel()

	client := &Client{}
	names := make([]string, 21)
	for i := range names {
		names[i] = "documents/example.com/a"
	}
	_, err := client.BatchGetDocuments(context.Background(), names)
	if err == nil {
		t.Fatal("expected error for 21 names")
	}
	if !strings.Contains(err.Error(), "at most 20 document names, got 21") {
		t.Fatalf("error = %q", err)
	}
}

func TestBatchGetDocumentsAccepts20Names(t *testing.T) {
	t.Parallel()

	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if got := len(req.URL.Query()["names"]); got != 20 {
					t.Fatalf("len(names) = %d, want 20", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"documents":[]}`)),
				}, nil
			}),
		},
	}

	names := make([]string, 20)
	for i := range names {
		names[i] = "documents/example.com/a"
	}
	if _, err := client.BatchGetDocuments(context.Background(), names); err != nil {
		t.Fatal(err)
	}
}

func TestBatchGetDocumentsAllChunks(t *testing.T) {
	t.Parallel()

	var chunkSizes []int
	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				chunkSizes = append(chunkSizes, len(req.URL.Query()["names"]))
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"documents":[{"name":"documents/example.com/a","content":"A"}]}`)),
				}, nil
			}),
		},
	}

	names := make([]string, 41)
	for i := range names {
		names[i] = "documents/example.com/a"
	}
	docs, err := client.BatchGetDocumentsAll(context.Background(), names)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("len(docs) = %d, want 3", len(docs))
	}
	if got, want := chunkSizes, []int{20, 20, 1}; !slices.Equal(got, want) {
		t.Fatalf("chunk sizes = %v, want %v", got, want)
	}
}

func TestNormalizeDocName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "developers.google.com/knowledge/api", want: "documents/developers.google.com/knowledge/api"},
		{in: "https://developers.google.com/knowledge/api", want: "documents/developers.google.com/knowledge/api"},
		{in: "https://developers.google.com/knowledge/api/", want: "documents/developers.google.com/knowledge/api"},
		{in: "HTTPS://developers.google.com/knowledge/api?q=1#frag", want: "documents/developers.google.com/knowledge/api"},
		{in: "https://EXAMPLE.COM:443/a%2Fb/?q=1#frag", want: "documents/example.com/a%2Fb"},
		{in: "https://EXAMPLE.COM:8443/a", want: "documents/example.com:8443/a"},
		{in: "example.com:8443/a", want: "documents/example.com:8443/a"},
		{in: "localhost:8080/a", want: "documents/localhost:8080/a"},
		{in: "example.com:8443?view=full", want: "documents/example.com:8443"},
		{in: "localhost:8080#section", want: "documents/localhost:8080"},
		{in: "https://[2001:db8::1]/a", want: "documents/[2001:db8::1]/a"},
		{in: "https://[2001:db8::1]:443/a", want: "documents/[2001:db8::1]/a"},
		{in: "https://[2001:db8::1]:8443/a", want: "documents/[2001:db8::1]:8443/a"},
		{in: "[2001:db8::1]/a", want: "documents/[2001:db8::1]/a"},
		{in: "[2001:db8::1]:8443/a", want: "documents/[2001:db8::1]:8443/a"},
		{in: "https://:443/a", want: ""},
		{in: "https:///missing-host", want: ""},
		{in: "https:/missing-host", want: ""},
		{in: "https:opaque", want: ""},
		{in: "https://user@example.com/a", want: ""},
		{in: "https://[example.com]/a", want: ""},
		{in: "https://xßx.invalid/a", want: ""},
		{in: "https://xẞx.invalid/a", want: ""},
		{in: "[example.com]/a", want: ""},
		{in: "[example.com]:8443/a", want: ""},
		{in: "[127.0.0.1]:8443/a", want: ""},
		{in: "user@example.com:8443/a", want: ""},
		{in: "ftp://example.com/a", want: ""},
		{in: "ftp:opaque", want: ""},
		{in: "mailto:user@example.com", want: ""},
		{in: "file:/tmp/a", want: ""},
		{in: "//example.com/a", want: ""},
		{in: "   ", want: ""},
		{in: "documents/example.com/a/", want: "documents/example.com/a"},
		{in: "documents/example.com/a///", want: "documents/example.com/a"},
		{in: "documents/example.com/a?foo=bar", want: "documents/example.com/a"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := NormalizeDocName(tt.in)
			if got != tt.want {
				t.Fatalf("NormalizeDocName(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if gotAgain := NormalizeDocName(got); gotAgain != got {
				t.Fatalf("NormalizeDocName(%q) after normalization = %q, want %q", got, gotAgain, got)
			}
		})
	}
}

func TestResolveQuotaProjectIDPrefersEnv(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "env-project")
	got, _ := ResolveQuotaProjectID(nil)
	if got != "env-project" {
		t.Fatalf("ResolveQuotaProjectID() = %q, want env-project", got)
	}
}

func TestQuotaProjectTransportSetsHeader(t *testing.T) {
	t.Parallel()

	var gotHeader string
	transport := &QuotaProjectTransport{
		Base: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			gotHeader = req.Header.Get("x-goog-user-project")
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
		}),
		Project: "quota-project",
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "quota-project" {
		t.Fatalf("x-goog-user-project = %q, want quota-project", gotHeader)
	}
}

func TestCheckResponseReturnsBodyReadError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("read failed")
	_, err := CheckResponse(&http.Response{
		StatusCode: http.StatusOK,
		Body:       errReadCloser{err: wantErr},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestDocumentURIIsNotOmittedFromJSON(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(Document{Name: "documents/example.com/a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"uri":""`) {
		t.Fatalf("Document JSON = %s, want empty uri field present", got)
	}
}

func TestDefaultCredentialsPathUsesCloudSDKConfig(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "/tmp/custom-gcloud")

	got := DefaultCredentialsPath()
	want := "/tmp/custom-gcloud/application_default_credentials.json"
	if got != want {
		t.Fatalf("DefaultCredentialsPath() = %q, want %q", got, want)
	}
}

func TestResolveADCCredentialsFallsBackWhenCloudSDKConfigADCIsMissing(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	cloudSDKConfig := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", cloudSDKConfig)
	setTestHome(t, t.TempDir())

	originalDefaultTokenSource := DefaultTokenSource
	t.Cleanup(func() {
		DefaultTokenSource = originalDefaultTokenSource
	})

	defaultCalled := false
	DefaultTokenSource = func(_ context.Context, scopes ...string) (oauth2.TokenSource, error) {
		defaultCalled = true
		if len(scopes) != 1 || scopes[0] != CloudPlatformScope {
			t.Fatalf("scopes = %v, want [%q]", scopes, CloudPlatformScope)
		}
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}

	if _, _, err := resolveADCCredentials(context.Background(), AuthConfig{}); err != nil {
		t.Fatalf("resolveADCCredentials() error = %v, want default token source fallback", err)
	}
	if !defaultCalled {
		t.Fatal("default token source was not called")
	}
}

func TestResolveADCCredentialsKeepsMetadataWhenCloudSDKConfigFallsBack(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	setTestHome(t, t.TempDir())

	path := platformDefaultCredentialsPath()
	writeAuthorizedUserCredentials(t, path, "quota-project")

	ts, metadata, err := resolveADCCredentials(context.Background(), AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if ts == nil {
		t.Fatal("token source is nil")
	}
	if metadata.Type != "authorized_user" {
		t.Fatalf("credential type = %q, want authorized_user", metadata.Type)
	}
	if metadata.QuotaProjectID != "quota-project" {
		t.Fatalf("quota project = %q, want quota-project", metadata.QuotaProjectID)
	}
}

func TestResolveADCCredentialsDoesNotFallbackOnCloudSDKConfigPathError(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	setTestHome(t, t.TempDir())
	writeAuthorizedUserCredentials(t, platformDefaultCredentialsPath(), "fallback-project")

	cloudSDKConfig := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cloudSDKConfig, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUDSDK_CONFIG", cloudSDKConfig)

	originalDefaultTokenSource := DefaultTokenSource
	t.Cleanup(func() {
		DefaultTokenSource = originalDefaultTokenSource
	})
	defaultCalled := false
	DefaultTokenSource = func(context.Context, ...string) (oauth2.TokenSource, error) {
		defaultCalled = true
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}

	_, _, err := resolveADCCredentials(context.Background(), AuthConfig{})
	if err == nil {
		t.Fatal("resolveADCCredentials() error = nil, want path error")
	}
	if !strings.Contains(err.Error(), "read ADC credentials") {
		t.Fatalf("resolveADCCredentials() error = %v, want ADC read context", err)
	}
	if defaultCalled {
		t.Fatal("default token source was called after CLOUDSDK_CONFIG path error")
	}
}

func TestResolveADCCredentialsDoesNotFallbackFromDanglingCloudSDKConfigADC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires additional privileges on Windows")
	}

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	setTestHome(t, t.TempDir())
	writeAuthorizedUserCredentials(t, platformDefaultCredentialsPath(), "fallback-project")

	cloudSDKConfig := t.TempDir()
	adcPath := filepath.Join(cloudSDKConfig, "application_default_credentials.json")
	if err := os.Symlink(filepath.Join(cloudSDKConfig, "missing-target.json"), adcPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUDSDK_CONFIG", cloudSDKConfig)

	_, _, err := resolveADCCredentials(context.Background(), AuthConfig{})
	if err == nil {
		t.Fatal("resolveADCCredentials() error = nil, want dangling-symlink error")
	}
	if !strings.Contains(err.Error(), "read ADC credentials") {
		t.Fatalf("resolveADCCredentials() error = %v, want ADC read context", err)
	}
}

func TestResolveADCCredentialsReturnsErrorWhenExplicitCredentialsAreMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv("CLOUDSDK_CONFIG", "")

	originalDefaultTokenSource := DefaultTokenSource
	t.Cleanup(func() {
		DefaultTokenSource = originalDefaultTokenSource
	})

	defaultCalled := false
	DefaultTokenSource = func(context.Context, ...string) (oauth2.TokenSource, error) {
		defaultCalled = true
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}

	if _, _, err := resolveADCCredentials(context.Background(), AuthConfig{}); err == nil {
		t.Fatal("resolveADCCredentials() error = nil, want missing explicit credentials error")
	}
	if defaultCalled {
		t.Fatal("default token source was called for explicit credentials")
	}
}

func TestNewADCHTTPClientExplicitCredentialsPathIsStrictAndEvaluatedOnce(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	pathCalls := 0
	path := filepath.Join(t.TempDir(), "missing-credentials.json")
	_, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode: AuthRequireADC,
		CredentialsPath: func() string {
			pathCalls++
			return path
		},
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NewADCHTTPClient() error = %v, want os.ErrNotExist", err)
	}
	if pathCalls != 1 {
		t.Fatalf("CredentialsPath calls = %d, want 1", pathCalls)
	}
}

func TestNewADCHTTPClientCustomTokenSourceDoesNotReadCredentialsPath(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	pathCalls := 0
	client, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode: AuthRequireADC,
		TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
		},
		CredentialsPath: func() string {
			pathCalls++
			return filepath.Join(t.TempDir(), "credentials.json")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pathCalls != 0 {
		t.Fatalf("CredentialsPath calls = %d, want 0", pathCalls)
	}
	if _, ok := quotaProjectTransport(client); ok {
		t.Fatal("custom token source unexpectedly used credentials-file quota metadata")
	}
}

func TestNewADCHTTPClientRejectsCrossOriginRedirect(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	var targetReached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetReached.Store(true)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	client, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode:          AuthRequireADC,
		AllowedOrigin: source.URL,
		TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(source.URL); err == nil {
		t.Fatal("http.Client.Get() error = nil, want cross-origin redirect error")
	}
	if targetReached.Load() {
		t.Fatal("cross-origin redirect target was reached")
	}
}

func TestNewADCHTTPClientValidatesAllowedOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		allowedOrigin string
		wantErr       bool
	}{
		{name: "empty hostname", allowedOrigin: "http://:8080", wantErr: true},
		{name: "empty hostname default port", allowedOrigin: "https://:443", wantErr: true},
		{name: "non HTTP scheme", allowedOrigin: "ftp://example.com", wantErr: true},
		{name: "userinfo", allowedOrigin: "https://user@example.com", wantErr: true},
		{name: "opaque", allowedOrigin: "https:opaque", wantErr: true},
		{name: "empty port", allowedOrigin: "https://example.com:", wantErr: true},
		{name: "port out of range", allowedOrigin: "https://example.com:65536", wantErr: true},
		{name: "bracketed DNS name", allowedOrigin: "https://[example.com]", wantErr: true},
		{name: "bracketed IPv4", allowedOrigin: "https://[127.0.0.1]", wantErr: true},
		{name: "Unicode sharp s", allowedOrigin: "https://xßx.invalid", wantErr: true},
		{name: "Unicode capital sharp s", allowedOrigin: "https://xẞx.invalid", wantErr: true},
		{name: "IPv4", allowedOrigin: "http://127.0.0.1:8080"},
		{name: "localhost", allowedOrigin: "http://localhost:8080"},
		{name: "HTTPS", allowedOrigin: "https://example.com"},
		{name: "IPv6", allowedOrigin: "https://[2001:db8::1]:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokenSourceCalled := false
			client, err := NewADCHTTPClient(context.Background(), AuthConfig{
				Mode:          AuthRequireADC,
				AllowedOrigin: tt.allowedOrigin,
				TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
					tokenSourceCalled = true
					return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
				},
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewADCHTTPClient() error = nil, want invalid-origin error")
				}
				if tokenSourceCalled {
					t.Fatal("token source was called for invalid allowed origin")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if client == nil {
				t.Fatal("NewADCHTTPClient() client = nil")
			}
			if !tokenSourceCalled {
				t.Fatal("token source was not called for valid allowed origin")
			}
		})
	}
}

func TestOriginGuardTransportClosesRejectedRequestBody(t *testing.T) {
	t.Parallel()

	client, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode:          AuthRequireADC,
		AllowedOrigin: "https://allowed.example",
		TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := &closeTrackingBody{}
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://blocked.example",
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err == nil {
		t.Fatal("http.Client.Do() error = nil, want origin error")
	}
	if got := body.closed.Load(); got != 1 {
		t.Fatalf("request body Close calls = %d, want 1", got)
	}
}

func TestOriginGuardTransportRejectsNilOrigin(t *testing.T) {
	t.Parallel()

	transportCalled := false
	guard := &originGuardTransport{
		Base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			transportCalled = true
			return nil, errors.New("unexpected request")
		}),
	}
	body := &closeTrackingBody{}
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://allowed.example",
		body,
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := guard.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip() error = nil, want nil-origin error")
	}
	if transportCalled {
		t.Fatal("base transport was called for nil origin")
	}
	if got := body.closed.Load(); got != 1 {
		t.Fatalf("request body Close calls = %d, want 1", got)
	}
}

func TestOriginGuardTransportRejectsMutatedRequestAuthority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "opaque URL",
			mutate: func(req *http.Request) {
				req.URL.Opaque = "//attacker.example/path"
			},
		},
		{
			name: "Host override",
			mutate: func(req *http.Request) {
				req.Host = "attacker.example"
			},
		},
		{
			name: "Host path suffix",
			mutate: func(req *http.Request) {
				req.Host = req.URL.Host + "/alternate"
			},
		},
		{
			name: "Host query suffix",
			mutate: func(req *http.Request) {
				req.Host = req.URL.Host + "?alternate"
			},
		},
		{
			name: "Host fragment suffix",
			mutate: func(req *http.Request) {
				req.Host = req.URL.Host + "#alternate"
			},
		},
		{
			name: "bracketed non-IPv6 Host",
			mutate: func(req *http.Request) {
				req.Host = "[" + req.URL.Hostname() + "]:" + req.URL.Port()
			},
		},
		{
			name: "bracketed non-IPv6 URL Host",
			mutate: func(req *http.Request) {
				req.URL.Host = "[" + req.URL.Hostname() + "]:" + req.URL.Port()
				req.Host = ""
			},
		},
		{
			name: "URL userinfo",
			mutate: func(req *http.Request) {
				req.URL.User = url.UserPassword("user", "password")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var serverReached atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				serverReached.Store(true)
			}))
			defer server.Close()

			tokenSource := &countingTokenSource{}
			client, err := NewADCHTTPClient(context.Background(), AuthConfig{
				Mode:           AuthRequireADC,
				AllowedOrigin:  server.URL,
				QuotaProjectID: "quota-project",
				TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
					return tokenSource, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			body := &closeTrackingBody{}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, body)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(req)

			if _, err := client.Do(req); err == nil {
				t.Fatal("http.Client.Do() error = nil, want request-authority error")
			}
			if serverReached.Load() {
				t.Fatal("server was reached for mutated request authority")
			}
			if got := tokenSource.calls.Load(); got != 0 {
				t.Fatalf("Token calls = %d, want 0", got)
			}
			if got := body.closed.Load(); got != 1 {
				t.Fatalf("request body Close calls = %d, want 1", got)
			}
		})
	}
}

func TestOriginGuardTransportAllowsCanonicalHostOverride(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host)
	}))
	defer server.Close()

	client, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode:          AuthRequireADC,
		AllowedOrigin: server.URL,
		TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = req.URL.Host
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != req.Host {
		t.Fatalf("Host = %q, want %q", body, req.Host)
	}
}

func TestNewAuthenticatedHTTPClientRejectsUnapprovedInitialOrigin(t *testing.T) {
	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	var blockedReached atomic.Bool
	blocked := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		blockedReached.Store(true)
	}))
	defer blocked.Close()

	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if got := r.Header.Get("x-goog-user-project"); got != "quota-project" {
			t.Errorf("x-goog-user-project = %q, want quota-project", got)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer allowed.Close()

	client, apiKey, err := NewAuthenticatedHTTPClient(context.Background(), AuthConfig{
		Mode:           AuthRequireADC,
		AllowedOrigin:  allowed.URL,
		QuotaProjectID: "quota-project",
		TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "" {
		t.Fatalf("apiKey = %q, want empty", apiKey)
	}
	if _, err := client.Get(blocked.URL); err == nil {
		t.Fatal("http.Client.Get() error = nil, want unapproved-origin error")
	}
	if blockedReached.Load() {
		t.Fatal("unapproved initial origin was reached")
	}

	resp, err := client.Get(allowed.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestNewADCHTTPClientCustomTokenSourceHonorsQuotaProjectEnv(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")

	client, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode: AuthRequireADC,
		TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := quotaProjectTransport(client)
	if !ok {
		t.Fatalf("client.Transport = %T, want *QuotaProjectTransport", client.Transport)
	}
	if transport.Project != "quota-project" {
		t.Fatalf("quota project = %q, want quota-project", transport.Project)
	}
}

func TestNewADCHTTPClientQuotaProjectConfigOverridesEnv(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "env-project")

	client, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode:           AuthRequireADC,
		QuotaProjectID: "config-project",
		TokenSource: func(context.Context, ...string) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := quotaProjectTransport(client)
	if !ok {
		t.Fatalf("client.Transport = %T, want *QuotaProjectTransport", client.Transport)
	}
	if transport.Project != "config-project" {
		t.Fatalf("quota project = %q, want config-project", transport.Project)
	}
}

func TestNewADCHTTPClientCredentialsPathIsEvaluatedOnce(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	path := filepath.Join(t.TempDir(), "credentials.json")
	writeAuthorizedUserCredentials(t, path, "quota-project")

	pathCalls := 0
	client, err := NewADCHTTPClient(context.Background(), AuthConfig{
		Mode: AuthRequireADC,
		CredentialsPath: func() string {
			pathCalls++
			return path
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pathCalls != 1 {
		t.Fatalf("CredentialsPath calls = %d, want 1", pathCalls)
	}
	transport, ok := quotaProjectTransport(client)
	if !ok {
		t.Fatalf("client.Transport = %T, want *QuotaProjectTransport", client.Transport)
	}
	if transport.Project != "quota-project" {
		t.Fatalf("quota project = %q, want quota-project", transport.Project)
	}
}

func TestNewADCHTTPClientExternalAccountQuotaProject(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	tests := []struct {
		name             string
		credentialFields map[string]any
		wantQuotaProject string
	}{
		{name: "no quota project", credentialFields: map[string]any{}},
		{
			name:             "quota project",
			credentialFields: map[string]any{"quota_project_id": "quota-project"},
			wantQuotaProject: "quota-project",
		},
		{
			name: "workforce user project is not API quota project",
			credentialFields: map[string]any{
				"audience":                    "//iam.googleapis.com/locations/global/workforcePools/pool/providers/provider",
				"workforce_pool_user_project": "workforce-project",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]any{
				"type":               "external_account",
				"audience":           "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
				"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
				"token_url":          "https://sts.googleapis.com/v1/token",
				"credential_source": map[string]any{
					"file": filepath.Join(t.TempDir(), "subject-token"),
				},
			}
			for key, value := range tt.credentialFields {
				config[key] = value
			}
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "credentials.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			client, err := NewADCHTTPClient(context.Background(), AuthConfig{
				Mode:            AuthRequireADC,
				CredentialsPath: func() string { return path },
			})
			if err != nil {
				t.Fatal(err)
			}
			transport, hasQuotaProject := quotaProjectTransport(client)
			if tt.wantQuotaProject == "" {
				if hasQuotaProject {
					t.Fatalf("unexpected quota project %q", transport.Project)
				}
				return
			}
			if !hasQuotaProject {
				t.Fatalf("client.Transport = %T, want *QuotaProjectTransport", client.Transport)
			}
			if transport.Project != tt.wantQuotaProject {
				t.Fatalf("quota project = %q, want %q", transport.Project, tt.wantQuotaProject)
			}
		})
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		Header: http.Header{
			"Retry-After": []string{time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)},
		},
	}
	got := ParseRetryAfter(resp)
	if got <= 0 || got > time.Minute {
		t.Fatalf("ParseRetryAfter() = %v, want a positive duration no greater than 1m", got)
	}
}

func TestCheckResponseAcceptsOther2xxStatuses(t *testing.T) {
	t.Parallel()

	body, err := CheckResponse(&http.Response{
		StatusCode: http.StatusCreated,
		Body:       io.NopCloser(strings.NewReader("created")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "created" {
		t.Fatalf("body = %q, want created", body)
	}
}

func TestCheckResponseUsesHTTPStatusForBisectableErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		httpStatus     int
		body           string
		wantCode       int
		wantBisectable bool
	}{
		{
			name:           "server error body claims not found",
			httpStatus:     http.StatusInternalServerError,
			body:           `{"error":{"code":404,"status":"NOT_FOUND","message":"proxy mismatch"}}`,
			wantCode:       http.StatusInternalServerError,
			wantBisectable: false,
		},
		{
			name:           "not found body omits numeric code",
			httpStatus:     http.StatusNotFound,
			body:           `{"error":{"status":"NOT_FOUND","message":"missing"}}`,
			wantCode:       http.StatusNotFound,
			wantBisectable: true,
		},
		{
			name:           "forbidden body claims not found",
			httpStatus:     http.StatusForbidden,
			body:           `{"error":{"code":404,"status":"NOT_FOUND","message":"forbidden"}}`,
			wantCode:       http.StatusForbidden,
			wantBisectable: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := CheckResponse(&http.Response{
				StatusCode: tt.httpStatus,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %T(%v), want *APIError", err, err)
			}
			if apiErr.Code != tt.wantCode {
				t.Fatalf("APIError.Code = %d, want %d", apiErr.Code, tt.wantCode)
			}
			if got := IsBisectableDocumentError(err); got != tt.wantBisectable {
				t.Fatalf("IsBisectableDocumentError() = %t, want %t", got, tt.wantBisectable)
			}
		})
	}
}

func TestDoAPIRequestMaxRetriesMeansRetries(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &Client{
		BaseURL:    "https://example.test/v1",
		MaxRetries: 1,
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Header:     http.Header{"Retry-After": []string{"0"}},
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
				}, nil
			}),
		},
	}

	body, err := client.DoGet(context.Background(), "https://example.test/v1/documents/example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestDoAPIRequestRetryExhaustionReturnsRateLimitError(t *testing.T) {
	t.Parallel()

	client := &Client{
		BaseURL:    "https://example.test/v1",
		MaxRetries: 0,
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Retry-After": []string{"1"}},
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}),
		},
	}

	_, err := client.DoGet(context.Background(), "https://example.test/v1/documents/example.com/a")
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("error = %T(%v), want *RateLimitError", err, err)
	}
}

func TestDoAPIRequestRejectsCrossOriginURL(t *testing.T) {
	t.Parallel()

	transportCalled := false
	client := &Client{
		BaseURL: "https://example.test/v1",
		APIKey:  "test-key",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				transportCalled = true
				return nil, errors.New("unexpected request")
			}),
		},
	}

	_, err := client.DoGet(context.Background(), "https://attacker.test/v1/documents/example.com/a")
	if err == nil {
		t.Fatal("DoGet() error = nil, want cross-origin error")
	}
	if transportCalled {
		t.Fatal("transport was called for cross-origin URL")
	}
}

func TestDoAPIRequestRejectsInvalidHTTPURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		reqURL  string
	}{
		{name: "empty base hostname", baseURL: "http://:8080", reqURL: "http://:8080/a"},
		{name: "bracketed DNS base", baseURL: "https://[example.com]/v1", reqURL: "https://[example.com]/v1/a"},
		{name: "Unicode base", baseURL: "https://xßx.invalid/v1", reqURL: "https://xẞx.invalid/v1/a"},
		{name: "request userinfo", baseURL: "https://example.com", reqURL: "https://user@example.com/a"},
		{name: "request non HTTP scheme", baseURL: "https://example.com", reqURL: "ftp://example.com/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			transportCalled := false
			client := &Client{
				BaseURL: tt.baseURL,
				HTTPClient: &http.Client{
					Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
						transportCalled = true
						return nil, errors.New("unexpected request")
					}),
				},
			}
			if _, err := client.DoGet(context.Background(), tt.reqURL); err == nil {
				t.Fatal("DoGet() error = nil, want invalid-URL error")
			}
			if transportCalled {
				t.Fatal("transport was called for invalid URL")
			}
		})
	}
}

func TestDoAPIRequestRejectsCrossOriginRedirect(t *testing.T) {
	t.Parallel()

	var targetReached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetReached.Store(true)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	oauthClient := oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}),
	)
	client := &Client{
		BaseURL: source.URL,
		APIKey:  "test-key",
		HTTPClient: &http.Client{
			Transport: &QuotaProjectTransport{
				Base:    oauthClient.Transport,
				Project: "quota-project",
			},
		},
	}

	if _, err := client.DoGet(context.Background(), source.URL); err == nil {
		t.Fatal("DoGet() error = nil, want cross-origin redirect error")
	}
	if targetReached.Load() {
		t.Fatal("cross-origin redirect target was reached")
	}
}

func TestDoAPIRequestRejectsRedirectCallbackOriginRewrite(t *testing.T) {
	t.Parallel()

	var targetReached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetReached.Store(true)
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/same-origin", http.StatusFound)
	}))
	defer source.Close()

	client := &Client{
		BaseURL: source.URL,
		HTTPClient: &http.Client{
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				req.URL = targetURL
				return nil
			},
		},
	}
	if _, err := client.DoGet(context.Background(), source.URL); err == nil {
		t.Fatal("DoGet() error = nil, want rewritten-origin error")
	}
	if targetReached.Load() {
		t.Fatal("rewritten cross-origin redirect target was reached")
	}
}

func TestDoAPIRequestRejectsRedirectCallbackAuthorityMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "opaque URL",
			mutate: func(req *http.Request) {
				req.URL.Opaque = "//attacker.example/path"
			},
		},
		{
			name: "Host override",
			mutate: func(req *http.Request) {
				req.Host = "attacker.example"
			},
		},
		{
			name: "Host path suffix",
			mutate: func(req *http.Request) {
				req.Host = req.URL.Host + "/alternate"
			},
		},
		{
			name: "bracketed non-IPv6 URL Host",
			mutate: func(req *http.Request) {
				req.URL.Host = "[" + req.URL.Hostname() + "]:" + req.URL.Port()
				req.Host = ""
			},
		},
		{
			name: "URL userinfo",
			mutate: func(req *http.Request) {
				req.URL.User = url.UserPassword("user", "password")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var redirectedRequestReached atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/redirected", http.StatusFound)
					return
				}
				redirectedRequestReached.Store(true)
			}))
			defer server.Close()

			client := &Client{
				BaseURL: server.URL,
				HTTPClient: &http.Client{
					CheckRedirect: func(req *http.Request, _ []*http.Request) error {
						tt.mutate(req)
						return nil
					},
				},
			}
			if _, err := client.DoGet(context.Background(), server.URL+"/start"); err == nil {
				t.Fatal("DoGet() error = nil, want mutated-authority error")
			}
			if redirectedRequestReached.Load() {
				t.Fatal("redirected request with mutated authority reached server")
			}
		})
	}
}

func TestDoAPIRequestAllowsSameOriginRedirect(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("x-goog-user-project"); got != "quota-project" {
			t.Errorf("x-goog-user-project = %q, want quota-project", got)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	oauthClient := oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}),
	)
	client := &Client{
		BaseURL: server.URL,
		APIKey:  "test-key",
		HTTPClient: &http.Client{
			Transport: &QuotaProjectTransport{
				Base:    oauthClient.Transport,
				Project: "quota-project",
			},
		},
	}

	body, err := client.DoGet(context.Background(), server.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

func TestIsBisectableDocumentError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "invalid argument", err: &APIError{Code: http.StatusBadRequest, Status: "INVALID_ARGUMENT"}, want: true},
		{name: "not found", err: &APIError{Code: http.StatusNotFound, Status: "NOT_FOUND"}, want: true},
		{name: "permission denied", err: &APIError{Code: http.StatusForbidden, Status: "PERMISSION_DENIED"}, want: false},
		{name: "server error", err: &APIError{Code: http.StatusInternalServerError, Status: "NOT_FOUND"}, want: false},
		{name: "rate limit", err: &RateLimitError{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsBisectableDocumentError(tt.err); got != tt.want {
				t.Fatalf("IsBisectableDocumentError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

type errReadCloser struct {
	err error
}

func (r errReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r errReadCloser) Close() error {
	return nil
}
