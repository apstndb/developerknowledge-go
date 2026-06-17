package dkapi

import (
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

	docs, err := client.BatchGetDocuments([]string{"documents/example.com/a", "documents/example.com/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Content != "A" {
		t.Fatalf("docs = %+v", docs)
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

	body, err := client.DoGet("https://example.test/v1/documents/example.com/a")
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
