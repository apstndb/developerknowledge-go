package dkapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	dkapi "github.com/apstndb/developerknowledge-go"
)

func ExampleClient_BatchGetDocumentsPartial() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		names := r.URL.Query()["names"]
		for _, name := range names {
			if name == "documents/example.com/missing" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(
					`{"error":{"code":404,"status":"NOT_FOUND","message":"missing"}}`,
				))
				return
			}
		}

		docs := make([]dkapi.Document, 0, len(names))
		for _, name := range names {
			docs = append(docs, dkapi.Document{
				Name:               name,
				View:               string(dkapi.DocumentViewBasic),
				ContentLengthBytes: 42,
			})
		}
		_ = json.NewEncoder(w).Encode(dkapi.BatchGetResponse{Documents: docs})
	}))
	defer server.Close()

	client := &dkapi.Client{
		BaseURL:    server.URL + "/v1",
		HTTPClient: server.Client(),
	}
	results, err := client.BatchGetDocumentsPartial(
		context.Background(),
		[]string{
			"documents/example.com/guide",
			"documents/example.com/missing",
		},
		dkapi.WithDocumentView(dkapi.DocumentViewBasic),
	)
	if err != nil {
		panic(err)
	}
	for _, result := range results {
		switch {
		case result.Err != nil:
			fmt.Printf("%s: unavailable\n", result.Name)
		case result.Document == nil:
			fmt.Printf("%s: omitted\n", result.Name)
		default:
			fmt.Printf("%s: %d bytes\n", result.Name, result.Document.ContentLengthBytes)
		}
	}

	// Output:
	// documents/example.com/guide: 42 bytes
	// documents/example.com/missing: unavailable
}
