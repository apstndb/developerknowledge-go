// Package dkapi provides shared primitives for Google Developer Knowledge API clients.
//
// This module complements (rather than replaces) the official generated client at
// google.golang.org/api/developerknowledge/v1. It focuses on auth-mode selection,
// quota-project handling for local ADC, rate-limit retry, batch bisection helpers,
// and document-name normalization used by dkcli, gcp-docs-mirror-tools, and
// spanner-mycli.
//
// Authentication supports API keys (DEVELOPERKNOWLEDGE_API_KEY or GOOGLE_API_KEY)
// and Application Default Credentials. When CLOUDSDK_CONFIG is set, both token
// and quota-project metadata are read from that directory's ADC file.
package dkapi
