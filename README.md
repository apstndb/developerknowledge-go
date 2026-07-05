# developerknowledge-go

Go helpers for the Google Developer Knowledge API.

This module provides shared primitives used by Developer Knowledge API clients:

- API key and ADC authentication helpers
- quota project handling for local ADC (including `CLOUDSDK_CONFIG`)
- Google API error parsing with bounded error-body reads
- rate limit error handling and `Retry-After` parsing
- context-aware HTTP request helpers
- `documents:batchGet` support with chunking via `BatchGetDocumentsAll`
- shared `Document` and `DocumentChunk` response types
- conservative batch bisection error classification

See [pkg.go.dev](https://pkg.go.dev/github.com/apstndb/developerknowledge-go) for
API documentation. This module complements the official generated client at
`google.golang.org/api/developerknowledge/v1`; see repository issues for the
long-term direction.

## Install

```sh
go get github.com/apstndb/developerknowledge-go
```

## Example

```go
import (
    "context"

    dkapi "github.com/apstndb/developerknowledge-go"
)

ctx := context.Background()
client, apiKey, err := dkapi.NewAuthenticatedHTTPClient(ctx, dkapi.AuthConfig{
    Mode:    dkapi.AuthPreferAPIKey,
    Timeout: dkapi.DefaultHTTPTimeout,
})
if err != nil {
    return err
}

dkClient := &dkapi.Client{
    BaseURL:    dkapi.DefaultV1BaseURL,
    APIKey:     apiKey,
    HTTPClient: client,
}

docs, err := dkClient.BatchGetDocuments(ctx, []string{
    "documents/developers.google.com/knowledge/api",
})
```
