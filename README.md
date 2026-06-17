# developerknowledge-go

Go helpers for the Google Developer Knowledge API.

This module is intentionally small. It provides shared primitives used by
Developer Knowledge API clients:

- API key and ADC authentication helpers
- quota project handling for local ADC
- Google API error parsing
- rate limit error handling and `Retry-After` parsing
- context-aware HTTP request helpers
- `documents:batchGet` support
- shared `Document` and `DocumentChunk` response types
- conservative batch bisection error classification

## Install

```sh
go get github.com/apstndb/developerknowledge-go@v0.1.0
```

## Example

```go
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
    Context:    ctx,
}

docs, err := dkClient.BatchGetDocuments([]string{
    "documents/developers.google.com/knowledge/api",
})
```
