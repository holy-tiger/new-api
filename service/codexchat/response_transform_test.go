package codexchat

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// --- Plain text response ---

func TestPlainTextToResponse(t *testing.T) {
	usage := dto.Usage{TotalTokens: 100, PromptTokens: 50, CompletionTokens: 50}
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:    "assistant",
					Content: "Hello, how can I help?",
				},
			},
		},
		Usage: usage,
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !strings.HasPrefix(resp.ID, "resp_") {
		t.Errorf("expected ID prefix 'resp_', got %q", resp.ID)
	}
	if resp.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", resp.Model)
	}
	if resp.Object != "response" {
		t.Errorf("expected object 'response', got %q", resp.Object)
	}
	if resp.CreatedAt <= 0 {
		t.Errorf("expected positive created_at, got %d", resp.CreatedAt)
	}

	// Check status is "completed"
	var status string
	if err := common.Unmarshal(resp.Status, &status); err != nil || status != "completed" {
		t.Errorf("expected status 'completed', got %s (err=%v)", string(resp.Status), err)
	}

	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}

	out := resp.Output[0]
	if out.Type != "message" {
		t.Errorf("expected output type 'message', got %q", out.Type)
	}
	if out.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", out.Role)
	}
	if len(out.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(out.Content))
	}
	if out.Content[0].Type != "output_text" {
		t.Errorf("expected content type 'output_text', got %q", out.Content[0].Type)
	}
	if out.Content[0].Text != "Hello, how can I help?" {
		t.Errorf("expected text 'Hello, how can I help?', got %q", out.Content[0].Text)
	}

	// Usage
	if resp.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if resp.Usage.TotalTokens != 100 {
		t.Errorf("expected total_tokens 100, got %d", resp.Usage.TotalTokens)
	}
	if resp.Usage.PromptTokens != 50 {
		t.Errorf("expected prompt_tokens 50, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 50 {
		t.Errorf("expected completion_tokens 50, got %d", resp.Usage.CompletionTokens)
	}
}

// --- Model and ID ---

func TestModelAndIDPreserved(t *testing.T) {
	chatResp := &dto.OpenAITextResponse{
		Model: "claude-3-opus",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:    "assistant",
					Content: "Hi",
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Model != "claude-3-opus" {
		t.Errorf("expected model 'claude-3-opus', got %q", resp.Model)
	}
	if !strings.HasPrefix(resp.ID, "resp_") {
		t.Errorf("expected ID prefix 'resp_', got %q", resp.ID)
	}
	// ID should be 29 chars ("resp_" + 24 hex)
	if len(resp.ID) != 29 {
		t.Errorf("expected ID length 29, got %d (%q)", len(resp.ID), resp.ID)
	}
}

// --- Tool calls ---

func TestToolCallsToFunctionCall(t *testing.T) {
	tcs := []dto.ToolCallResponse{
		{
			ID:   "call-1",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "get_weather",
				Arguments: `{"city":"NYC"}`,
			},
		},
	}
	tcJSON, err := common.Marshal(tcs)
	if err != nil {
		t.Fatalf("failed to marshal tool calls: %v", err)
	}

	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "tool_calls",
				Message: dto.Message{
					Role:      "assistant",
					ToolCalls: tcJSON,
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Find function_call output items
	var fcItems []dto.ResponsesOutput
	for _, out := range resp.Output {
		if out.Type == "function_call" {
			fcItems = append(fcItems, out)
		}
	}
	if len(fcItems) != 1 {
		t.Fatalf("expected 1 function_call output, got %d", len(fcItems))
	}

	fc := fcItems[0]
	if fc.ID != "call-1" {
		t.Errorf("expected ID 'call-1', got %q", fc.ID)
	}
	if fc.CallId != "call-1" {
		t.Errorf("expected CallId 'call-1', got %q", fc.CallId)
	}
	if fc.Name != "get_weather" {
		t.Errorf("expected name 'get_weather', got %q", fc.Name)
	}
	if fc.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", fc.Status)
	}

	// Verify arguments
	argsStr := dto.ResponsesArgumentsString(fc.Arguments)
	if argsStr != `{"city":"NYC"}` {
		t.Errorf("expected arguments '{\"city\":\"NYC\"}', got %q", argsStr)
	}
}

