package codexchat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// =============================================================================
// Helpers
// =============================================================================

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := common.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	return data
}

// =============================================================================
// 1. String input → user message
// =============================================================================

func TestStringInputToUserMessage(t *testing.T) {
	inputStr := "Hello, how are you?"
	inputRaw := mustMarshal(t, inputStr)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", chatReq.Model)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", chatReq.Messages[0].Role)
	}
	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", chatReq.Messages[0].Content)
	}
	if content != inputStr {
		t.Errorf("expected content %q, got %q", inputStr, content)
	}
}

// =============================================================================
// 2. Instructions → system message
// =============================================================================

func TestInstructionsToSystemMessage(t *testing.T) {
	instructions := "You are a helpful assistant."
	instructionsRaw := mustMarshal(t, instructions)

	inputRaw := mustMarshal(t, "Hello")

	req := &dto.OpenAIResponsesRequest{
		Model:        "gpt-4o",
		Input:        inputRaw,
		Instructions: instructionsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + user), got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "system" {
		t.Errorf("expected first message role 'system', got %q", chatReq.Messages[0].Role)
	}
	sysContent, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content for system message, got %T", chatReq.Messages[0].Content)
	}
	if sysContent != instructions {
		t.Errorf("expected system content %q, got %q", instructions, sysContent)
	}
	if chatReq.Messages[1].Role != "user" {
		t.Errorf("expected second message role 'user', got %q", chatReq.Messages[1].Role)
	}
}

func TestSystemMessagesCollapseToHeadAndRolesNormalize(t *testing.T) {
	instructionsRaw := mustMarshal(t, "You are Codex.")
	inputItems := []map[string]any{
		{
			"type":    "message",
			"role":    "developer",
			"content": "Follow project instructions.",
		},
		{
			"type":    "message",
			"role":    "user",
			"content": "Inspect the repo.",
		},
		{
			"type":    "message",
			"role":    "system",
			"content": "Collaboration Mode: Default",
		},
		{
			"type":    "message",
			"role":    "latest_reminder",
			"content": "Newest user request wins.",
		},
	}

	req := &dto.OpenAIResponsesRequest{
		Model:        "gpt-4o",
		Input:        mustMarshal(t, inputItems),
		Instructions: instructionsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 3 {
		t.Fatalf("expected 3 messages after collapsing system messages, got %d: %#v", len(chatReq.Messages), chatReq.Messages)
	}
	if chatReq.Messages[0].Role != "system" {
		t.Fatalf("expected first message to be system, got %q", chatReq.Messages[0].Role)
	}
	systemContent, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected system content to be string, got %T", chatReq.Messages[0].Content)
	}
	for _, expected := range []string{"You are Codex.", "Follow project instructions.", "Collaboration Mode: Default"} {
		if !strings.Contains(systemContent, expected) {
			t.Fatalf("expected collapsed system content to contain %q, got %q", expected, systemContent)
		}
	}
	for i, msg := range chatReq.Messages[1:] {
		if msg.Role == "system" {
			t.Fatalf("expected no system message after index 0, got system at index %d", i+1)
		}
	}
	if chatReq.Messages[1].Role != "user" || chatReq.Messages[1].Content != "Inspect the repo." {
		t.Fatalf("expected original user message at index 1, got %#v", chatReq.Messages[1])
	}
	if chatReq.Messages[2].Role != "user" || chatReq.Messages[2].Content != "Newest user request wins." {
		t.Fatalf("expected latest_reminder to normalize to user at index 2, got %#v", chatReq.Messages[2])
	}
}

func TestReviewPromptAddsCodexReviewFormattingGuidance(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type":    "message",
				"role":    "user",
				"content": "帮我 review 最近 2 天修改的代码",
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 2 {
		t.Fatalf("expected system guidance plus user message, got %#v", chatReq.Messages)
	}
	if chatReq.Messages[0].Role != "system" {
		t.Fatalf("expected review guidance to be a system message, got %#v", chatReq.Messages[0])
	}
	systemContent, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected system content string, got %T", chatReq.Messages[0].Content)
	}
	for _, expected := range []string{"Codex code review format", "Findings", "Do not include Scope", "End with a concise completion sentence"} {
		if !strings.Contains(systemContent, expected) {
			t.Fatalf("expected review guidance to contain %q, got %q", expected, systemContent)
		}
	}
	if chatReq.Messages[1].Role != "user" || chatReq.Messages[1].Content != "帮我 review 最近 2 天修改的代码" {
		t.Fatalf("expected original user review prompt at index 1, got %#v", chatReq.Messages[1])
	}
}

// =============================================================================
// 3. Message item → correct role
// =============================================================================

func TestMessageItemCorrectRole(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "message",
			"role":    "assistant",
			"content": "I can help with that.",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", chatReq.Messages[0].Role)
	}
}

// =============================================================================
// 4. Function call → tool_calls
// =============================================================================

func TestFunctionCallToToolCalls(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_abc123",
			"name":      "get_weather",
			"arguments": `{"city":"Boston"}`,
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	msg := chatReq.Messages[0]
	if msg.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msg.Role)
	}
	if len(msg.ToolCalls) == 0 {
		t.Fatal("expected non-empty ToolCalls")
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(msg.ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal ToolCalls: %v", err)
	}
	if len(tcs) != 1 {
		t.Fatalf("expected 1 ToolCall, got %d", len(tcs))
	}
	if tcs[0].ID != "call_abc123" {
		t.Errorf("expected call_id 'call_abc123', got %q", tcs[0].ID)
	}
	if tcs[0].Type != "function" {
		t.Errorf("expected type 'function', got %v", tcs[0].Type)
	}
	if tcs[0].Function.Name != "get_weather" {
		t.Errorf("expected name 'get_weather', got %q", tcs[0].Function.Name)
	}
	if tcs[0].Function.Arguments != `{"city":"Boston"}` {
		t.Errorf("expected arguments '%s', got %q", `{"city":"Boston"}`, tcs[0].Function.Arguments)
	}
}

func TestFunctionCallCarriesReasoningContent(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":              "function_call",
			"call_id":           "call_reasoning_1",
			"name":              "get_weather",
			"arguments":         `{"city":"Boston"}`,
			"reasoning_content": "Need weather data before answering.",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].ReasoningContent != "Need weather data before answering." {
		t.Fatalf("expected reasoning_content to round-trip, got %q", chatReq.Messages[0].ReasoningContent)
	}
}

