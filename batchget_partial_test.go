package dkapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBatchGetDocumentsPartialPreservesOrderAndDuplicateErrors(t *testing.T) {
	t.Parallel()

	const badName = "documents/example.com/bad"
	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.URL.Query().Get("view"); got != string(DocumentViewBasic) {
					t.Fatalf("view = %q, want %q", got, DocumentViewBasic)
				}
				names := req.URL.Query()["names"]
				for _, name := range names {
					if name == badName {
						return jsonHTTPResponse(
							http.StatusNotFound,
							`{"error":{"code":404,"status":"NOT_FOUND","message":"missing"}}`,
						), nil
					}
				}

				docs := make([]Document, 0, len(names))
				for _, name := range names {
					docs = append(docs, Document{Name: name})
				}
				body, err := json.Marshal(BatchGetResponse{Documents: docs})
				if err != nil {
					t.Fatal(err)
				}
				return jsonHTTPResponse(http.StatusOK, string(body)), nil
			}),
		},
	}

	names := []string{
		"documents/example.com/a",
		badName,
		"documents/example.com/b",
		badName,
	}
	results, err := client.BatchGetDocumentsPartial(
		context.Background(),
		names,
		WithDocumentView(DocumentViewBasic),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(names) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(names))
	}

	for i, result := range results {
		if result.Name != names[i] {
			t.Errorf("results[%d].Name = %q, want %q", i, result.Name, names[i])
		}
	}
	for _, i := range []int{0, 2} {
		if results[i].Document == nil || results[i].Document.Name != names[i] {
			t.Errorf("results[%d].Document = %+v, want %q", i, results[i].Document, names[i])
		}
		if results[i].Err != nil {
			t.Errorf("results[%d].Err = %v, want nil", i, results[i].Err)
		}
	}
	for _, i := range []int{1, 3} {
		if results[i].Document != nil {
			t.Errorf("results[%d].Document = %+v, want nil", i, results[i].Document)
		}
		var apiErr *APIError
		if !errors.As(results[i].Err, &apiErr) || apiErr.Code != http.StatusNotFound {
			t.Errorf("results[%d].Err = %v, want wrapped 404 APIError", i, results[i].Err)
		}
	}
}

func TestBatchGetDocumentsPartialReturnsCompletedResultsWithFatalError(t *testing.T) {
	t.Parallel()

	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				names := req.URL.Query()["names"]
				if len(names) == 1 && names[0] == "documents/example.com/20" {
					return jsonHTTPResponse(
						http.StatusServiceUnavailable,
						`{"error":{"code":503,"status":"UNAVAILABLE","message":"retry later"}}`,
					), nil
				}

				docs := make([]Document, 0, len(names))
				for _, name := range names {
					docs = append(docs, Document{Name: name})
				}
				body, err := json.Marshal(BatchGetResponse{Documents: docs})
				if err != nil {
					t.Fatal(err)
				}
				return jsonHTTPResponse(http.StatusOK, string(body)), nil
			}),
		},
	}

	names := make([]string, MaxBatchGetDocuments+1)
	for i := range names {
		names[i] = fmt.Sprintf("documents/example.com/%d", i)
	}
	results, err := client.BatchGetDocumentsPartial(context.Background(), names)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want 503 APIError", err)
	}
	if len(results) != len(names) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(names))
	}
	for i := 0; i < MaxBatchGetDocuments; i++ {
		if results[i].Document == nil || results[i].Document.Name != names[i] {
			t.Errorf("results[%d].Document = %+v, want %q", i, results[i].Document, names[i])
		}
	}
	last := results[MaxBatchGetDocuments]
	if last.Name != names[MaxBatchGetDocuments] || last.Document != nil || last.Err != nil {
		t.Errorf("last result = %+v, want named but unprocessed result", last)
	}
}

func TestBatchGetDocumentsPartialMatchesReturnedDocumentsByName(t *testing.T) {
	t.Parallel()

	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return jsonHTTPResponse(
					http.StatusOK,
					`{"documents":[{"name":"documents/example.com/b"}]}`,
				), nil
			}),
		},
	}

	results, err := client.BatchGetDocumentsPartial(context.Background(), []string{
		"documents/example.com/a",
		"documents/example.com/b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Document != nil || results[0].Err != nil {
		t.Errorf("results[0] = %+v, want omitted result", results[0])
	}
	if results[1].Document == nil || results[1].Document.Name != "documents/example.com/b" {
		t.Errorf("results[1] = %+v, want document b", results[1])
	}
}

