package dkapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// DocumentView selects how much of each Document the API returns.
type DocumentView string

const (
	DocumentViewUnspecified DocumentView = "DOCUMENT_VIEW_UNSPECIFIED"
	DocumentViewBasic       DocumentView = "DOCUMENT_VIEW_BASIC"
	DocumentViewFull        DocumentView = "DOCUMENT_VIEW_FULL"
	DocumentViewContent     DocumentView = "DOCUMENT_VIEW_CONTENT"
)

type batchGetConfig struct {
	view DocumentView
}

// BatchGetOption configures BatchGetDocumentsPartial.
type BatchGetOption func(*batchGetConfig)

// WithDocumentView sets the document view for every batchGet request. Without
// this option, the server default is DOCUMENT_VIEW_CONTENT.
func WithDocumentView(view DocumentView) BatchGetOption {
	return func(cfg *batchGetConfig) {
		cfg.view = view
	}
}

func (v DocumentView) valid() bool {
	switch v {
	case "", DocumentViewUnspecified, DocumentViewBasic, DocumentViewFull, DocumentViewContent:
		return true
	default:
		return false
	}
}

// BatchGetDocumentResult pairs one input occurrence with its outcome. Name is
// always populated. Document is set for a returned document, and Err is set for
// a document-specific failure. Both are nil when a fatal error stopped
// processing or the API omitted the document from a successful response.
type BatchGetDocumentResult struct {
	Name     string
	Document *Document
	Err      error
}

// BatchGetDocumentsPartial fetches names in chunks of MaxBatchGetDocuments and
// bisects document-specific failures. Results preserve input order and
// duplicates: len(results) equals len(names), and results[i].Name is names[i].
//
// Document-specific errors are stored in the corresponding result and do not
// make the method return an error. A non-bisectable error stops processing and
// is returned with all results completed before the failure. If a successful
// response contains fewer documents than requested, unmatched results remain
// nil; response documents that match no remaining input occurrence are ignored.
func (c *Client) BatchGetDocumentsPartial(
	ctx context.Context,
	names []string,
	opts ...BatchGetOption,
) ([]BatchGetDocumentResult, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("batchGet requires at least one document name")
	}

	cfg := batchGetConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	results := make([]BatchGetDocumentResult, len(names))
	for i, name := range names {
		results[i].Name = name
	}
	if !cfg.view.valid() {
		return results, fmt.Errorf("invalid document view %q", cfg.view)
	}

	for start := 0; start < len(names); start += MaxBatchGetDocuments {
		end := min(start+MaxBatchGetDocuments, len(names))
		if err := c.fetchBatchGetRange(ctx, names, results, start, end, cfg); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (c *Client) fetchBatchGetRange(
	ctx context.Context,
	names []string,
	results []BatchGetDocumentResult,
	start int,
	end int,
	cfg batchGetConfig,
) error {
	docs, err := c.batchGetDocuments(ctx, names[start:end], cfg)
	if err == nil {
		assignBatchGetDocuments(results, start, end, docs)
		return nil
	}
	if !IsBisectableDocumentError(err) {
		return err
	}
	if end-start == 1 {
		results[start].Err = fmt.Errorf("%s: %w", names[start], err)
		return nil
	}

	mid := start + (end-start)/2
	if err := c.fetchBatchGetRange(ctx, names, results, start, mid, cfg); err != nil {
		return err
	}
	return c.fetchBatchGetRange(ctx, names, results, mid, end, cfg)
}

func assignBatchGetDocuments(
	results []BatchGetDocumentResult,
	start int,
	end int,
	docs []Document,
) {
	if len(docs) == end-start {
		for i := range docs {
			doc := docs[i]
			results[start+i].Document = &doc
		}
		return
	}

	pending := make(map[string][]int, end-start)
	for i := start; i < end; i++ {
		pending[results[i].Name] = append(pending[results[i].Name], i)
	}

	for i := range docs {
		indexes := pending[docs[i].Name]
		if len(indexes) == 0 {
			continue
		}
		resultIndex := indexes[0]
		pending[docs[i].Name] = indexes[1:]
		doc := docs[i]
		results[resultIndex].Document = &doc
	}
}

func (c *Client) batchGetDocuments(
	ctx context.Context,
	names []string,
	cfg batchGetConfig,
) ([]Document, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("batchGet requires at least one document name")
	}
	if len(names) > MaxBatchGetDocuments {
		return nil, fmt.Errorf(
			"batchGet accepts at most %d document names, got %d",
			MaxBatchGetDocuments,
			len(names),
		)
	}

	params := url.Values{}
	for _, name := range names {
		params.Add("names", name)
	}
	if cfg.view != "" && cfg.view != DocumentViewUnspecified {
		params.Set("view", string(cfg.view))
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