func TestReasoningItemAttachesToFollowingFunctionCall(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type": "reasoning",
			"text": "Need weather data before answering.",
		},
		{
			"type":      "function_call",
			"call_id":   "call_reasoning_2",
			"name":      "get_weather",
			"arguments": `{"city":"Boston"}`,
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 assistant tool_call message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" {
		t.Fatalf("expected assistant role, got %q", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[0].ReasoningContent != "Need weather data before answering." {
		t.Fatalf("expected reasoning_content to attach to tool call message, got %q", chatReq.Messages[0].ReasoningContent)
	}
	if len(chatReq.Messages[0].ToolCalls) == 0 {
		t.Fatal("expected tool_calls on assistant message")
	}
}

func TestTrailingReasoningAttachesToPreviousFunctionCall(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_reasoning_3",
			"name":      "get_weather",
			"arguments": `{"city":"Boston"}`,
		},
		{
			"type":    "function_call_output",
			"call_id": "call_reasoning_3",
			"output":  "sunny, 72F",
		},
		{
			"type":    "reasoning",
			"summary": "Need weather data before answering.",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" {
		t.Fatalf("expected first message role assistant, got %q", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[0].ReasoningContent != "Need weather data before answering." {
		t.Fatalf("expected trailing reasoning to backfill assistant tool call, got %q", chatReq.Messages[0].ReasoningContent)
	}
	if chatReq.Messages[1].Role != "tool" {
		t.Fatalf("expected second message role tool, got %q", chatReq.Messages[1].Role)
	}
}

func TestFunctionCallGetsPlaceholderReasoningContent(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_reasoning_4",
			"name":      "get_weather",
			"arguments": `{"city":"Boston"}`,
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "deepseek-reasoner",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" {
		t.Fatalf("expected assistant role, got %q", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[0].ReasoningContent != "tool call" {
		t.Fatalf("expected placeholder reasoning_content, got %q", chatReq.Messages[0].ReasoningContent)
	}
}

func TestAdjacentFunctionCallsGroupedBeforeToolOutputs(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_group_1",
			"name":      "get_weather",
			"arguments": `{"city":"Boston"}`,
		},
		{
			"type":      "function_call",
			"call_id":   "call_group_2",
			"name":      "get_time",
			"arguments": `{"timezone":"Asia/Shanghai"}`,
		},
		{
			"type":    "function_call_output",
			"call_id": "call_group_1",
			"output":  "sunny, 72F",
		},
		{
			"type":    "function_call_output",
			"call_id": "call_group_2",
			"output":  "08:00",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" {
		t.Fatalf("expected first message role assistant, got %q", chatReq.Messages[0].Role)
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal ToolCalls: %v", err)
	}
	if len(tcs) != 2 {
		t.Fatalf("expected 2 grouped tool calls, got %d", len(tcs))
	}
	if tcs[0].ID != "call_group_1" || tcs[1].ID != "call_group_2" {
		t.Fatalf("unexpected tool call order: %#v", tcs)
	}
	if chatReq.Messages[1].Role != "tool" || chatReq.Messages[1].ToolCallId != "call_group_1" {
		t.Fatalf("expected tool output for call_group_1 at index 1, got %#v", chatReq.Messages[1])
	}
	if chatReq.Messages[2].Role != "tool" || chatReq.Messages[2].ToolCallId != "call_group_2" {
		t.Fatalf("expected tool output for call_group_2 at index 2, got %#v", chatReq.Messages[2])
	}
}

func TestAssistantMessageBetweenFunctionCallAndOutputMovesBeforeToolCall(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_interleaved",
			"name":      "read_file",
			"arguments": `{"path":"README.md"}`,
		},
		{
			"type":    "message",
			"role":    "assistant",
			"content": "I need to read the relevant files.",
		},
		{
			"type":    "function_call_output",
			"call_id": "call_interleaved",
			"output":  "file content",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" || chatReq.Messages[0].Content != "I need to read the relevant files." {
		t.Fatalf("expected assistant text before tool_calls, got %#v", chatReq.Messages[0])
	}
	if chatReq.Messages[1].Role != "assistant" || len(chatReq.Messages[1].ToolCalls) == 0 {
		t.Fatalf("expected assistant tool_calls at index 1, got %#v", chatReq.Messages[1])
	}
	if chatReq.Messages[2].Role != "tool" || chatReq.Messages[2].ToolCallId != "call_interleaved" {
		t.Fatalf("expected tool output immediately after tool_calls, got %#v", chatReq.Messages[2])
	}
}

func TestMessageBetweenToolOutputsMovesAfterToolOutputGroup(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_group_1",
			"name":      "read_file",
			"arguments": `{"path":"README.md"}`,
		},
		{
			"type":      "function_call",
			"call_id":   "call_group_2",
			"name":      "search",
			"arguments": `{"q":"responses"}`,
		},
		{
			"type":    "function_call_output",
			"call_id": "call_group_1",
			"output":  "file content",
		},
		{
			"role":    "system",
			"content": "latest reminder",
		},
		{
			"type":    "function_call_output",
			"call_id": "call_group_2",
			"output":  "search output",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "system" || chatReq.Messages[0].Content != "latest reminder" {
		t.Fatalf("expected interleaved system message to collapse to head, got %#v", chatReq.Messages[0])
	}
	if chatReq.Messages[1].Role != "assistant" || len(chatReq.Messages[1].ToolCalls) == 0 {
		t.Fatalf("expected assistant tool_calls at index 1, got %#v", chatReq.Messages[1])
	}
	if chatReq.Messages[2].Role != "tool" || chatReq.Messages[2].ToolCallId != "call_group_1" {
		t.Fatalf("expected first tool output at index 2, got %#v", chatReq.Messages[2])
	}
	if chatReq.Messages[3].Role != "tool" || chatReq.Messages[3].ToolCallId != "call_group_2" {
		t.Fatalf("expected second tool output at index 3, got %#v", chatReq.Messages[3])
	}
}

// =============================================================================
// 5. Function call output → tool message
// =============================================================================

func TestFunctionCallOutputToToolMessage(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "function_call_output",
			"call_id": "call_xyz789",
			"output":  "sunny, 72F",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	msg := chatReq.Messages[0]
	if msg.Role != "tool" {
		t.Errorf("expected role 'tool', got %q", msg.Role)
	}
	if msg.ToolCallId != "call_xyz789" {
		t.Errorf("expected ToolCallId 'call_xyz789', got %q", msg.ToolCallId)
	}
	content, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", msg.Content)
	}
	if content != "sunny, 72F" {
		t.Errorf("expected content 'sunny, 72F', got %q", content)
	}
}

// =============================================================================
// 6. Custom tool call → prefixed name
// =============================================================================

func TestCustomToolCallKeepsOriginalName(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "custom_tool_call",
			"call_id":   "call_custom1",
			"name":      "my_tool",
			"arguments": `{"key":"value"}`,
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal ToolCalls: %v", err)
	}
	if len(tcs) != 1 {
		t.Fatalf("expected 1 ToolCall, got %d", len(tcs))
	}
	if tcs[0].Function.Name != "my_tool" {
		t.Errorf("expected name 'my_tool', got %q", tcs[0].Function.Name)
	}
}

// =============================================================================
// 7. Tool search call → synthetic tool
// =============================================================================

func TestToolSearchCallSyntheticTool(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "tool_search_call",
			"call_id": "search_call_1",
			"input":   "latest news",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal ToolCalls: %v", err)
	}
	if len(tcs) != 1 {
		t.Fatalf("expected 1 ToolCall, got %d", len(tcs))
	}
	if tcs[0].Function.Name != "tool_search" {
		t.Errorf("expected name 'tool_search', got %q", tcs[0].Function.Name)
	}
}

// =============================================================================
// 8. Mixed array
// =============================================================================

