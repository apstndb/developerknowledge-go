package dkapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	dkapi "github.com/apstndb/developerknowledge-go"
)

// WithDocumentView must stay usable where the v0.3.1 option type is expected.
var _ dkapi.BatchGetOption = dkapi.WithDocumentView(dkapi.DocumentViewBasic)

// BatchGetDocumentsPartial must retain its v0.3.1 source-level method shape.
var _ func(context.Context, []string, ...dkapi.BatchGetOption) ([]dkapi.BatchGetDocumentResult, error) = (&dkapi.Client{}).BatchGetDocumentsPartial

func ExampleClient_GetDocument_metadataOnly() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The BASIC view answers without the content field.
		_ = json.NewEncoder(w).Encode(dkapi.Document{
			Name:               "documents/example.com/guide",
			URI:                "https://example.com/guide",
			View:               r.URL.Query().Get("view"),
			UpdateTime:         "2026-07-18T00:00:00Z",
			ContentLengthBytes: 31940,
		})
	}))
	defer server.Close()

	client := &dkapi.Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	doc, err := client.GetDocument(
		context.Background(),
		dkapi.NormalizeDocName("https://example.com/guide"),
		dkapi.WithDocumentView(dkapi.DocumentViewBasic),
	)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s view=%s bytes=%d content=%q\n",
		doc.Name, doc.View, doc.ContentLengthBytes, doc.Content)

	// Output:
	// documents/example.com/guide view=DOCUMENT_VIEW_BASIC bytes=31940 content=""
}
