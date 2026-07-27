// Package dkapi provides shared primitives for Google Developer Knowledge API clients.
//
// This module complements (rather than replaces) the official generated client at
// google.golang.org/api/developerknowledge/v1. It focuses on auth-mode selection,
// quota-project handling for local ADC, rate-limit retry, AnswerQuery,
// document retrieval with view selection, batch bisection helpers, and
// document-name normalization used by dkcli, gcp-docs-mirror-tools, and
// spanner-mycli.
//
// Authentication supports API keys (DEVELOPERKNOWLEDGE_API_KEY or GOOGLE_API_KEY)
// and Application Default Credentials. When CLOUDSDK_CONFIG is set, its ADC
// file provides both token and quota-project metadata when present. If that
// optional file is absent, standard ADC discovery continues; other path or
// read errors are returned.
//
// # Partial batch retrieval
//
// BatchGetDocumentsPartial chunks large input lists and bisects
// document-specific failures while preserving input order and duplicate names.
// WithDocumentView can request metadata-only results. A fatal batch-level error
// stops later requests but returns the positional results completed before it.
//
// # Document views
//
// GetDocument retrieves one document, and WithDocumentView selects how much of
// it the API returns. DOCUMENT_VIEW_BASIC omits Content while still reporting
// ContentLengthBytes and UpdateTime, so size and freshness checks do not have to
// download full content. The same option applies to BatchGetDocumentsPartial.
package dkapi
