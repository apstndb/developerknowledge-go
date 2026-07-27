package dkapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// DocumentView selects how much of each Document the API returns.
type DocumentView string

const (
	DocumentViewUnspecified DocumentView = "DOCUMENT_VIEW_UNSPECIFIED"
	DocumentViewBasic       DocumentView = "DOCUMENT_VIEW_BASIC"
	DocumentViewFull        DocumentView = "DOCUMENT_VIEW_FULL"
	DocumentViewContent     DocumentView = "DOCUMENT_VIEW_CONTENT"
)

func (v DocumentView) valid() bool {
	switch v {
	case "", DocumentViewUnspecified, DocumentViewBasic, DocumentViewFull, DocumentViewContent:
		return true
	default:
		return false
	}
}

type documentRequestConfig struct {
	view DocumentView
}

// DocumentOption configures a document retrieval request. The same options
// apply to GetDocument and BatchGetDocumentsPartial.
type DocumentOption func(*documentRequestConfig)

// BatchGetOption is an alias of DocumentOption. It is retained because
// BatchGetDocumentsPartial introduced the option type under this name.
type BatchGetOption = DocumentOption

// WithDocumentView sets the document view for every request the call makes.
// Without this option, the server default applies: DOCUMENT_VIEW_CONTENT for
// GetDocument and batchGet.
func WithDocumentView(view DocumentView) DocumentOption {
	return func(cfg *documentRequestConfig) {
		cfg.view = view
	}
}

func applyDocumentOptions(opts []DocumentOption) documentRequestConfig {
	cfg := documentRequestConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

func (cfg documentRequestConfig) validate() error {
	if !cfg.view.valid() {
		return fmt.Errorf("invalid document view %q", cfg.view)
	}
	return nil
}

// setQueryParams adds the configured request parameters to params. An unset or
// unspecified view is omitted so the server default applies.
func (cfg documentRequestConfig) setQueryParams(params url.Values) {
	if cfg.view != "" && cfg.view != DocumentViewUnspecified {
		params.Set("view", string(cfg.view))
	}
}

// GetDocument retrieves a single document by resource name, for example
// "documents/docs.cloud.google.com/storage/docs/creating-buckets". Use
// NormalizeDocName to convert a pasted URL into that form; a name with a
// leading slash, a query string, or a fragment is rejected locally.
//
// WithDocumentView requests a metadata-only document. DOCUMENT_VIEW_BASIC omits
// Content while still reporting ContentLengthBytes, which makes size and
// freshness checks much cheaper than fetching full content.
func (c *Client) GetDocument(ctx context.Context, name string, opts ...DocumentOption) (*Document, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return nil, fmt.Errorf("get document requires a document name")
	}
	if name != trimmedName {
		return nil, fmt.Errorf("document name must not contain leading or trailing whitespace: %q", name)
	}
	if strings.HasPrefix(name, "/") || strings.ContainsAny(name, "?#") {
		return nil, fmt.Errorf("document name must be a resource name without a leading slash, query, or fragment: %q", name)
	}

	cfg := applyDocumentOptions(opts)
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	reqURL := c.baseURL() + "/" + name
	params := url.Values{}
	cfg.setQueryParams(params)
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	body, err := c.DoGet(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var doc *Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode document response: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("decode document response: expected object, got null")
	}
	return doc, nil
}
