package dkapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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
	names := make([]string, MaxBatchGetDocuments+1)
	for i := range names {
		names[i] = "documents/example.com/a"
	}
	_, err := client.BatchGetDocuments(context.Background(), names)
	if err == nil {
		t.Fatal("expected error for too many names")
	}
}

func TestBatchGetDocumentsAllChunks(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"documents":[{"name":"documents/example.com/a","content":"A"}]}`)),
				}, nil
			}),
		},
	}

	names := make([]string, MaxBatchGetDocuments+1)
	for i := range names {
		names[i] = "documents/example.com/a"
	}
	docs, err := client.BatchGetDocumentsAll(context.Background(), names)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("len(docs) = %d, want 2", len(docs))
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
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
		{in: "HTTPS://developers.google.com/knowledge/api?q=1#frag", want: "documents/developers.google.com/knowledge/api"},
		{in: "documents/example.com/a/", want: "documents/example.com/a"},
		{in: "documents/example.com/a?foo=bar", want: "documents/example.com/a"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeDocName(tt.in); got != tt.want {
				t.Fatalf("NormalizeDocName(%q) = %q, want %q", tt.in, got, tt.want)
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

func TestDoAPIRequestMaxRetriesMeansRetries(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &Client{
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

func TestDoAPIRequestRetriesServiceUnavailable(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &Client{
		MaxRetries: 1,
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return &http.Response{
						StatusCode: http.StatusServiceUnavailable,
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

func TestDoAPIRequestRetriesTransportError(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &Client{
		MaxRetries: 1,
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return nil, errors.New("connection reset")
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

func TestRetryWaitDurationCapsRetryAfter(t *testing.T) {
	t.Parallel()

	got := retryWaitDuration(time.Second, 2*time.Minute, defaultMaxRetryBackoff)
	if got > defaultMaxRetryBackoff {
		t.Fatalf("wait = %v, want <= %v", got, defaultMaxRetryBackoff)
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
