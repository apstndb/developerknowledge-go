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

// escapeDocumentResourceName encodes a resource name for a multi-segment
// URL path. Slashes remain separators. Bytes outside [-_.~/0-9a-zA-Z] are
// percent-encoded. An already-encoded slash (%2F or %2f) is copied through
// unchanged: Google HttpRule decoding of multi-segment path variables does
// not turn %2F into a separator.
func escapeDocumentResourceName(name string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isDocumentPathUnescaped(c) {
			b.WriteByte(c)
			continue
		}
		if c == '%' && i+2 < len(name) && name[i+1] == '2' && (name[i+2] == 'F' || name[i+2] == 'f') {
			b.WriteString(name[i : i+3])
			i += 2
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

func isDocumentPathUnescaped(c byte) bool {
	return c == '/' || c == '-' || c == '_' || c == '.' || c == '~' ||
		(c >= '0' && c <= '9') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z')
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

	// Escape the resource name before concatenating it. A raw append lets the
	// HTTP client treat %25 in the logical name as a transport escape, so
	// GetDocument and batchGet request different documents. Multi-segment
	// HttpRule variables keep %2F encoded; encoding that percent sign would
	// not match server decoding. url.URL.Path assignment is unsuitable
	// because URL.String re-encodes Path and drops a RawPath that contains
	// %2F when it no longer round-trips through Path.
	reqURL := c.baseURL() + "/" + escapeDocumentResourceName(name)
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
