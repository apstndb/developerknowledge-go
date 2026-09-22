package dkapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

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
	opts ...DocumentOption,
) ([]BatchGetDocumentResult, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("batchGet requires at least one document name")
	}

	cfg := applyDocumentOptions(opts)

	results := make([]BatchGetDocumentResult, len(names))
	for i, name := range names {
		results[i].Name = name
	}
	// Validate after populating names so callers can still correlate every
	// unprocessed result with its input when an option is invalid.
	if err := cfg.validate(); err != nil {
		return results, err
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
	cfg documentRequestConfig,
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
	cfg documentRequestConfig,
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
	cfg.setQueryParams(params)

	body, err := c.DoGet(ctx, c.baseURL()+"/documents:batchGet?"+params.Encode())
	if err != nil {
		return nil, err
	}

	var resp *BatchGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode batchGet response: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("decode batchGet response: expected object, got null")
	}
	return resp.Documents, nil
}
