package dkapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AnswerQueryRequest is the request body for AnswerQuery.
type AnswerQueryRequest struct {
	// Query is the question to answer.
	Query string `json:"query" yaml:"query"`
}

// AnswerQueryResponse is the response from AnswerQuery.
type AnswerQueryResponse struct {
	// Answer is nil if the service omits the answer object.
	Answer *Answer `json:"answer,omitempty" yaml:"answer,omitempty"`
}

// Answer is a generated answer and its supporting citations and references.
type Answer struct {
	AnswerText string            `json:"answerText" yaml:"answer_text"`
	Citations  []AnswerCitation  `json:"citations,omitempty" yaml:"citations,omitempty"`
	References []AnswerReference `json:"references,omitempty" yaml:"references,omitempty"`
}

// AnswerCitation describes a segment of Answer.AnswerText and its sources.
type AnswerCitation struct {
	// StartIndex is the inclusive UTF-8 byte offset of the cited segment.
	StartIndex int64 `json:"startIndex" yaml:"start_index"`
	// EndIndex is the exclusive UTF-8 byte offset of the cited segment.
	EndIndex int64 `json:"endIndex" yaml:"end_index"`
	// Sources identify entries in Answer.References.
	Sources []CitationSource `json:"sources,omitempty" yaml:"sources,omitempty"`
}

// CitationSource identifies a supporting entry in Answer.References.
type CitationSource struct {
	ReferenceIndex int64 `json:"referenceIndex" yaml:"reference_index"`
}

// AnswerReference represents a source used to generate an answer.
type AnswerReference struct {
	DocumentReference *DocumentReference `json:"documentReference,omitempty" yaml:"document_reference,omitempty"`
}

// DocumentReference represents a document source.
type DocumentReference struct {
	// DocumentChunk contains the source chunk. The API leaves its ID field empty.
	DocumentChunk *DocumentChunk `json:"documentChunk,omitempty" yaml:"document_chunk,omitempty"`
}

// AnswerQuery answers a natural-language query using Developer Knowledge
// content. As of google.golang.org/api v0.289.0, the official generated v1 Go
// client does not expose this GA operation. See [issue #1] for the long-term
// relationship with that client.
//
// [issue #1]: https://github.com/apstndb/developerknowledge-go/issues/1
func (c *Client) AnswerQuery(ctx context.Context, req *AnswerQueryRequest) (*AnswerQueryResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("answerQuery request is required")
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("answerQuery requires a non-empty query")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode answerQuery request: %w", err)
	}
	body, err = c.DoJSONPost(ctx, c.baseURL()+":answerQuery", body)
	if err != nil {
		return nil, err
	}

	var resp *AnswerQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode answerQuery response: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("decode answerQuery response: expected object, got null")
	}
	return resp, nil
}
