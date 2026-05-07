package openai

import (
	"context"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestResponsesConverterMapsStandardRequestToProviderRequest(t *testing.T) {
	converter := ResponsesConverter{ProviderName: "derived"}
	preq, err := converter.ConvertRequest(context.Background(), llm.Request{
		Model:              "gpt-5-mini",
		Messages:           []llm.Message{llm.UserText("hi")},
		PreviousResponseID: "resp_prev",
		Metadata:           map[string]string{"request_id": "req_1"},
		ParallelToolCalls:  llm.Bool(true),
	}, llm.ConvertOptions{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if preq.Operation != responsesOperation || preq.Model != "gpt-5-mini" || !preq.Stream || preq.Metadata["request_id"] != "req_1" {
		t.Fatalf("provider request metadata가 이상해요: %+v", preq)
	}
	preq.Metadata["request_id"] = "mutated"
	if preq.Body.(map[string]any)["metadata"].(map[string]string)["request_id"] != "req_1" {
		t.Fatalf("ProviderRequest.Metadata는 body metadata와 독립된 복사본이어야 해요: %+v", preq.Metadata)
	}
	body := preq.Body.(map[string]any)
	if body["previous_response_id"] != "resp_prev" || body["metadata"].(map[string]string)["request_id"] != "req_1" || body["parallel_tool_calls"] != true {
		t.Fatalf("표준 request 필드가 payload로 변환돼야 해요: %#v", body)
	}
}

func TestResponsesConverterMapsProviderResultToStandardResponse(t *testing.T) {
	converter := ResponsesConverter{ProviderName: "derived"}
	resp, err := converter.ConvertResponse(context.Background(), llm.ProviderResult{Model: "gpt-5-mini", Body: []byte(`{"id":"resp_1","model":"gpt-5-mini","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "derived" || resp.Model != "gpt-5-mini" || resp.Text != "ok" {
		t.Fatalf("표준 response 변환이 이상해요: %+v", resp)
	}
}
