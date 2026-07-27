package dkapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetDocument(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/documents/example.com/a" {
			t.Errorf("path = %q, want /v1/documents/example.com/a", r.URL.Path)
		}
		if _, ok := r.URL.Query()["view"]; ok {
			t.Errorf("unexpected view query parameter: %q", r.URL.RawQuery)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q, want test-key", got)
		}
		_, _ = io.WriteString(w, `{"name":"documents/example.com/a","uri":"https://example.com/a","content":"body","contentLengthBytes":4}`)
	}))
	defer server.Close()

	client := &Client{
		BaseURL:    server.URL + "/v1",
		APIKey:     "test-key",
		HTTPClient: server.Client(),
	}
	doc, err := client.GetDocument(context.Background(), "documents/example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Name != "documents/example.com/a" {
		t.Errorf("name = %q, want documents/example.com/a", doc.Name)
	}
	if doc.Content != "body" {
		t.Errorf("content = %q, want body", doc.Content)
	}
	if doc.ContentLengthBytes != 4 {
		t.Errorf("content length = %d, want 4", doc.ContentLengthBytes)
	}
}

func TestGetDocumentRequestsDocumentView(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("view"); got != string(DocumentViewBasic) {
			t.Errorf("view = %q, want %q", got, DocumentViewBasic)
		}
		// The BASIC view omits content but still reports the content length.
		_, _ = io.WriteString(w, `{"name":"documents/example.com/a","view":"DOCUMENT_VIEW_BASIC","contentLengthBytes":31940}`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	doc, err := client.GetDocument(
		context.Background(),
		"documents/example.com/a",
		WithDocumentView(DocumentViewBasic),
	)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Content != "" {
		t.Errorf("content = %q, want empty", doc.Content)
	}
	if doc.ContentLengthBytes != 31940 {
		t.Errorf("content length = %d, want 31940", doc.ContentLengthBytes)
	}
}

func TestGetDocumentPreservesEscapedResourcePath(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/v1/documents/example.com/a%2Fb"; got != want {
			t.Errorf("escaped path = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `{"name":"documents/example.com/a%2Fb"}`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	if _, err := client.GetDocument(context.Background(), "documents/example.com/a%2Fb"); err != nil {
		t.Fatal(err)
	}
}

func TestGetDocumentOmitsUnspecifiedView(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["view"]; ok {
			t.Errorf("unexpected view query parameter: %q", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"name":"documents/example.com/a"}`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	if _, err := client.GetDocument(
		context.Background(),
		"documents/example.com/a",
		WithDocumentView(DocumentViewUnspecified),
	); err != nil {
		t.Fatal(err)
	}
}

func TestGetDocumentRejectsInvalidRequestsBeforeHTTP(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	tests := []struct {
		name    string
		docName string
		opts    []DocumentOption
		want    string
	}{
		{name: "empty", docName: "", want: "requires a document name"},
		{name: "whitespace", docName: " \t\n", want: "requires a document name"},
		{name: "leading whitespace", docName: " documents/example.com/a", want: "leading or trailing whitespace"},
		{name: "trailing whitespace", docName: "documents/example.com/a ", want: "leading or trailing whitespace"},
		{name: "leading slash", docName: "/documents/example.com/a", want: "without a leading slash"},
		{name: "query", docName: "documents/example.com/a?view=x", want: "without a leading slash"},
		{name: "fragment", docName: "documents/example.com/a#frag", want: "without a leading slash"},
		{
			name:    "unknown view",
			docName: "documents/example.com/a",
			opts:    []DocumentOption{WithDocumentView(DocumentView("DOCUMENT_VIEW_FUTURE"))},
			want:    "invalid document view",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := client.GetDocument(context.Background(), tt.docName, tt.opts...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("GetDocument() error = %v, want containing %q", err, tt.want)
			}
			if doc != nil {
				t.Fatalf("GetDocument() document = %+v, want nil", doc)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server requests = %d, want 0", got)
	}
}

func TestGetDocumentRejectsNullResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `null`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	_, err := client.GetDocument(context.Background(), "documents/example.com/a")
	if err == nil || !strings.Contains(err.Error(), "expected object, got null") {
		t.Fatalf("GetDocument() error = %v, want null response error", err)
	}
}

func TestGetDocumentReturnsAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"status":"NOT_FOUND","message":"missing"}}`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	_, err := client.GetDocument(context.Background(), "documents/example.com/missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("GetDocument() error = %v, want *APIError", err)
	}
	if apiErr.Code != http.StatusNotFound || apiErr.Status != "NOT_FOUND" {
		t.Fatalf("APIError = %#v, want 404 NOT_FOUND", apiErr)
	}
	if !IsBisectableDocumentError(err) {
		t.Errorf("IsBisectableDocumentError() = false, want true for 404 NOT_FOUND")
	}
}
