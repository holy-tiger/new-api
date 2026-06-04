package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
)

func TestResponsesResponseToChatCompletionsResponse_MapsIncompleteToLength(t *testing.T) {
	resp := &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		Object:    "response",
		CreatedAt: 123,
		Model:     "gpt-4o",
		Status:    json.RawMessage(`"incomplete"`),
		IncompleteDetails: &dto.IncompleteDetails{
			Reasoning: "max_output_tokens",
		},
		Output: []dto.ResponsesOutput{
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "partial"},
				},
			},
		},
		Usage: &dto.Usage{
			InputTokens:  10,
			OutputTokens: 2,
			TotalTokens:  12,
		},
	}

	chatResp, usage, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage == nil {
		t.Fatal("expected usage")
	}
	if len(chatResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(chatResp.Choices))
	}
	if chatResp.Choices[0].FinishReason != "length" {
		t.Fatalf("expected finish_reason=length, got %q", chatResp.Choices[0].FinishReason)
	}
}

func TestResponsesResponseToChatCompletionsResponse_AcceptsAllToolCallTypes(t *testing.T) {
	resp := &dto.OpenAIResponsesResponse{
		ID:        "resp_2",
		Object:    "response",
		CreatedAt: 456,
		Model:     "gpt-4o",
		Status:    json.RawMessage(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type:      "custom_tool_call",
				ID:        "item_custom",
				CallId:    "call_custom",
				Name:      "my_custom_tool",
				Arguments: json.RawMessage(`{"x":1}`),
				Status:    "completed",
			},
			{
				Type:      "tool_search_call",
				ID:        "item_search",
				CallId:    "call_search",
				Name:      "tool_search",
				Arguments: json.RawMessage(`{"query":"golang"}`),
				Status:    "completed",
			},
		},
	}

	chatResp, _, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(chatResp.Choices))
	}
	if chatResp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, got %q", chatResp.Choices[0].FinishReason)
	}

	toolCalls := chatResp.Choices[0].Message.ParseToolCalls()
	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}
	if toolCalls[0].ID != "call_custom" || toolCalls[0].Function.Name != "my_custom_tool" {
		t.Fatalf("unexpected first tool call: %#v", toolCalls[0])
	}
	if toolCalls[1].ID != "call_search" || toolCalls[1].Function.Name != "tool_search" {
		t.Fatalf("unexpected second tool call: %#v", toolCalls[1])
	}
}

func TestResponsesResponseToChatCompletionsResponse_PreservesTextAndToolCallsTogether(t *testing.T) {
	resp := &dto.OpenAIResponsesResponse{
		ID:        "resp_3",
		Object:    "response",
		CreatedAt: 789,
		Model:     "gpt-4o",
		Status:    json.RawMessage(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "I will call a tool now."},
				},
			},
			{
				Type:      "custom_tool_call",
				ID:        "item_custom",
				CallId:    "call_custom",
				Name:      "my_custom_tool",
				Arguments: json.RawMessage(`{"x":1}`),
				Status:    "completed",
			},
		},
	}

	chatResp, _, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(chatResp.Choices))
	}

	msg := chatResp.Choices[0].Message
	if msg.StringContent() != "I will call a tool now." {
		t.Fatalf("expected content preserved, got %#v", msg.Content)
	}

	toolCalls := msg.ParseToolCalls()
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Function.Name != "my_custom_tool" {
		t.Fatalf("unexpected tool call: %#v", toolCalls[0])
	}
	if chatResp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, got %q", chatResp.Choices[0].FinishReason)
	}
}