func TestAssignBatchGetDocumentsUsesPositionForCompleteResponse(t *testing.T) {
	t.Parallel()

	results := []BatchGetDocumentResult{
		{Name: "documents/EXAMPLE.com/a"},
		{Name: "documents/example.com/a"},
	}
	docs := []Document{
		{Name: "documents/example.com/a", Title: "first"},
		{Name: "documents/example.com/a", Title: "second"},
	}

	assignBatchGetDocuments(results, 0, len(results), docs)
	if results[0].Document == nil || results[0].Document.Title != "first" {
		t.Fatalf("results[0] = %+v, want first positional document", results[0])
	}
	if results[1].Document == nil || results[1].Document.Title != "second" {
		t.Fatalf("results[1] = %+v, want second positional document", results[1])
	}
	results[0].Document.Title = "changed"
	if results[1].Document.Title != "second" {
		t.Fatalf("duplicate occurrences share a Document pointer: %+v", results)
	}
}

func TestAssignBatchGetDocumentsMatchesShortDuplicateResponse(t *testing.T) {
	t.Parallel()

	results := []BatchGetDocumentResult{
		{Name: "documents/example.com/a"},
		{Name: "documents/example.com/a"},
		{Name: "documents/example.com/b"},
	}
	docs := []Document{
		{Name: "documents/example.com/a"},
		{Name: "documents/example.com/b"},
	}

	assignBatchGetDocuments(results, 0, len(results), docs)
	if results[0].Document == nil || results[0].Document.Name != results[0].Name {
		t.Errorf("results[0] = %+v, want first a occurrence", results[0])
	}
	if results[1].Document != nil || results[1].Err != nil {
		t.Errorf("results[1] = %+v, want omitted second a occurrence", results[1])
	}
	if results[2].Document == nil || results[2].Document.Name != results[2].Name {
		t.Errorf("results[2] = %+v, want b occurrence", results[2])
	}
}

func TestBatchGetDocumentsPartialOmitsUnspecifiedView(t *testing.T) {
	t.Parallel()

	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if _, ok := req.URL.Query()["view"]; ok {
					t.Fatalf("unexpected view query parameter: %q", req.URL.RawQuery)
				}
				return jsonHTTPResponse(
					http.StatusOK,
					`{"documents":[{"name":"documents/example.com/a"}]}`,
				), nil
			}),
		},
	}

	results, err := client.BatchGetDocumentsPartial(
		context.Background(),
		[]string{"documents/example.com/a"},
		WithDocumentView(DocumentViewUnspecified),
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Document == nil {
		t.Fatalf("results[0] = %+v, want document", results[0])
	}
}

func TestBatchGetDocumentsPartialRejectsUnknownView(t *testing.T) {
	t.Parallel()

	client := &Client{
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("HTTP request made for invalid view")
				return nil, nil
			}),
		},
	}
	results, err := client.BatchGetDocumentsPartial(
		context.Background(),
		[]string{"documents/example.com/a"},
		WithDocumentView(DocumentView("DOCUMENT_VIEW_FUTURE")),
	)
	if err == nil || !strings.Contains(err.Error(), "invalid document view") {
		t.Fatalf("error = %v, want invalid document view", err)
	}
	if len(results) != 1 || results[0].Name != "documents/example.com/a" {
		t.Fatalf("results = %+v, want named unprocessed result", results)
	}
}

func TestBatchGetDocumentsPartialStopsSiblingAfterRecursiveFatalError(t *testing.T) {
	t.Parallel()

	client := &Client{
		BaseURL: "https://example.test/v1",
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				names := req.URL.Query()["names"]
				if len(names) > 1 {
					return jsonHTTPResponse(
						http.StatusNotFound,
						`{"error":{"code":404,"status":"NOT_FOUND","message":"bisect"}}`,
					), nil
				}
				switch names[0] {
				case "documents/example.com/good":
					return jsonHTTPResponse(
						http.StatusOK,
						`{"documents":[{"name":"documents/example.com/good"}]}`,
					), nil
				case "documents/example.com/fatal":
					return jsonHTTPResponse(
						http.StatusServiceUnavailable,
						`{"error":{"code":503,"status":"UNAVAILABLE","message":"fatal"}}`,
					), nil
				default:
					t.Fatalf("unexpected sibling request after fatal error: %v", names)
					return nil, nil
				}
			}),
		},
	}

	results, err := client.BatchGetDocumentsPartial(context.Background(), []string{
		"documents/example.com/good",
		"documents/example.com/fatal",
		"documents/example.com/sibling",
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want 503 APIError", err)
	}
	if results[0].Document == nil {
		t.Errorf("results[0] = %+v, want completed document", results[0])
	}
	for _, i := range []int{1, 2} {
		if results[i].Document != nil || results[i].Err != nil {
			t.Errorf("results[%d] = %+v, want unprocessed result", i, results[i])
		}
	}
}

func TestBatchGetDocumentsPartialRejectsEmptyNames(t *testing.T) {
	t.Parallel()

	results, err := (&Client{}).BatchGetDocumentsPartial(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if results != nil {
		t.Fatalf("results = %+v, want nil", results)
	}
}

func jsonHTTPResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
