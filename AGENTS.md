# developerknowledge-go

Shared Go primitives for the Google Developer Knowledge API.

## Purpose

This module is used by:

- `dkcli` — CLI for Developer Knowledge API access
- `gcp-docs-mirror-tools` — batch document mirroring with bisection
- `spanner-mycli` — LLM tool integration for documentation lookup

It complements the official generated client at
`google.golang.org/api/developerknowledge/v1` (see open issues for the long-term
relationship). Value-add today: API-key/ADC selection, `CLOUDSDK_CONFIG`-aware
ADC, quota-project handling, 429 retry with `Retry-After`, batch bisection
classification, and `NormalizeDocName`.

## Versioning

v0 policy: breaking API changes bump the minor version. Release notes on GitHub
tags serve as the changelog.

## Verification

```sh
go test -race ./...
go vet ./...
golangci-lint run
govulncheck ./...
```

## Conventions

- Request methods take `context.Context` as the first parameter.
- `documents:batchGet` accepts at most `MaxBatchGetDocuments` (20) names per call.
- Package name is `dkapi`; import as
  `import dkapi "github.com/apstndb/developerknowledge-go"`.