func TestMixedArray(t *testing.T) {
	inputItems := []any{
		"User starts the conversation",
		map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": "How can I help?",
		},
		map[string]any{
			"type":      "function_call",
			"call_id":   "call_mixed",
			"name":      "lookup",
			"arguments": `{"q":"test"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_mixed",
			"output":  "result found",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(chatReq.Messages))
	}
	// First: string → user
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("msg[0]: expected 'user', got %q", chatReq.Messages[0].Role)
	}
	// Second: message item with role=assistant
	if chatReq.Messages[1].Role != "assistant" {
		t.Errorf("msg[1]: expected 'assistant', got %q", chatReq.Messages[1].Role)
	}
	// Third: function_call → assistant with ToolCalls
	if chatReq.Messages[2].Role != "assistant" {
		t.Errorf("msg[2]: expected 'assistant', got %q", chatReq.Messages[2].Role)
	}
	if len(chatReq.Messages[2].ToolCalls) == 0 {
		t.Error("msg[2]: expected non-empty ToolCalls")
	}
	// Fourth: function_call_output → tool
	if chatReq.Messages[3].Role != "tool" {
		t.Errorf("msg[3]: expected 'tool', got %q", chatReq.Messages[3].Role)
	}
}

// =============================================================================
// 9. Reasoning item
// =============================================================================

func TestReasoningItem(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type": "reasoning",
			"text": "Let me think about this carefully.",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", chatReq.Messages[0].Role)
	}
	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", chatReq.Messages[0].Content)
	}
	expectedPrefix := "Reasoning: Let me think about this carefully."
	if content != expectedPrefix {
		t.Errorf("expected content %q, got %q", expectedPrefix, content)
	}
}

// TestReasoningUsesContentField tests that reasoning falls back to "content" if "text" is empty.
func TestReasoningUsesContentField(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "reasoning",
			"content": "Fallback reasoning text",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", chatReq.Messages[0].Content)
	}
	if content != "Reasoning: Fallback reasoning text" {
		t.Errorf("expected 'Reasoning: Fallback reasoning text', got %q", content)
	}
}

// =============================================================================
// 10. Params mapping
// =============================================================================

func TestParamsMapping(t *testing.T) {
	stream := true
	temperature := 0.7
	topP := 0.9
	maxTokens := uint(1000)
	topLogProbs := int(5)

	req := &dto.OpenAIResponsesRequest{
		Model:           "gpt-4o",
		Input:           mustMarshal(t, "Hello"),
		Stream:          &stream,
		Temperature:     &temperature,
		TopP:            &topP,
		MaxOutputTokens: &maxTokens,
		TopLogProbs:     &topLogProbs,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if chatReq.Stream == nil || *chatReq.Stream != true {
		t.Error("expected Stream=true")
	}
	if chatReq.Temperature == nil || *chatReq.Temperature != 0.7 {
		t.Error("expected Temperature=0.7")
	}
	if chatReq.TopP == nil || *chatReq.TopP != 0.9 {
		t.Error("expected TopP=0.9")
	}
	if chatReq.MaxCompletionTokens == nil || *chatReq.MaxCompletionTokens != 1000 {
		t.Error("expected MaxCompletionTokens=1000")
	}
	if chatReq.TopLogProbs == nil || *chatReq.TopLogProbs != 5 {
		t.Error("expected TopLogProbs=5")
	}
}

// =============================================================================
// 11. Tools mapping
// =============================================================================

func TestToolsMapping(t *testing.T) {
	toolsItems := []map[string]any{
		{
			"type":        "function",
			"name":        "get_weather",
			"description": "Get the weather for a city",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
			},
		},
		{
			"type":        "custom",
			"name":        "my_custom_tool",
			"description": "A custom tool",
		},
	}
	toolsRaw := mustMarshal(t, toolsItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(chatReq.Tools))
	}

	// First tool: function type → name unchanged
	if chatReq.Tools[0].Type != "function" {
		t.Errorf("tool[0]: expected type 'function', got %q", chatReq.Tools[0].Type)
	}
	if chatReq.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tool[0]: expected name 'get_weather', got %q", chatReq.Tools[0].Function.Name)
	}
	if chatReq.Tools[0].Function.Description != "Get the weather for a city" {
		t.Errorf("tool[0]: expected description, got %q", chatReq.Tools[0].Function.Description)
	}

	// Second tool: custom type keeps the original name and exposes an input envelope schema.
	if chatReq.Tools[1].Function.Name != "my_custom_tool" {
		t.Errorf("tool[1]: expected name 'my_custom_tool', got %q", chatReq.Tools[1].Function.Name)
	}
}

// TestToolsMappingWithToolChoice tests tool_choice passthrough.
func TestToolsMappingWithToolChoice(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "function", "name": "test_tool"},
	})
	toolChoiceRaw := mustMarshal(t, "auto")

	req := &dto.OpenAIResponsesRequest{
		Model:      "gpt-4o",
		Input:      mustMarshal(t, "Hello"),
		Tools:      toolsRaw,
		ToolChoice: toolChoiceRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.ToolChoice == nil {
		t.Fatal("expected non-nil ToolChoice")
	}
	tc, ok := chatReq.ToolChoice.(string)
	if !ok || tc != "auto" {
		t.Errorf("expected ToolChoice='auto', got %v", chatReq.ToolChoice)
	}
}

// =============================================================================
// 12. Response format
// =============================================================================

func TestResponseFormat(t *testing.T) {
	textRaw := mustMarshal(t, map[string]any{
		"format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "my_schema"},
		},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Text:  textRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.ResponseFormat == nil {
		t.Fatal("expected non-nil ResponseFormat")
	}
	if chatReq.ResponseFormat.Type != "json_schema" {
		t.Errorf("expected type 'json_schema', got %q", chatReq.ResponseFormat.Type)
	}
}

func TestResponseFormatEmptyText(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.ResponseFormat != nil {
		t.Error("expected nil ResponseFormat when text is empty")
	}
}

// =============================================================================
// 13. Image generation → error
// =============================================================================

func TestImageGenerationReturnsError(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":   "image_generation_call",
			"prompt": "a cat",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	_, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err == nil {
		t.Fatal("expected error for image_generation_call, got nil")
	}
	// Verify it contains something about image_generation_call
	if !contains(err.Error(), "image_generation_call") {
		t.Errorf("error should mention 'image_generation_call', got %q", err.Error())
	}
}

// =============================================================================
// 14. Unsupported type → error
// =============================================================================

func TestUnsupportedTypeReturnsError(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type": "unknown_weird_type",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	_, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err == nil {
		t.Fatal("expected error for unsupported type, got nil")
	}
	if !contains(err.Error(), "unknown_weird_type") {
		t.Errorf("error should mention the unsupported type, got %q", err.Error())
	}
}

// =============================================================================
// 15. Empty input → error
// =============================================================================

func TestEmptyInputReturnsError(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: nil,
	}

	_, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err == nil {
		t.Fatal("expected error for nil input, got nil")
	}
	if !contains(err.Error(), "input is required") {
		t.Errorf("expected 'input is required' in error, got %q", err.Error())
	}
}

func TestEmptyInputArrayReturnsMessages(t *testing.T) {
	// Empty array should be valid and return empty messages
	inputRaw := mustMarshal(t, []any{})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error for empty array: %v", err)
	}
	if len(chatReq.Messages) != 0 {
		t.Errorf("expected 0 messages for empty array, got %d", len(chatReq.Messages))
	}
}

// =============================================================================
// 16. Nested arrays
// =============================================================================

func TestNestedArrays(t *testing.T) {
	inputItems := []any{
		"String item",
		map[string]any{
			"type":    "message",
			"role":    "user",
			"content": "Object item",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("msg[0]: expected 'user', got %q", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[1].Role != "user" {
		t.Errorf("msg[1]: expected 'user', got %q", chatReq.Messages[1].Role)
	}
}

// =============================================================================
// extractInstructions tests
// =============================================================================

// 17. JSON string instructions
func TestExtractInstructionsJSONString(t *testing.T) {
	raw := mustMarshal(t, "you are helpful")
	result := extractInstructions(raw)
	if result != "you are helpful" {
		t.Errorf("expected 'you are helpful', got %q", result)
	}
}

// 18. Empty instructions
func TestExtractInstructionsEmpty(t *testing.T) {
	var empty json.RawMessage
	result := extractInstructions(empty)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestExtractInstructionsNil(t *testing.T) {
	var nilRaw json.RawMessage = nil
	result := extractInstructions(nilRaw)
	if result != "" {
		t.Errorf("expected empty string for nil, got %q", result)
	}
}

// 19. Non-string type
func TestExtractInstructionsNonString(t *testing.T) {
	raw := mustMarshal(t, 42)
	result := extractInstructions(raw)
	if result != "" {
		t.Errorf("expected empty string for number, got %q", result)
	}
}

func TestExtractInstructionsObject(t *testing.T) {
	raw := mustMarshal(t, map[string]any{"text": "hello"})
	result := extractInstructions(raw)
	if result != "" {
		t.Errorf("expected empty string for object, got %q", result)
	}
}

// =============================================================================
// EnrichRequestWithHistory tests
// =============================================================================

// 20. Nil store/req → nil error
func TestEnrichNilStoreOrReq(t *testing.T) {
	// Nil store
	err := EnrichRequestWithHistory(nil, "scope", 1, "session", &dto.OpenAIResponsesRequest{})
	if err != nil {
		t.Errorf("expected nil error for nil store, got %v", err)
	}

	// Nil req
	store := NewHistoryStore(10, time.Minute)
	err = EnrichRequestWithHistory(store, "scope", 1, "session", nil)
	if err != nil {
		t.Errorf("expected nil error for nil req, got %v", err)
	}
}

// 21. No continuation context
func TestEnrichNoContinuationContext(t *testing.T) {
	store := NewHistoryStore(10, time.Minute)
	inputRaw := mustMarshal(t, []any{
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_test",
			"output":  "result",
		},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
		// No PreviousResponseID, and sessionScope is "" in the call
	}

	err := EnrichRequestWithHistory(store, "scope", 1, "", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not change input since there's no continuation context
}

// 22. Matching calls present → no enrichment needed
func TestEnrichMatchingCallsPresent(t *testing.T) {
	store := NewHistoryStore(10, time.Minute)
	inputRaw := mustMarshal(t, []any{
		map[string]any{
			"type":      "function_call",
			"call_id":   "call_match",
			"name":      "test_tool",
			"arguments": `{}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_match",
			"output":  "done",
		},
	})

	req := &dto.OpenAIResponsesRequest{
		Model:              "gpt-4o",
		Input:              inputRaw,
		PreviousResponseID: "resp_123",
	}

	err := EnrichRequestWithHistory(store, "scope", 1, "", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Input should not change — both call and output are present
	var items []any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("failed to parse input: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

// 23. Missing calls recovered
func TestEnrichMissingCallsRecovered(t *testing.T) {
	store := NewHistoryStore(10, 10*time.Minute)

	// Pre-populate the cache with a response that had function_call "call_missing"
	store.Store("scope", 1, "resp_456", "session_abc", []CachedFunctionCall{
		{
			CallID:    "call_missing",
			Name:      "recovered_tool",
			Arguments: `{"x":1}`,
		},
	})

	inputRaw := mustMarshal(t, []any{
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_missing",
			"output":  "recovered result",
		},
	})

	req := &dto.OpenAIResponsesRequest{
		Model:              "gpt-4o",
		Input:              inputRaw,
		PreviousResponseID: "resp_456",
	}

	err := EnrichRequestWithHistory(store, "scope", 1, "session_abc", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Input should now have 2 items: the recovered function_call + original output
	var items []any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("failed to parse enriched input: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items in enriched input, got %d", len(items))
	}

	// First item should be the recovered function_call
	firstItem, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first item should be a map, got %T", items[0])
	}
	if firstItem["type"] != "function_call" {
		t.Errorf("expected first item type 'function_call', got %v", firstItem["type"])
	}
	if firstItem["call_id"] != "call_missing" {
		t.Errorf("expected call_id 'call_missing', got %v", firstItem["call_id"])
	}
	if firstItem["name"] != "recovered_tool" {
		t.Errorf("expected name 'recovered_tool', got %v", firstItem["name"])
	}

	// Second item should be the original function_call_output
	secondItem, ok := items[1].(map[string]any)
	if !ok {
		t.Fatalf("second item should be a map, got %T", items[1])
	}
	if secondItem["type"] != "function_call_output" {
		t.Errorf("expected second item type 'function_call_output', got %v", secondItem["type"])
	}
}

