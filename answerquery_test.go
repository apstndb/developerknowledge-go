package dkapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAnswerQuery(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1:answerQuery" {
			t.Errorf("path = %q, want /v1:answerQuery", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q, want test-key", got)
		}

		var req AnswerQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Query != "How do I use the API?" {
			t.Errorf("query = %q, want How do I use the API?", req.Query)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "answer": {
    "answerText": "Use the client library.",
    "citations": [{
      "startIndex": 0,
      "endIndex": 22,
      "sources": [{"referenceIndex": 0}]
    }],
    "references": [{
      "documentReference": {
        "documentChunk": {
          "parent": "documents/developers.google.com/knowledge/api",
          "content": "Client library documentation",
          "document": {
            "name": "documents/developers.google.com/knowledge/api",
            "uri": "https://developers.google.com/knowledge/api",
            "title": "Developer Knowledge API"
          }
        }
      }
    }]
  }
}`)
	}))
	defer server.Close()

	client := &Client{
		BaseURL:    server.URL + "/v1",
		APIKey:     "test-key",
		HTTPClient: server.Client(),
	}
	resp, err := client.AnswerQuery(context.Background(), &AnswerQueryRequest{Query: "How do I use the API?"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answer == nil {
		t.Fatal("AnswerQuery() answer = nil")
	}
	if got := resp.Answer.AnswerText; got != "Use the client library." {
		t.Errorf("answer text = %q, want Use the client library.", got)
	}
	if got := len(resp.Answer.Citations); got != 1 {
		t.Fatalf("citations = %#v, want one citation", resp.Answer.Citations)
	}
	if got := len(resp.Answer.Citations[0].Sources); got != 1 {
		t.Fatalf("sources = %#v, want one source", resp.Answer.Citations[0].Sources)
	}
	if got := resp.Answer.Citations[0].EndIndex; got != 22 {
		t.Errorf("citation end index = %d, want 22", got)
	}
	if got := resp.Answer.Citations[0].Sources[0].ReferenceIndex; got != 0 {
		t.Errorf("reference index = %d, want 0", got)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"startIndex":0`, `"referenceIndex":0`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("re-encoded response = %s, want %s", encoded, want)
		}
	}
	if got := len(resp.Answer.References); got != 1 {
		t.Fatalf("references = %#v, want one reference", resp.Answer.References)
	}
	ref := resp.Answer.References[0].DocumentReference
	if ref == nil || ref.DocumentChunk == nil {
		t.Fatalf("reference = %#v, want document chunk", resp.Answer.References[0])
	}
	chunk := ref.DocumentChunk
	if chunk.ID != "" {
		t.Errorf("document chunk ID = %q, want empty", chunk.ID)
	}
	if chunk.Document == nil || chunk.Document.Title != "Developer Knowledge API" {
		t.Errorf("document = %#v, want Developer Knowledge API title", chunk.Document)
	}
}

func TestAnswerQueryRejectsInvalidRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	tests := []struct {
		name string
		req  *AnswerQueryRequest
		want string
	}{
		{name: "nil", req: nil, want: "request is required"},
		{name: "empty", req: &AnswerQueryRequest{}, want: "non-empty query"},
		{name: "whitespace", req: &AnswerQueryRequest{Query: " \t\n"}, want: "non-empty query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.AnswerQuery(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("AnswerQuery() error = %v, want containing %q", err, tt.want)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server requests = %d, want 0", got)
	}
}

func TestAnswerQueryReturnsAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"status":"INVALID_ARGUMENT","message":"invalid query"}}`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	_, err := client.AnswerQuery(context.Background(), &AnswerQueryRequest{Query: "question"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("AnswerQuery() error = %v, want *APIError", err)
	}
	if apiErr.Code != http.StatusBadRequest || apiErr.Status != "INVALID_ARGUMENT" {
		t.Fatalf("APIError = %#v, want 400 INVALID_ARGUMENT", apiErr)
	}
}

func TestAnswerQueryRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	_, err := client.AnswerQuery(context.Background(), &AnswerQueryRequest{Query: "question"})
	if err == nil || !strings.Contains(err.Error(), "decode answerQuery response") {
		t.Fatalf("AnswerQuery() error = %v, want decode error", err)
	}
}

func TestAnswerQueryRejectsNullResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `null`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	_, err := client.AnswerQuery(context.Background(), &AnswerQueryRequest{Query: "question"})
	if err == nil || !strings.Contains(err.Error(), "expected object, got null") {
		t.Fatalf("AnswerQuery() error = %v, want null response error", err)
	}
}

func TestAnswerQueryResponseOmitsMissingAnswer(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(AnswerQueryResponse{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != `{}` {
		t.Fatalf("json.Marshal(AnswerQueryResponse{}) = %s, want {}", got)
	}
}