// --- Finish reason: length ---

func TestFinishReasonLength(t *testing.T) {
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "length",
				Message: dto.Message{
					Role:    "assistant",
					Content: "truncated...",
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	var status string
	if err := common.Unmarshal(resp.Status, &status); err != nil || status != "incomplete" {
		t.Errorf("expected status 'incomplete', got %s (err=%v)", string(resp.Status), err)
	}

	if resp.IncompleteDetails == nil {
		t.Fatal("expected non-nil IncompleteDetails")
	}
	if resp.IncompleteDetails.Reasoning != "max_output_tokens" {
		t.Errorf("expected IncompleteDetails.Reasoning 'max_output_tokens', got %q", resp.IncompleteDetails.Reasoning)
	}

	// Output should still contain the message
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
}

// --- Finish reason: stop ---

func TestFinishReasonStop(t *testing.T) {
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:    "assistant",
					Content: "done",
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	var status string
	if err := common.Unmarshal(resp.Status, &status); err != nil || status != "completed" {
		t.Errorf("expected status 'completed', got %s (err=%v)", string(resp.Status), err)
	}

	if resp.IncompleteDetails != nil {
		t.Errorf("expected nil IncompleteDetails for stop, got %+v", resp.IncompleteDetails)
	}
}

// --- Reasoning content ---

func TestReasoningContent(t *testing.T) {
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:             "assistant",
					Content:          "The answer is 42.",
					ReasoningContent: "Let me think about this carefully...",
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Find reasoning output items
	var reasoningItems []dto.ResponsesOutput
	for _, out := range resp.Output {
		if out.Type == "reasoning" {
			reasoningItems = append(reasoningItems, out)
		}
	}
	if len(reasoningItems) != 1 {
		t.Fatalf("expected 1 reasoning output, got %d", len(reasoningItems))
	}

	ri := reasoningItems[0]
	if len(ri.Content) != 1 {
		t.Fatalf("expected 1 content part in reasoning, got %d", len(ri.Content))
	}
	if ri.Content[0].Type != "summary_text" {
		t.Errorf("expected content type 'summary_text', got %q", ri.Content[0].Type)
	}
	if ri.Content[0].Text != "Let me think about this carefully..." {
		t.Errorf("expected reasoning text preserved, got %q", ri.Content[0].Text)
	}

	// Should also have the regular message output
	var msgItems []dto.ResponsesOutput
	for _, out := range resp.Output {
		if out.Type == "message" {
			msgItems = append(msgItems, out)
		}
	}
	if len(msgItems) != 1 {
		t.Fatalf("expected 1 message output, got %d", len(msgItems))
	}
}

// --- Usage fields ---

func TestUsageFields(t *testing.T) {
	usage := dto.Usage{
		TotalTokens:          200,
		PromptTokens:         80,
		CompletionTokens:     120,
		PromptCacheHitTokens: 30,
	}
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:    "assistant",
					Content: "ok",
				},
			},
		},
		Usage: usage,
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if resp.Usage.TotalTokens != 200 {
		t.Errorf("expected total_tokens 200, got %d", resp.Usage.TotalTokens)
	}
	if resp.Usage.PromptTokens != 80 {
		t.Errorf("expected prompt_tokens 80, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 120 {
		t.Errorf("expected completion_tokens 120, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.PromptCacheHitTokens != 30 {
		t.Errorf("expected prompt_cache_hit_tokens 30, got %d", resp.Usage.PromptCacheHitTokens)
	}
}

// --- Empty choices ---

func TestEmptyChoices(t *testing.T) {
	usage := dto.Usage{TotalTokens: 10, PromptTokens: 5, CompletionTokens: 5}
	chatResp := &dto.OpenAITextResponse{
		Model:   "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{},
		Usage:   usage,
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", resp.Model)
	}
	if len(resp.Output) != 0 {
		t.Errorf("expected empty output, got %d items", len(resp.Output))
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if resp.Usage.TotalTokens != 10 {
		t.Errorf("expected total_tokens 10, got %d", resp.Usage.TotalTokens)
	}
}

// --- Nil chat response ---

func TestNilChatResponse(t *testing.T) {
	resp := ChatCompletionsResponseToResponsesResponse(nil, nil)
	if resp != nil {
		t.Errorf("expected nil response for nil input, got %+v", resp)
	}
}

// --- Multiple tool calls ---

func TestMultipleToolCalls(t *testing.T) {
	tcs := []dto.ToolCallResponse{
		{
			ID:   "call-1",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "get_weather",
				Arguments: `{"city":"NYC"}`,
			},
		},
		{
			ID:   "call-2",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "get_time",
				Arguments: `{"timezone":"UTC"}`,
			},
		},
		{
			ID:   "call-3",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "search",
				Arguments: `{"query":"hello"}`,
			},
		},
	}
	tcJSON, err := common.Marshal(tcs)
	if err != nil {
		t.Fatalf("failed to marshal tool calls: %v", err)
	}

	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "tool_calls",
				Message: dto.Message{
					Role:      "assistant",
					ToolCalls: tcJSON,
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Count function_call output items
	var fcItems []dto.ResponsesOutput
	for _, out := range resp.Output {
		if out.Type == "function_call" {
			fcItems = append(fcItems, out)
		}
	}
	if len(fcItems) != 3 {
		t.Fatalf("expected 3 function_call outputs, got %d", len(fcItems))
	}

	// Verify each
	expectedNames := []string{"get_weather", "get_time", "search"}
	expectedArgs := []string{`{"city":"NYC"}`, `{"timezone":"UTC"}`, `{"query":"hello"}`}
	for i, fc := range fcItems {
		if fc.Name != expectedNames[i] {
			t.Errorf("function_call[%d]: expected name %q, got %q", i, expectedNames[i], fc.Name)
		}
		if dto.ResponsesArgumentsString(fc.Arguments) != expectedArgs[i] {
			t.Errorf("function_call[%d]: expected args %q, got %q", i, expectedArgs[i], dto.ResponsesArgumentsString(fc.Arguments))
		}
		if fc.Status != "completed" {
			t.Errorf("function_call[%d]: expected status 'completed', got %q", i, fc.Status)
		}
		if fc.CallId != fc.ID {
			t.Errorf("function_call[%d]: expected CallId to match ID, got ID=%q CallId=%q", i, fc.ID, fc.CallId)
		}
	}
}