// 24. Cache miss
func TestEnrichCacheMiss(t *testing.T) {
	store := NewHistoryStore(10, time.Minute)
	// Don't store anything — cache will miss

	inputRaw := mustMarshal(t, []any{
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_nonexistent",
			"output":  "orphan result",
		},
	})

	req := &dto.OpenAIResponsesRequest{
		Model:              "gpt-4o",
		Input:              inputRaw,
		PreviousResponseID: "resp_nonexistent",
	}

	err := EnrichRequestWithHistory(store, "scope", 1, "session_x", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Input should be unchanged
	var items []any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("failed to parse input: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item (unchanged), got %d", len(items))
	}
}

// =============================================================================
// Additional edge case tests
// =============================================================================

// TestNilRequest tests ResponsesRequestToChatCompletionsRequest with nil request.
func TestNilRequest(t *testing.T) {
	_, _, err := ResponsesRequestToChatCompletionsRequest(nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

// TestSingleObjectInput tests a single JSON object as input (not an array).
func TestSingleObjectInput(t *testing.T) {
	inputRaw := mustMarshal(t, map[string]any{
		"type":    "message",
		"role":    "user",
		"content": "single object",
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", chatReq.Messages[0].Role)
	}
}

// TestInputItemWithoutTypeButWithRole tests items without a "type" but with "role".
func TestInputItemWithoutTypeButWithRole(t *testing.T) {
	inputItems := []map[string]any{
		{
			"role":    "assistant",
			"content": "I'm helping without a type field",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", chatReq.Messages[0].Role)
	}
}

// TestInputItemWithoutTypeOrRole tests items without type or role — should be user.
func TestInputItemWithoutTypeOrRole(t *testing.T) {
	inputItems := []map[string]any{
		{
			"some_key": "some_value",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", chatReq.Messages[0].Role)
	}
}

// TestResponsesInputToMessages tests the convenience wrapper.
func TestResponsesInputToMessages(t *testing.T) {
	inputRaw := mustMarshal(t, "Hello via wrapper")
	messages, err := ResponsesInputToMessages(inputRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", messages[0].Role)
	}
}

// TestFunctionCallArgumentsAsObject tests arguments that are objects (not strings).
// Interface2String uses fmt.Sprintf("%v", ...) as fallback for non-string types like map,
// so the resulting Arguments string is Go's %v representation, not JSON.
func TestFunctionCallArgumentsAsObject(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "function_call",
			"call_id": "call_obj_args",
			"name":    "process_data",
			"arguments": map[string]any{
				"key": "value",
				"num": float64(42),
			},
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal ToolCalls: %v", err)
	}
	if tcs[0].Function.Arguments == "" {
		t.Error("expected non-empty arguments string, got empty")
	}
	// Interface2String returns fmt.Sprintf("%v", ...) for maps, so we get Go's %v representation
	// The result contains "key:value" and "num:42" as evidence the map was stringified
	if !contains(tcs[0].Function.Arguments, "key") {
		t.Errorf("expected arguments to contain 'key', got %q", tcs[0].Function.Arguments)
	}
}

// TestToolOutputWithObjectOutput tests function_call_output where output is an object.
// Interface2String uses fmt.Sprintf("%v", ...) as fallback for non-string types like map.
func TestToolOutputWithObjectOutput(t *testing.T) {
	outputObj := map[string]any{
		"status": "ok",
		"data":   map[string]any{"temp": 72},
	}
	inputItems := []map[string]any{
		{
			"type":    "function_call_output",
			"call_id": "call_output_obj",
			"output":  outputObj,
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", chatReq.Messages[0].Content)
	}
	// Interface2String returns fmt.Sprintf("%v", ...) for maps
	if !contains(content, "status") || !contains(content, "ok") {
		t.Errorf("expected content to contain output data, got %q", content)
	}
}

// TestReasoningEffortMapping tests that reasoning.effort is mapped.
func TestReasoningEffortMapping(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Reasoning: &dto.Reasoning{
			Effort: "high",
		},
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.ReasoningEffort != "high" {
		t.Errorf("expected ReasoningEffort='high', got %q", chatReq.ReasoningEffort)
	}
}

// TestParallelToolCallsMapping tests parallel_tool_calls mapping.
func TestParallelToolCallsMapping(t *testing.T) {
	parallelRaw := mustMarshal(t, false)

	req := &dto.OpenAIResponsesRequest{
		Model:             "gpt-4o",
		Input:             mustMarshal(t, "Hello"),
		ParallelToolCalls: parallelRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.ParallelTooCalls == nil || *chatReq.ParallelTooCalls != false {
		t.Errorf("expected ParallelTooCalls=false, got %v", chatReq.ParallelTooCalls)
	}
}

// TestStreamOptionsMapping tests StreamOptions mapping.
func TestStreamOptionsMapping(t *testing.T) {
	streamOpts := &dto.StreamOptions{
		IncludeUsage: true,
	}

	req := &dto.OpenAIResponsesRequest{
		Model:         "gpt-4o",
		Input:         mustMarshal(t, "Hello"),
		StreamOptions: streamOpts,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.StreamOptions == nil {
		t.Fatal("expected non-nil StreamOptions")
	}
	if !chatReq.StreamOptions.IncludeUsage {
		t.Error("expected IncludeUsage=true")
	}
}

func TestStreamingRequestInjectsIncludeUsageStreamOption(t *testing.T) {
	stream := true
	req := &dto.OpenAIResponsesRequest{
		Model:  "gpt-4o",
		Input:  mustMarshal(t, "Hello"),
		Stream: &stream,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.StreamOptions == nil {
		t.Fatal("expected StreamOptions to be injected for streaming request")
	}
	if !chatReq.StreamOptions.IncludeUsage {
		t.Fatal("expected injected StreamOptions.IncludeUsage=true")
	}
}

// TestMetadataMapping tests metadata mapping.
func TestMetadataMapping(t *testing.T) {
	metaRaw := mustMarshal(t, map[string]string{"key": "value"})

	req := &dto.OpenAIResponsesRequest{
		Model:    "gpt-4o",
		Input:    mustMarshal(t, "Hello"),
		Metadata: metaRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Metadata) == 0 {
		t.Error("expected non-empty Metadata")
	}
}

// TestUserMapping tests user mapping.
func TestUserMapping(t *testing.T) {
	userRaw := mustMarshal(t, "test_user_123")

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		User:  userRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.User) == 0 {
		t.Error("expected non-empty User")
	}
}

// TestServiceTierMapping tests service_tier mapping.
func TestServiceTierMapping(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model:       "gpt-4o",
		Input:       mustMarshal(t, "Hello"),
		ServiceTier: "auto",
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.ServiceTier) == 0 {
		t.Error("expected non-empty ServiceTier")
	}
}

// TestPromptCacheKeyMapping tests prompt_cache_key mapping.
func TestPromptCacheKeyMapping(t *testing.T) {
	cacheKeyRaw := mustMarshal(t, "cache_key_abc")

	req := &dto.OpenAIResponsesRequest{
		Model:          "gpt-4o",
		Input:          mustMarshal(t, "Hello"),
		PromptCacheKey: cacheKeyRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.PromptCacheKey != "cache_key_abc" {
		t.Errorf("expected PromptCacheKey='cache_key_abc', got %q", chatReq.PromptCacheKey)
	}
}

// TestFunctionCallMissingCallID tests error for function_call without call_id.
func TestFunctionCallMissingCallID(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "function_call",
			"name":      "no_call_id_tool",
			"arguments": `{}`,
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	_, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err == nil {
		t.Fatal("expected error for function_call without call_id")
	}
	if !contains(err.Error(), "call_id") {
		t.Errorf("error should mention 'call_id', got %q", err.Error())
	}
}

// TestEmptyInputArrayWithInstructions tests that instructions are still prepended even with empty input.
func TestEmptyInputArrayWithInstructions(t *testing.T) {
	inputRaw := mustMarshal(t, []any{})
	instructionsRaw := mustMarshal(t, "System instruction only")

	req := &dto.OpenAIResponsesRequest{
		Model:        "gpt-4o",
		Input:        inputRaw,
		Instructions: instructionsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("expected 1 message (system only), got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", chatReq.Messages[0].Role)
	}
}

// TestToolSearchCallWithArguments tests tool_search_call with "arguments" key.
func TestToolSearchCallWithArguments(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":      "tool_search_call",
			"call_id":   "search_call_2",
			"arguments": `{"query":"test"}`,
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal ToolCalls: %v", err)
	}
	if tcs[0].Function.Name != "tool_search" {
		t.Errorf("expected name 'tool_search', got %q", tcs[0].Function.Name)
	}
}

func TestCustomToolCallUsesInputArgumentEnvelope(t *testing.T) {
	inputRaw := mustMarshal(t, []map[string]any{
		{
			"type":    "custom_tool_call",
			"call_id": "call_custom_1",
			"name":    "my_tool",
			"input":   "scan the repo",
		},
	})

	req := &dto.OpenAIResponsesRequest{Model: "gpt-4o", Input: inputRaw}
	chatReq, toolCtx, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal tool calls: %v", err)
	}

	if tcs[0].Function.Name != "my_tool" {
		t.Fatalf("expected chat tool name my_tool, got %q", tcs[0].Function.Name)
	}
	if tcs[0].Function.Arguments != `{"input":"scan the repo"}` {
		t.Fatalf("expected custom tool input envelope, got %q", tcs[0].Function.Arguments)
	}
	if !toolCtx.IsCustomTool("my_tool") {
		t.Fatal("expected tool context to classify my_tool as custom")
	}
}

func TestToolSearchCallUsesProxyFunctionName(t *testing.T) {
	inputRaw := mustMarshal(t, []map[string]any{
		{
			"type":      "tool_search_call",
			"call_id":   "call_tool_search_1",
			"arguments": map[string]any{"query": "gmail search emails", "limit": 10},
		},
	})

	req := &dto.OpenAIResponsesRequest{Model: "gpt-4o", Input: inputRaw}
	chatReq, toolCtx, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tcs []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &tcs); err != nil {
		t.Fatalf("failed to unmarshal tool calls: %v", err)
	}

	if tcs[0].Function.Name != "tool_search" {
		t.Fatalf("expected proxy function name tool_search, got %q", tcs[0].Function.Name)
	}
	if toolCtx.RestoreToolType("tool_search", "function_call") != "tool_search_call" {
		t.Fatal("expected tool_search to restore as tool_search_call")
	}
}

func TestNamespaceToolChoiceMapsToFlattenedChatName(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{
			"type":      "function",
			"name":      "search_threads",
			"namespace": "mcp__codex_apps__gmail",
			"parameters": map[string]any{
				"type": "object",
			},
		},
	})
	toolChoiceRaw := mustMarshal(t, map[string]any{
		"type":      "function",
		"name":      "search_threads",
		"namespace": "mcp__codex_apps__gmail",
	})

	tools, toolChoice, toolCtx, err := buildChatTools(toolsRaw, toolChoiceRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tools[0].Function.Name != "mcp__codex_apps__gmail__search_threads" {
		t.Fatalf("expected flattened namespace chat name, got %q", tools[0].Function.Name)
	}
	tcMap := toolChoice.(map[string]any)
	fn := tcMap["function"].(map[string]any)
	if fn["name"] != "mcp__codex_apps__gmail__search_threads" {
		t.Fatalf("expected flattened tool choice name, got %v", fn["name"])
	}
	if toolCtx.RestoreToolType("mcp__codex_apps__gmail__search_threads", "function_call") != "function_call" {
		t.Fatal("expected namespace tool to restore as function_call")
	}
}

// TestCustomToolCallOutput tests custom_tool_call_output type.
func TestCustomToolCallOutput(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "custom_tool_call_output",
			"call_id": "custom_out_1",
			"output":  "custom result",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Messages[0].Role != "tool" {
		t.Errorf("expected role 'tool', got %q", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[0].ToolCallId != "custom_out_1" {
		t.Errorf("expected ToolCallId 'custom_out_1', got %q", chatReq.Messages[0].ToolCallId)
	}
}

// TestToolSearchOutput tests tool_search_output type.
func TestToolSearchOutput(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "tool_search_output",
			"call_id": "search_out_1",
			"output":  "search result",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Messages[0].Role != "tool" {
		t.Errorf("expected role 'tool', got %q", chatReq.Messages[0].Role)
	}
}

// TestToolsWebSearchPrefixing tests that web_search tool type gets name prefixed.
func TestToolsWebSearchPrefixing(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "web_search", "name": "search"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Name != "web_search_search" {
		t.Errorf("expected name 'web_search_search', got %q", chatReq.Tools[0].Function.Name)
	}
}

// TestToolsFileSearchPrefixing tests file_search prefixing.
func TestToolsFileSearchPrefixing(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "file_search", "name": "files"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Name != "file_search_files" {
		t.Errorf("expected name 'file_search_files', got %q", chatReq.Tools[0].Function.Name)
	}
}

// TestToolsCodeInterpreterPrefixing tests code_interpreter prefixing.
func TestToolsCodeInterpreterPrefixing(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "code_interpreter", "name": "python"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Name != "code_interpreter_python" {
		t.Errorf("expected name 'code_interpreter_python', got %q", chatReq.Tools[0].Function.Name)
	}
}

// TestToolsLocalShellPrefixing tests local_shell prefixing.
func TestToolsLocalShellPrefixing(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "local_shell", "name": "bash"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Name != "local_shell_bash" {
		t.Errorf("expected name 'local_shell_bash', got %q", chatReq.Tools[0].Function.Name)
	}
}

// TestToolsImageGenerationPrefixing tests image_generation prefixing.
func TestToolsImageGenerationPrefixing(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "image_generation", "name": "dalle"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Name != "image_generation_dalle" {
		t.Errorf("expected name 'image_generation_dalle', got %q", chatReq.Tools[0].Function.Name)
	}
}

// TestToolsFallbackToFunctionName tests fallback to function_name when name is empty.
func TestToolsFallbackToFunctionName(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "function", "function_name": "fallback_func"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Name != "fallback_func" {
		t.Errorf("expected name 'fallback_func', got %q", chatReq.Tools[0].Function.Name)
	}
}

// TestToolsFallbackToTypeName tests fallback to type name when both name and function_name are empty.
func TestToolsFallbackToTypeName(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "function"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Name != "function" {
		t.Errorf("expected name 'function', got %q", chatReq.Tools[0].Function.Name)
	}
}

// TestToolsSchemaAsParameters tests schema field used as parameters.
func TestToolsSchemaAsParameters(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "integer"}},
	}
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "function", "name": "schema_tool", "schema": schema},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Parameters == nil {
		t.Error("expected non-nil Parameters from schema field")
	}
}

// TestToolsFallbackToDescription tests fallback to function_description.
func TestToolsFallbackToDescription(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "function", "name": "desc_tool", "function_description": "A described tool"},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: toolsRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Tools[0].Function.Description != "A described tool" {
		t.Errorf("expected description 'A described tool', got %q", chatReq.Tools[0].Function.Description)
	}
}

