package dkapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	dkapi "github.com/apstndb/developerknowledge-go"
)

func ExampleClient_AnswerQuery() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"answer":{"answerText":"Use documents:batchGet."}}`)
	}))
	defer server.Close()

	client := &dkapi.Client{BaseURL: server.URL + "/v1", HTTPClient: server.Client()}
	resp, err := client.AnswerQuery(context.Background(), &dkapi.AnswerQueryRequest{
		Query: "How can I fetch multiple documents?",
	})
	if err != nil {
		panic(err)
	}
	if resp.Answer == nil {
		fmt.Println("No answer returned.")
		return
	}
	fmt.Println(resp.Answer.AnswerText)
	// Output: Use documents:batchGet.
}