// --- Message with content as array (multi-modal content) ---

func TestArrayContentToResponse(t *testing.T) {
	content := []any{
		map[string]any{
			"type": "text",
			"text": "Here is the image description.",
		},
	}
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:    "assistant",
					Content: content,
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Content[0].Text != "Here is the image description." {
		t.Errorf("expected text from array content, got %q", resp.Output[0].Content[0].Text)
	}
}

// --- No message content (only tool calls, no text) ---

func TestToolCallsOnlyNoContent(t *testing.T) {
	tcs := []dto.ToolCallResponse{
		{
			ID:   "call-abc",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "do_stuff",
				Arguments: `{}`,
			},
		},
	}
	tcJSON, _ := common.Marshal(tcs)

	// Content is explicitly nil — no text message should be produced
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "tool_calls",
				Message: dto.Message{
					Role:      "assistant",
					Content:   nil,
					ToolCalls: tcJSON,
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Should only have function_call, no message output
	var msgItems []dto.ResponsesOutput
	var fcItems []dto.ResponsesOutput
	for _, out := range resp.Output {
		switch out.Type {
		case "message":
			msgItems = append(msgItems, out)
		case "function_call":
			fcItems = append(fcItems, out)
		}
	}
	if len(msgItems) != 0 {
		t.Errorf("expected 0 message outputs when content is nil, got %d", len(msgItems))
	}
	if len(fcItems) != 1 {
		t.Fatalf("expected 1 function_call output, got %d", len(fcItems))
	}
	if fcItems[0].Name != "do_stuff" {
		t.Errorf("expected name 'do_stuff', got %q", fcItems[0].Name)
	}
}