// =============================================================================
// Tool choice remapping tests (Task 1)
// =============================================================================

// TestToolChoiceFunctionObjectMapsToNestedChatSelector tests that a Responses
// tool_choice of shape {"type":"function","name":"get_weather"} is converted
// into Chat's nested form {"type":"function","function":{"name":"get_weather"}}.
func TestToolChoiceFunctionObjectMapsToNestedChatSelector(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: mustMarshal(t, []map[string]any{
			{"type": "function", "name": "get_weather"},
		}),
		ToolChoice: mustMarshal(t, map[string]any{
			"type": "function",
			"name": "get_weather",
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc, ok := chatReq.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("expected object ToolChoice, got %T", chatReq.ToolChoice)
	}
	if tc["type"] != "function" {
		t.Fatalf("expected type=function, got %v", tc["type"])
	}
	fn, ok := tc["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested function object, got %T", tc["function"])
	}
	if fn["name"] != "get_weather" {
		t.Fatalf("expected nested function name get_weather, got %v", fn["name"])
	}
}

// TestToolChoiceCustomToolMapsToNestedChatSelector tests that a custom-type
// tool_choice {"type":"custom","name":"my_tool"} is remapped to the chat-visible
// prefixed name in the nested Chat selector format.
func TestToolChoiceCustomToolMapsToNestedChatSelector(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: mustMarshal(t, []map[string]any{
			{"type": "custom", "name": "my_tool"},
		}),
		ToolChoice: mustMarshal(t, map[string]any{
			"type": "custom",
			"name": "my_tool",
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc, ok := chatReq.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("expected object ToolChoice, got %T", chatReq.ToolChoice)
	}
	if tc["type"] != "function" {
		t.Fatalf("expected type=function in Chat tool_choice, got %v", tc["type"])
	}
	fn, ok := tc["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested function object, got %T", tc["function"])
	}
	if fn["name"] != "my_tool" {
		t.Fatalf("expected nested function name my_tool, got %v", fn["name"])
	}
}