// --- Usage nil (no usage provided) ---

func TestNilUsage(t *testing.T) {
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:    "assistant",
					Content: "no usage info",
				},
			},
		},
		// Usage is zero-value (empty struct), which means all token fields are 0
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil usage pointer")
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("expected total_tokens 0, got %d", resp.Usage.TotalTokens)
	}
}

// --- Finish reason: content_filter ---

func TestFinishReasonContentFilter(t *testing.T) {
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "content_filter",
				Message: dto.Message{
					Role:    "assistant",
					Content: "I cannot answer that.",
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Not "length" and not "stop" — should default to "completed"
	var status string
	if err := common.Unmarshal(resp.Status, &status); err != nil || status != "completed" {
		t.Errorf("expected status 'completed' for content_filter, got %s (err=%v)", string(resp.Status), err)
	}
	if resp.IncompleteDetails != nil {
		t.Errorf("expected nil IncompleteDetails for content_filter, got %+v", resp.IncompleteDetails)
	}
}

// --- ID is unique per call ---

func TestIDUniquePerCall(t *testing.T) {
	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "stop",
				Message: dto.Message{
					Role:    "assistant",
					Content: "test",
				},
			},
		},
	}

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		if ids[resp.ID] {
			t.Errorf("duplicate ID found: %q", resp.ID)
		}
		ids[resp.ID] = true
	}
}

// --- Tool type restoration from ChatToolContext ---