// TestToolChoiceFileSearchMapsToNestedChatSelector tests that a file_search
// tool_choice is remapped to the prefixed chat-visible name.
func TestToolChoiceFileSearchMapsToNestedChatSelector(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: mustMarshal(t, []map[string]any{
			{"type": "file_search", "name": "search"},
		}),
		ToolChoice: mustMarshal(t, map[string]any{
			"type": "file_search",
			"name": "search",
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc, ok := chatReq.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("expected object ToolChoice, got %T", chatReq.ToolChoice)
	}
	fn, ok := tc["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested function object, got %T", tc["function"])
	}
	// The file_search tool name is prefixed to "file_search_search" in Chat tools.
	if fn["name"] != "file_search_search" {
		t.Fatalf("expected nested function name file_search_search, got %v", fn["name"])
	}
}

// TestToolChoiceWebSearchMapsToNestedChatSelector tests that a web_search
// tool_choice is remapped to the prefixed chat-visible name.
func TestToolChoiceWebSearchMapsToNestedChatSelector(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, "Hello"),
		Tools: mustMarshal(t, []map[string]any{
			{"type": "web_search", "name": "search"},
		}),
		ToolChoice: mustMarshal(t, map[string]any{
			"type": "web_search",
			"name": "search",
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc, ok := chatReq.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("expected object ToolChoice, got %T", chatReq.ToolChoice)
	}
	fn, ok := tc["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested function object, got %T", tc["function"])
	}
	// The web_search tool name is prefixed to "web_search_search" in Chat tools.
	if fn["name"] != "web_search_search" {
		t.Fatalf("expected nested function name web_search_search, got %v", fn["name"])
	}
}

// TestToolChoiceAutoPassthrough tests that simple string tool_choice values
// like "auto" and "none" are passed through unchanged.
func TestToolChoiceAutoPassthrough(t *testing.T) {
	for _, choice := range []string{"auto", "none", "required"} {
		t.Run(choice, func(t *testing.T) {
			req := &dto.OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: mustMarshal(t, "Hello"),
				Tools: mustMarshal(t, []map[string]any{
					{"type": "function", "name": "test_tool"},
				}),
				ToolChoice: mustMarshal(t, choice),
			}

			chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc, ok := chatReq.ToolChoice.(string)
			if !ok || tc != choice {
				t.Errorf("expected ToolChoice=%q, got %v", choice, chatReq.ToolChoice)
			}
		})
	}
}

// TestBuildChatToolsReturnsToolContext verifies that the internal buildChatTools
// populates the chatToolContext with correct name mappings.
func TestBuildChatToolsReturnsToolContext(t *testing.T) {
	toolsRaw := mustMarshal(t, []map[string]any{
		{"type": "function", "name": "get_weather"},
		{"type": "custom", "name": "my_tool"},
		{"type": "file_search", "name": "files"},
	})

	tools, _, ctx, err := buildChatTools(toolsRaw, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	if ctx == nil {
		t.Fatal("expected non-nil chatToolContext")
	}

	// Verify name mappings
	if ctx.responseNameToChatName["get_weather"] != "get_weather" {
		t.Errorf("expected get_weather -> get_weather, got %s", ctx.responseNameToChatName["get_weather"])
	}
	if ctx.responseNameToChatName["my_tool"] != "my_tool" {
		t.Errorf("expected my_tool -> my_tool, got %s", ctx.responseNameToChatName["my_tool"])
	}
	if ctx.responseNameToChatName["files"] != "file_search_files" {
		t.Errorf("expected files -> file_search_files, got %s", ctx.responseNameToChatName["files"])
	}
}

// TestToolContextTracksAllPrefixedTypes verifies that the chatToolContext
// maps all prefixed tool types correctly, including web_search_preview variants,
// code_interpreter, local_shell, and image_generation.
func TestToolContextTracksAllPrefixedTypes(t *testing.T) {
	for _, tc := range []struct {
		toolType    string
		toolName    string
		expectedMap string
	}{
		{"web_search", "ws", "web_search_ws"},
		{"web_search_preview", "ws", "web_search_ws"},
		{"web_search_preview_2025_03_11", "ws", "web_search_ws"},
		{"file_search", "fs", "file_search_fs"},
		{"code_interpreter", "ci", "code_interpreter_ci"},
		{"local_shell", "ls", "local_shell_ls"},
		{"image_generation", "ig", "image_generation_ig"},
		{"custom", "ct", "ct"},
		{"function", "fn", "fn"},
	} {
		t.Run(tc.toolType, func(t *testing.T) {
			toolsRaw := mustMarshal(t, []map[string]any{
				{"type": tc.toolType, "name": tc.toolName},
			})

			_, _, ctx, err := buildChatTools(toolsRaw, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ctx == nil {
				t.Fatal("expected non-nil chatToolContext")
			}
			if ctx.responseNameToChatName[tc.toolName] != tc.expectedMap {
				t.Errorf("expected %s -> %s, got %s", tc.toolName, tc.expectedMap, ctx.responseNameToChatName[tc.toolName])
			}
		})
	}
}

// =============================================================================
// TestMessageTypeDefaultsRoleToUser tests that message type with no role defaults to "user".
func TestMessageTypeDefaultsRoleToUser(t *testing.T) {
	inputItems := []map[string]any{
		{
			"type":    "message",
			"content": "Message without explicit role",
		},
	}
	inputRaw := mustMarshal(t, inputItems)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("expected default role 'user', got %q", chatReq.Messages[0].Role)
	}
}

// TestEnrichWithSessionScopeFallback tests that enrichment works via sessionScope fallback.
func TestEnrichWithSessionScopeFallback(t *testing.T) {
	store := NewHistoryStore(10, 10*time.Minute)

	// Store with session scope
	store.Store("scope", 1, "resp_session", "my_session", []CachedFunctionCall{
		{
			CallID:    "call_session",
			Name:      "session_tool",
			Arguments: `{}`,
		},
	})

	inputRaw := mustMarshal(t, []any{
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_session",
			"output":  "session result",
		},
	})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: inputRaw,
		// No PreviousResponseID — relies on sessionScope fallback
	}

	err := EnrichRequestWithHistory(store, "scope", 1, "my_session", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var items []any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("failed to parse enriched input: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	firstItem := items[0].(map[string]any)
	if firstItem["type"] != "function_call" {
		t.Errorf("expected 'function_call', got %v", firstItem["type"])
	}
}

// TestEnrichSingleInputSkips tests that a single non-array input is skipped.
func TestEnrichSingleInputSkips(t *testing.T) {
	store := NewHistoryStore(10, time.Minute)
	store.Store("scope", 1, "resp_single", "sess", []CachedFunctionCall{
		{CallID: "call_x", Name: "t", Arguments: "{}"},
	})

	inputRaw := mustMarshal(t, "just a string — not an array")

	req := &dto.OpenAIResponsesRequest{
		Model:              "gpt-4o",
		Input:              inputRaw,
		PreviousResponseID: "resp_single",
	}

	err := EnrichRequestWithHistory(store, "scope", 1, "sess", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Input should still be a string (unchanged)
	var input any
	if err := common.Unmarshal(req.Input, &input); err != nil {
		t.Fatalf("failed to parse input: %v", err)
	}
	if s, ok := input.(string); !ok || s != "just a string — not an array" {
		t.Errorf("expected unchanged string input, got %v", input)
	}
}

// TestEnrichDuplicateCallIDOnlyOneRecovery tests that a call_id is only recovered once.
func TestEnrichDuplicateCallIDOnlyOneRecovery(t *testing.T) {
	store := NewHistoryStore(10, 10*time.Minute)
	store.Store("scope", 1, "resp_dup", "sess_dup", []CachedFunctionCall{
		{CallID: "call_dup", Name: "dup_tool", Arguments: "{}"},
	})

	inputRaw := mustMarshal(t, []any{
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_dup",
			"output":  "first",
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_dup",
			"output":  "second",
		},
	})

	req := &dto.OpenAIResponsesRequest{
		Model:              "gpt-4o",
		Input:              inputRaw,
		PreviousResponseID: "resp_dup",
	}

	err := EnrichRequestWithHistory(store, "scope", 1, "sess_dup", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var items []any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("failed to parse input: %v", err)
	}
	// 1 recovered call + 2 original outputs = 3
	if len(items) != 3 {
		t.Fatalf("expected 3 items (1 recovered + 2 outputs), got %d", len(items))
	}
}

// =============================================================================
// Structured content parts tests (Task 2: Responses content → Chat content)
// =============================================================================

// TestMessageContentPartsMapToChatContentParts tests that structured Responses
// message content (arrays of input_text/input_image parts) is properly converted
// to Chat Completions content part format.
func TestMessageContentPartsMapToChatContentParts(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "Weather?"},
					{"type": "input_image", "image_url": "data:image/png;base64,abc"},
				},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts, ok := chatReq.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("expected content parts, got %T", chatReq.Messages[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
}

// TestInputTextMapsToChatTextPart verifies that input_text content parts
// are converted to Chat text parts with type="text".
func TestInputTextMapsToChatTextPart(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "Hello world"},
				},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts, ok := chatReq.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("expected content parts, got %T", chatReq.Messages[0].Content)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}

	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map part, got %T", parts[0])
	}
	if part["type"] != "text" {
		t.Errorf("expected part type 'text', got %v", part["type"])
	}
	if part["text"] != "Hello world" {
		t.Errorf("expected part text 'Hello world', got %v", part["text"])
	}
}

// TestInputImageMapsToChatImageUrlPart verifies that input_image content parts
// are converted to Chat image_url parts with nested image_url.url structure.
func TestInputImageMapsToChatImageUrlPart(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_image", "image_url": "https://example.com/img.png"},
				},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts, ok := chatReq.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("expected content parts, got %T", chatReq.Messages[0].Content)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}

	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map part, got %T", parts[0])
	}
	if part["type"] != "image_url" {
		t.Errorf("expected part type 'image_url', got %v", part["type"])
	}
	imgUrl, ok := part["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested image_url map, got %T", part["image_url"])
	}
	if imgUrl["url"] != "https://example.com/img.png" {
		t.Errorf("expected url 'https://example.com/img.png', got %v", imgUrl["url"])
	}
}

// TestMixedTextAndImageContent verifies that a message with both text and image
// parts converts all parts correctly.
func TestMixedTextAndImageContent(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "What's in this image?"},
					{"type": "input_image", "image_url": "data:image/jpeg;base64,xyz"},
				},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts, ok := chatReq.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("expected content parts, got %T", chatReq.Messages[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	// First part: text
	textPart, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map part[0], got %T", parts[0])
	}
	if textPart["type"] != "text" {
		t.Errorf("expected part[0] type 'text', got %v", textPart["type"])
	}

	// Second part: image_url
	imgPart, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("expected map part[1], got %T", parts[1])
	}
	if imgPart["type"] != "image_url" {
		t.Errorf("expected part[1] type 'image_url', got %v", imgPart["type"])
	}
}

// TestUnsupportedContentKindReturnsError verifies that unsupported content types
// like input_file and input_audio return an error instead of leaking the raw
// Responses shape.
func TestUnsupportedContentKindReturnsError(t *testing.T) {
	tests := []struct {
		name    string
		content []map[string]any
	}{
		{
			name: "input_file",
			content: []map[string]any{
				{"type": "input_file", "file_data": "some data"},
			},
		},
		{
			name: "input_audio",
			content: []map[string]any{
				{"type": "input_audio", "audio_data": "some audio"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &dto.OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: mustMarshal(t, []map[string]any{
					{
						"type":    "message",
						"role":    "user",
						"content": tt.content,
					},
				}),
			}

			_, _, err := ResponsesRequestToChatCompletionsRequest(req)
			if err == nil {
				t.Fatalf("expected error for unsupported content type %q, got nil", tt.name)
			}
		})
	}
}

// TestOutputTextMapsToChatTextPart verifies that output_text content parts
// (from assistant messages) are converted to Chat text parts.
func TestOutputTextMapsToChatTextPart(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "The weather is sunny."},
				},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts, ok := chatReq.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("expected content parts, got %T", chatReq.Messages[0].Content)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}

	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map part, got %T", parts[0])
	}
	if part["type"] != "text" {
		t.Errorf("expected part type 'text', got %v", part["type"])
	}
	if part["text"] != "The weather is sunny." {
		t.Errorf("expected part text 'The weather is sunny.', got %v", part["text"])
	}
}

// TestStringContentPassthrough verifies that simple string content is still
// passed through as a string (not wrapped in an array).
func TestStringContentPassthrough(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type":    "message",
				"role":    "user",
				"content": "Just a plain string",
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", chatReq.Messages[0].Content)
	}
	if content != "Just a plain string" {
		t.Errorf("expected content 'Just a plain string', got %q", content)
	}
}

// TestContentPartsViaRoleWithoutType verifies that structured content parts
// are also converted when the input item has no "type" but has a "role"
// (the typeVal=="" && roleVal!="" code path).
func TestContentPartsViaRoleWithoutType(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "Describe this"},
					{"type": "input_image", "image_url": "https://example.com/photo.jpg"},
				},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts, ok := chatReq.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("expected content parts, got %T", chatReq.Messages[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	// First part: text
	textPart, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map part[0], got %T", parts[0])
	}
	if textPart["type"] != "text" {
		t.Errorf("expected part[0] type 'text', got %v", textPart["type"])
	}

	// Second part: image_url
	imgPart, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("expected map part[1], got %T", parts[1])
	}
	if imgPart["type"] != "image_url" {
		t.Errorf("expected part[1] type 'image_url', got %v", imgPart["type"])
	}
}

// =============================================================================
// JSON preservation tests (Task 3: Preserve JSON semantics for tool arguments/outputs)
// =============================================================================

// TestFunctionCallArgumentsObjectPreserveJSON verifies that when function_call
// arguments is a JSON object (not a string), it gets marshaled to proper JSON
// instead of Go's fmt.Sprintf("%v") representation like "map[a:1 b:2]".
func TestFunctionCallArgumentsObjectPreserveJSON(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": map[string]any{"b": 2, "a": 1},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var toolCalls []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &toolCalls); err != nil {
		t.Fatalf("unmarshal tool calls: %v", err)
	}
	args := toolCalls[0].Function.Arguments
	if args == "" {
		t.Fatal("expected non-empty arguments, got empty string")
	}
	// The result must be valid JSON, not Go's fmt representation
	if strings.HasPrefix(args, "map[") {
		t.Fatalf("arguments were stringified with fmt semantics instead of JSON: %q", args)
	}
	// Verify it's actually parseable JSON
	var parsed map[string]any
	if err := common.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments is not valid JSON: %q, err: %v", args, err)
	}
	if parsed["a"] != float64(1) || parsed["b"] != float64(2) {
		t.Errorf("expected a=1, b=2, got %v", parsed)
	}
}

// TestFunctionCallArgumentsArrayPreserveJSON verifies that when function_call
// arguments is a JSON array, it gets marshaled to proper JSON.
func TestFunctionCallArgumentsArrayPreserveJSON(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type":      "function_call",
				"call_id":   "call_arr",
				"name":      "multi",
				"arguments": []any{"x", float64(1), true},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var toolCalls []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &toolCalls); err != nil {
		t.Fatalf("unmarshal tool calls: %v", err)
	}
	args := toolCalls[0].Function.Arguments
	// Must not be Go's slice formatting like "[x 1 true]"
	if strings.HasPrefix(args, "[") && !strings.HasPrefix(args, `["`) {
		t.Fatalf("arguments were stringified with fmt semantics instead of JSON: %q", args)
	}
	// Verify it's valid JSON
	var parsed []any
	if err := common.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments is not valid JSON: %q, err: %v", args, err)
	}
	if len(parsed) != 3 || parsed[0] != "x" || parsed[1] != float64(1) || parsed[2] != true {
		t.Errorf("expected [x, 1, true], got %v", parsed)
	}
}

// TestFunctionCallOutputObjectPreserveJSON verifies that when function_call_output
// output is a JSON object, it gets marshaled to proper JSON instead of Go's
// fmt.Sprintf("%v") representation.
func TestFunctionCallOutputObjectPreserveJSON(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type":    "function_call_output",
				"call_id": "call_out_obj",
				"output":  map[string]any{"status": "ok", "code": float64(200)},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", chatReq.Messages[0].Content)
	}
	// Must not be Go's map formatting like "map[code:200 status:ok]"
	if strings.HasPrefix(content, "map[") {
		t.Fatalf("output was stringified with fmt semantics instead of JSON: %q", content)
	}
	// Verify it's valid JSON
	var parsed map[string]any
	if err := common.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %q, err: %v", content, err)
	}
	if parsed["status"] != "ok" || parsed["code"] != float64(200) {
		t.Errorf("expected status=ok, code=200, got %v", parsed)
	}
}