func TestChatResponseRestoresCustomToolType(t *testing.T) {
	tcs := []dto.ToolCallResponse{
		{
			ID:   "call-1",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "my_tool",
				Arguments: `{"input":"scan the repo"}`,
			},
		},
	}
	tcJSON, err := common.Marshal(tcs)
	if err != nil {
		t.Fatalf("failed to marshal tool calls: %v", err)
	}

	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "tool_calls",
				Message: dto.Message{
					Role:      "assistant",
					ToolCalls: tcJSON,
				},
			},
		},
	}

	toolCtx := &ChatToolContext{
		chatNameToSpec: map[string]ChatToolSpec{
			"my_tool": {
				Kind:     ChatToolKindCustom,
				Name:     "my_tool",
				ChatName: "my_tool",
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, toolCtx)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	var fcItems []dto.ResponsesOutput
	for _, out := range resp.Output {
		if out.Type == "custom_tool_call" {
			fcItems = append(fcItems, out)
		}
	}
	if len(fcItems) != 1 {
		t.Fatalf("expected 1 custom_tool_call output, got %d (output types: %v)", len(fcItems), outputTypes(resp.Output))
	}
	if fcItems[0].Name != "my_tool" {
		t.Errorf("expected name 'my_tool', got %q", fcItems[0].Name)
	}
	if string(fcItems[0].Input) != `"scan the repo"` {
		t.Errorf("expected input %q, got %s", `"scan the repo"`, string(fcItems[0].Input))
	}
}

func TestChatResponseRestoresToolSearchCallType(t *testing.T) {
	tcs := []dto.ToolCallResponse{
		{
			ID:   "call-tool-search-1",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "tool_search",
				Arguments: `{"query":"gmail search emails","limit":10}`,
			},
		},
	}
	tcJSON, err := common.Marshal(tcs)
	if err != nil {
		t.Fatalf("failed to marshal tool calls: %v", err)
	}

	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "tool_calls",
				Message: dto.Message{
					Role:      "assistant",
					ToolCalls: tcJSON,
				},
			},
		},
	}

	toolCtx := &ChatToolContext{
		chatNameToSpec: map[string]ChatToolSpec{
			"tool_search": {
				Kind:     ChatToolKindToolSearch,
				Name:     "tool_search",
				ChatName: "tool_search",
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, toolCtx)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "tool_search_call" {
		t.Fatalf("expected tool_search_call, got %q", resp.Output[0].Type)
	}
	var args map[string]any
	if err := common.Unmarshal(resp.Output[0].Arguments, &args); err != nil {
		t.Fatalf("expected tool_search_call arguments to be valid JSON object, got %s (err=%v)", string(resp.Output[0].Arguments), err)
	}
	if args["query"] != "gmail search emails" {
		t.Fatalf("expected query to round-trip, got %#v", args)
	}
	if args["limit"] != float64(10) {
		t.Fatalf("expected limit to round-trip, got %#v", args)
	}
}

func TestChatResponseRestoresNamespaceFunctionCallMetadata(t *testing.T) {
	tcs := []dto.ToolCallResponse{
		{
			ID:   "call-ns-1",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "mcp__codex_apps__gmail__search_threads",
				Arguments: `{"query":"inbox"}`,
			},
		},
	}
	tcJSON, err := common.Marshal(tcs)
	if err != nil {
		t.Fatalf("failed to marshal tool calls: %v", err)
	}

	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "tool_calls",
				Message: dto.Message{
					Role:      "assistant",
					ToolCalls: tcJSON,
				},
			},
		},
	}

	toolCtx := &ChatToolContext{
		chatNameToSpec: map[string]ChatToolSpec{
			"mcp__codex_apps__gmail__search_threads": {
				Kind:      ChatToolKindNamespace,
				Name:      "search_threads",
				Namespace: "mcp__codex_apps__gmail",
				ChatName:  "mcp__codex_apps__gmail__search_threads",
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, toolCtx)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "function_call" {
		t.Fatalf("expected function_call, got %q", resp.Output[0].Type)
	}
	if resp.Output[0].Name != "search_threads" {
		t.Fatalf("expected restored name search_threads, got %q", resp.Output[0].Name)
	}
	if resp.Output[0].Namespace != "mcp__codex_apps__gmail" {
		t.Fatalf("expected restored namespace, got %q", resp.Output[0].Namespace)
	}
	if string(resp.Output[0].Arguments) != `"{\"query\":\"inbox\"}"` {
		t.Fatalf("expected namespace function_call arguments to remain stringified JSON, got %s", string(resp.Output[0].Arguments))
	}
}

func TestChatResponseWithoutToolCtxDefaultsToFunctionCall(t *testing.T) {
	tcs := []dto.ToolCallResponse{
		{
			ID:   "call-1",
			Type: "function",
			Function: dto.FunctionResponse{
				Name:      "get_weather",
				Arguments: `{}`,
			},
		},
	}
	tcJSON, err := common.Marshal(tcs)
	if err != nil {
		t.Fatalf("failed to marshal tool calls: %v", err)
	}

	chatResp := &dto.OpenAITextResponse{
		Model: "gpt-4o",
		Choices: []dto.OpenAITextResponseChoice{
			{
				FinishReason: "tool_calls",
				Message: dto.Message{
					Role:      "assistant",
					ToolCalls: tcJSON,
				},
			},
		},
	}

	resp := ChatCompletionsResponseToResponsesResponse(chatResp, nil)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	var fcItems []dto.ResponsesOutput
	for _, out := range resp.Output {
		if out.Type == "function_call" {
			fcItems = append(fcItems, out)
		}
	}
	if len(fcItems) != 1 {
		t.Fatalf("expected 1 function_call output, got %d", len(fcItems))
	}
}

func outputTypes(outputs []dto.ResponsesOutput) []string {
	var types []string
	for _, o := range outputs {
		types = append(types, o.Type)
	}
	return types
}