// TestFunctionCallArgumentsBoolAndNumberPreserve verifies that explicit boolean
// false and numeric 0 values survive the conversion to proper JSON strings,
// not being silently dropped or converted to empty strings.
func TestFunctionCallArgumentsBoolAndNumberPreserve(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type":      "function_call",
				"call_id":   "call_bool_num",
				"name":      "toggle",
				"arguments": map[string]any{"enabled": false, "count": float64(0)},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var toolCalls []dto.ToolCallResponse
	if err := common.Unmarshal(chatReq.Messages[0].ToolCalls, &toolCalls); err != nil {
		t.Fatalf("unmarshal tool calls: %v", err)
	}
	args := toolCalls[0].Function.Arguments

	// Verify the result is valid JSON with explicit false and 0
	var parsed map[string]any
	if err := common.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments is not valid JSON: %q, err: %v", args, err)
	}
	if enabled, ok := parsed["enabled"].(bool); !ok || enabled != false {
		t.Errorf("expected enabled=false, got %v", parsed["enabled"])
	}
	if count, ok := parsed["count"].(float64); !ok || count != 0 {
		t.Errorf("expected count=0, got %v", parsed["count"])
	}
}

// TestFunctionCallOutputBoolAndNumberPreserve verifies that explicit boolean
// false and numeric 0 values in function_call_output survive the conversion.
func TestFunctionCallOutputBoolAndNumberPreserve(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: mustMarshal(t, []map[string]any{
			{
				"type":    "function_call_output",
				"call_id": "call_out_bool_num",
				"output":  map[string]any{"success": false, "result_count": float64(0)},
			},
		}),
	}

	chatReq, _, err := ResponsesRequestToChatCompletionsRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := chatReq.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", chatReq.Messages[0].Content)
	}

	var parsed map[string]any
	if err := common.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %q, err: %v", content, err)
	}
	if success, ok := parsed["success"].(bool); !ok || success != false {
		t.Errorf("expected success=false, got %v", parsed["success"])
	}
	if count, ok := parsed["result_count"].(float64); !ok || count != 0 {
		t.Errorf("expected result_count=0, got %v", parsed["result_count"])
	}
}

// contains checks if s contains substr (simple helper).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// Task 4 tests: in-place recovery and session-scope reuse
// =============================================================================

// TestHistoryRecoveryInsertsCallBeforeMatchingOutput tests that a recovered
// function_call is inserted immediately before its matching function_call_output,
// not prepended at the front of the entire array.
func TestHistoryRecoveryInsertsCallBeforeMatchingOutput(t *testing.T) {
	store := NewHistoryStore(16, time.Hour)
	store.Store("user:1", 10, "resp_prev", "sess_1", []CachedFunctionCall{
		{CallID: "call_1", Name: "read_file", Arguments: `{"path":"README.md"}`},
	})

	req := &dto.OpenAIResponsesRequest{
		PreviousResponseID: "resp_prev",
		Input: mustMarshal(t, []map[string]any{
			{"role": "user", "content": "continue"},
			{"type": "function_call_output", "call_id": "call_1", "output": "done"},
		}),
	}

	if err := EnrichRequestWithHistory(store, "user:1", 10, "sess_1", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var items []map[string]any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d: %#v", len(items), items)
	}
	if items[0]["role"] != "user" {
		t.Fatalf("user message order changed unexpectedly: %#v", items)
	}
	if items[1]["type"] != "function_call" {
		t.Fatalf("expected recovered call at index 1, got %#v", items[1])
	}
	if items[2]["type"] != "function_call_output" {
		t.Fatalf("expected output at index 2, got %#v", items[2])
	}
}

// TestHistoryRecoveryPreservesUserMessageOrder tests that when multiple user
// messages and function_call_output items exist, the original ordering of
// user messages is preserved and recovered calls are placed adjacent to
// their matching outputs.
func TestHistoryRecoveryPreservesUserMessageOrder(t *testing.T) {
	store := NewHistoryStore(16, time.Hour)
	store.Store("user:1", 10, "resp_prev", "sess_1", []CachedFunctionCall{
		{CallID: "call_a", Name: "tool_a", Arguments: `{}`},
		{CallID: "call_b", Name: "tool_b", Arguments: `{}`},
	})

	req := &dto.OpenAIResponsesRequest{
		PreviousResponseID: "resp_prev",
		Input: mustMarshal(t, []map[string]any{
			{"role": "user", "content": "first message"},
			{"role": "user", "content": "second message"},
			{"type": "function_call_output", "call_id": "call_a", "output": "result_a"},
			{"role": "user", "content": "third message"},
			{"type": "function_call_output", "call_id": "call_b", "output": "result_b"},
		}),
	}

	if err := EnrichRequestWithHistory(store, "user:1", 10, "sess_1", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var items []map[string]any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	// Expected order:
	// 0: user "first message"
	// 1: user "second message"
	// 2: function_call call_a (recovered)
	// 3: function_call_output call_a
	// 4: user "third message"
	// 5: function_call call_b (recovered)
	// 6: function_call_output call_b

	expectedOrder := []struct {
		key      string
		val      string
		itemType string
	}{
		{"role", "user", ""},
		{"role", "user", ""},
		{"type", "function_call", ""},
		{"type", "function_call_output", ""},
		{"role", "user", ""},
		{"type", "function_call", ""},
		{"type", "function_call_output", ""},
	}

	if len(items) != len(expectedOrder) {
		t.Fatalf("expected %d items, got %d: %#v", len(expectedOrder), len(items), items)
	}

	for i, exp := range expectedOrder {
		v, ok := items[i][exp.key]
		if !ok {
			t.Fatalf("item[%d] missing key %q, got %#v", i, exp.key, items[i])
		}
		s, ok := v.(string)
		if !ok || s != exp.val {
			t.Fatalf("item[%d][%q] = %q, want %q", i, exp.key, v, exp.val)
		}
	}
}

// TestResolveResponsesSessionScopeSameScopeForLookupAndStore verifies that
// ResolveResponsesSessionScope extracts metadata.session_id from the request
// and that the same computed scope is used for both lookup and store operations.
func TestResolveResponsesSessionScopeSameScopeForLookupAndStore(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Metadata: mustMarshal(t, map[string]string{"session_id": "meta-session-xyz"}),
	}

	scope := ResolveResponsesSessionScope(req, "", "")
	if scope != "meta-session-xyz" {
		t.Errorf("expected 'meta-session-xyz', got %q", scope)
	}

	// Now verify the scope works for both store and lookup
	store := NewHistoryStore(16, time.Hour)
	store.Store("user:1", 10, "resp_1", scope, []CachedFunctionCall{
		{CallID: "call_1", Name: "my_tool", Arguments: `{}`},
	})

	// Lookup using the same scope should succeed
	entry := store.LookupByCallID("user:1", 10, scope, "call_1")
	if entry == nil {
		t.Fatal("expected to find entry using the same computed scope, got nil")
	}
}

// TestHistoryRecoveryMultipleRecoveriesAdjacentToOutputs tests that when
// multiple function_call_outputs are present with missing calls, each
// recovered call is placed directly before its matching output.
func TestHistoryRecoveryMultipleRecoveriesAdjacentToOutputs(t *testing.T) {
	store := NewHistoryStore(16, time.Hour)
	store.Store("user:1", 10, "resp_prev", "sess_1", []CachedFunctionCall{
		{CallID: "call_x", Name: "tool_x", Arguments: `{}`},
		{CallID: "call_y", Name: "tool_y", Arguments: `{}`},
	})

	req := &dto.OpenAIResponsesRequest{
		PreviousResponseID: "resp_prev",
		Input: mustMarshal(t, []map[string]any{
			{"type": "function_call_output", "call_id": "call_x", "output": "result_x"},
			{"type": "function_call_output", "call_id": "call_y", "output": "result_y"},
		}),
	}

	if err := EnrichRequestWithHistory(store, "user:1", 10, "sess_1", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var items []map[string]any
	if err := common.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	// Expected: call_x, output_x, call_y, output_y
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d: %#v", len(items), items)
	}
	if items[0]["type"] != "function_call" || items[0]["call_id"] != "call_x" {
		t.Fatalf("expected function_call call_x at 0, got %#v", items[0])
	}
	if items[1]["type"] != "function_call_output" {
		t.Fatalf("expected function_call_output at index 1, got %#v", items[1])
	}
	// Verify call_x is immediately before its output
	if items[0]["type"] != "function_call" {
		t.Fatalf("expected function_call at index 0, got %#v", items[0])
	}
	callXID, _ := items[0]["call_id"].(string)
	outputXID, _ := items[1]["call_id"].(string)
	if callXID != outputXID {
		t.Fatalf("expected call_id at index 0 to match output at index 1, got %q vs %q", callXID, outputXID)
	}
	// Verify call_y is immediately before its output
	if items[2]["type"] != "function_call" {
		t.Fatalf("expected function_call at index 2, got %#v", items[2])
	}
	if items[3]["type"] != "function_call_output" {
		t.Fatalf("expected function_call_output at index 3, got %#v", items[3])
	}
	callYID, _ := items[2]["call_id"].(string)
	outputYID, _ := items[3]["call_id"].(string)
	if callYID != outputYID {
		t.Fatalf("expected call_id at index 2 to match output at index 3, got %q vs %q", callYID, outputYID)
	}
}
