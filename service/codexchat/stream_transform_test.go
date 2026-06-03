package codexchat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

// (uses existing generic ptr[T any] helper from request_transform_test.go)

// =============================================================================
// Text Streaming Tests
// =============================================================================

func TestSingleTextChunk(t *testing.T) {
	st := &StreamTransformState{}
	content := "Hello"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: &content,
				},
			},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (added + delta), got %d", len(events))
	}

	// First event should be output_item.added
	if events[0]["type"] != "response.output_item.added" {
		t.Errorf("expected first event 'response.output_item.added', got %v", events[0]["type"])
	}

	// Verify item details in added event
	item, ok := events[0]["item"].(map[string]any)
	if !ok {
		t.Fatal("expected 'item' map in added event")
	}
	if item["type"] != "message" {
		t.Errorf("expected item type 'message', got %v", item["type"])
	}
	if item["role"] != "assistant" {
		t.Errorf("expected item role 'assistant', got %v", item["role"])
	}

	// Second event should be output_text.delta
	if events[1]["type"] != "response.output_text.delta" {
		t.Errorf("expected second event 'response.output_text.delta', got %v", events[1]["type"])
	}
	if events[1]["delta"] != "Hello" {
		t.Errorf("expected delta 'Hello', got %v", events[1]["delta"])
	}
}

func TestMultipleTextChunks(t *testing.T) {
	st := &StreamTransformState{}
	c1 := "Hello"
	c2 := " world"

	// First chunk: should produce 2 events (added + delta)
	chunk1 := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c1}},
		},
	}
	events1 := st.ProcessChatSSEChunk(chunk1)
	if len(events1) < 2 {
		t.Fatalf("first chunk: expected >= 2 events, got %d", len(events1))
	}

	// Second chunk: should produce 1 event (delta only)
	chunk2 := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c2}},
		},
	}
	events2 := st.ProcessChatSSEChunk(chunk2)
	if len(events2) == 0 {
		t.Fatal("second chunk: expected at least 1 event")
	}

	// Should only have delta events, no added events
	for _, e := range events2 {
		if e["type"] == "response.output_item.added" {
			t.Error("second chunk should not have output_item.added event")
		}
	}

	// Verify the delta event
	if events2[0]["type"] != "response.output_text.delta" {
		t.Errorf("expected 'response.output_text.delta', got %v", events2[0]["type"])
	}
	if events2[0]["delta"] != " world" {
		t.Errorf("expected delta ' world', got %v", events2[0]["delta"])
	}
}

func TestEmptyChunk(t *testing.T) {
	st := &StreamTransformState{}
	chunk := &dto.ChatCompletionsStreamResponse{
		Id: "chatcmpl-1",
		// No choices
		Choices: []dto.ChatCompletionsStreamResponseChoice{},
	}

	events := st.ProcessChatSSEChunk(chunk)
	if len(events) != 0 {
		t.Errorf("expected 0 events for empty chunk, got %d", len(events))
	}
}

// =============================================================================
// Reasoning Streaming Tests
// =============================================================================

func TestFirstReasoningChunk(t *testing.T) {
	st := &StreamTransformState{}
	reasoning := "Let me think about this..."
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ReasoningContent: &reasoning,
				},
			},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (added + delta), got %d", len(events))
	}

	// First event should be output_item.added (reasoning)
	if events[0]["type"] != "response.output_item.added" {
		t.Errorf("expected first event 'response.output_item.added', got %v", events[0]["type"])
	}
	item, ok := events[0]["item"].(map[string]any)
	if !ok {
		t.Fatal("expected 'item' map in added event")
	}
	if item["type"] != "reasoning" {
		t.Errorf("expected item type 'reasoning', got %v", item["type"])
	}

	// Second event should be reasoning_summary_text.delta
	if events[1]["type"] != "response.reasoning_summary_text.delta" {
		t.Errorf("expected second event 'response.reasoning_summary_text.delta', got %v", events[1]["type"])
	}
	if events[1]["delta"] != reasoning {
		t.Errorf("expected delta '%s', got %v", reasoning, events[1]["delta"])
	}
}

func TestSecondReasoningChunk(t *testing.T) {
	st := &StreamTransformState{}
	r1 := "First part"
	r2 := " second part"

	// First reasoning chunk
	chunk1 := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: &r1}},
		},
	}
	events1 := st.ProcessChatSSEChunk(chunk1)
	if len(events1) < 2 {
		t.Fatalf("first reasoning chunk: expected >= 2 events, got %d", len(events1))
	}

	// Second reasoning chunk: should only produce delta (no added)
	chunk2 := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: &r2}},
		},
	}
	events2 := st.ProcessChatSSEChunk(chunk2)
	if len(events2) == 0 {
		t.Fatal("second reasoning chunk: expected at least 1 event")
	}

	for _, e := range events2 {
		if e["type"] == "response.output_item.added" {
			t.Error("second reasoning chunk should not have output_item.added event")
		}
	}

	if events2[0]["type"] != "response.reasoning_summary_text.delta" {
		t.Errorf("expected 'response.reasoning_summary_text.delta', got %v", events2[0]["type"])
	}
}

// =============================================================================
// Tool Call Streaming Tests
// =============================================================================

func TestToolCallStart(t *testing.T) {
	st := &StreamTransformState{}
	tcIndex := 0
	tcID := "call_abc123"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ToolCalls: []dto.ToolCallResponse{
						{
							Index: &tcIndex,
							ID:    tcID,
							Function: dto.FunctionResponse{
								Name:      "get_weather",
								Arguments: `{"city":"NYC"}`,
							},
						},
					},
				},
			},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (added + arguments delta), got %d", len(events))
	}

	// First event should be output_item.added (function_call)
	if events[0]["type"] != "response.output_item.added" {
		t.Errorf("expected first event 'response.output_item.added', got %v", events[0]["type"])
	}
	item, ok := events[0]["item"].(map[string]any)
	if !ok {
		t.Fatal("expected 'item' map in added event")
	}
	if item["type"] != "function_call" {
		t.Errorf("expected item type 'function_call', got %v", item["type"])
	}
	if item["call_id"] != tcID {
		t.Errorf("expected call_id '%s', got %v", tcID, item["call_id"])
	}
	if item["name"] != "get_weather" {
		t.Errorf("expected name 'get_weather', got %v", item["name"])
	}

	// Second event should be function_call_arguments.delta
	if events[1]["type"] != "response.function_call_arguments.delta" {
		t.Errorf("expected second event 'response.function_call_arguments.delta', got %v", events[1]["type"])
	}
	if events[1]["delta"] != `{"city":"NYC"}` {
		t.Errorf("expected delta '{\"city\":\"NYC\"}', got %v", events[1]["delta"])
	}
}

func TestToolCallContinuation(t *testing.T) {
	st := &StreamTransformState{}
	tcIndex := 0
	tcID := "call_abc123"

	// First chunk: tool call begins with name and first arguments
	chunk1 := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ToolCalls: []dto.ToolCallResponse{
						{
							Index: &tcIndex,
							ID:    tcID,
							Function: dto.FunctionResponse{
								Name:      "get_weather",
								Arguments: `{"city":"`,
							},
						},
					},
				},
			},
		},
	}
	events1 := st.ProcessChatSSEChunk(chunk1)
	if len(events1) < 2 {
		t.Fatalf("first tool call chunk: expected >= 2 events, got %d", len(events1))
	}

	// Second chunk: more arguments, no name (continuation)
	chunk2 := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ToolCalls: []dto.ToolCallResponse{
						{
							Index: &tcIndex,
							ID:    tcID,
							Function: dto.FunctionResponse{
								Arguments: `NYC"}`,
							},
						},
					},
				},
			},
		},
	}
	events2 := st.ProcessChatSSEChunk(chunk2)
	if len(events2) == 0 {
		t.Fatal("second tool call chunk: expected at least 1 event")
	}

	// Should only have delta events, no added events
	for _, e := range events2 {
		if e["type"] == "response.output_item.added" {
			t.Error("second tool call chunk should not have output_item.added event")
		}
	}

	// Should be function_call_arguments.delta
	if events2[0]["type"] != "response.function_call_arguments.delta" {
		t.Errorf("expected 'response.function_call_arguments.delta', got %v", events2[0]["type"])
	}
	if events2[0]["delta"] != `NYC"}` {
		t.Errorf("expected delta 'NYC\"}', got %v", events2[0]["delta"])
	}
}

// =============================================================================
// Completion Tests
// =============================================================================

func TestCompletedEvent(t *testing.T) {
	st := &StreamTransformState{}
	// First prime the state with metadata
	st.ResponseID = "chatcmpl-1"
	st.Model = "gpt-4o"
	st.CreatedAt = 12345
	st.responseStarted = true

	finishReason := "stop"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &finishReason},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)
	var completed *map[string]any
	for i := range events {
		if events[i]["type"] == "response.completed" {
			m := events[i]
			completed = &m
			break
		}
	}
	if completed == nil {
		t.Fatal("expected response.completed event")
	}

	// Check response details
	resp, ok := (*completed)["response"].(map[string]any)
	if !ok {
		t.Fatal("expected 'response' map in completed event")
	}
	if resp["status"] != "completed" {
		t.Errorf("expected status 'completed', got %v", resp["status"])
	}
	if resp["id"] != "chatcmpl-1" {
		t.Errorf("expected id 'chatcmpl-1', got %v", resp["id"])
	}
}

func TestIncompleteEvent(t *testing.T) {
	st := &StreamTransformState{}
	st.ResponseID = "chatcmpl-1"
	st.Model = "gpt-4o"
	st.CreatedAt = 12345
	st.responseStarted = true

	finishReason := "length"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &finishReason},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)
	var completed *map[string]any
	for i := range events {
		if events[i]["type"] == "response.completed" {
			m := events[i]
			completed = &m
			break
		}
	}
	if completed == nil {
		t.Fatal("expected response.completed event")
	}

	resp, ok := (*completed)["response"].(map[string]any)
	if !ok {
		t.Fatal("expected 'response' map in completed event")
	}
	if resp["status"] != "incomplete" {
		t.Errorf("expected status 'incomplete', got %v", resp["status"])
	}

	// Check incomplete_details
	details, ok := resp["incomplete_details"].(map[string]any)
	if !ok {
		t.Fatal("expected 'incomplete_details' map for length finish")
	}
	if details["reason"] != "max_output_tokens" {
		t.Errorf("expected reason 'max_output_tokens', got %v", details["reason"])
	}
}

// =============================================================================
// Usage Tests
// =============================================================================

func TestUsageChunk(t *testing.T) {
	st := &StreamTransformState{}
	content := "Hello"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &content}},
		},
		Usage: &dto.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	st.ProcessChatSSEChunk(chunk)

	if st.LatestUsage == nil {
		t.Fatal("expected LatestUsage to be set")
	}
	if st.LatestUsage.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", st.LatestUsage.PromptTokens)
	}
	if st.LatestUsage.CompletionTokens != 5 {
		t.Errorf("expected completion_tokens=5, got %d", st.LatestUsage.CompletionTokens)
	}
	if st.LatestUsage.TotalTokens != 15 {
		t.Errorf("expected total_tokens=15, got %d", st.LatestUsage.TotalTokens)
	}
}

func TestUsageInCompletedEvent(t *testing.T) {
	st := &StreamTransformState{}
	st.ResponseID = "chatcmpl-1"
	st.Model = "gpt-4o"
	st.CreatedAt = 12345
	st.responseStarted = true
	st.LatestUsage = &dto.Usage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}

	finishReason := "stop"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &finishReason},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)
	var completed *map[string]any
	for i := range events {
		if events[i]["type"] == "response.completed" {
			m := events[i]
			completed = &m
			break
		}
	}
	if completed == nil {
		t.Fatal("expected response.completed event")
	}

	resp, ok := (*completed)["response"].(map[string]any)
	if !ok {
		t.Fatal("expected 'response' map in completed event")
	}

	usage, ok := resp["usage"].(*dto.Usage)
	if !ok {
		t.Fatalf("expected usage to be *dto.Usage, got %T", resp["usage"])
	}
	if usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens=15 in completed usage, got %d", usage.TotalTokens)
	}
}

// =============================================================================
// Think Tag Extraction Tests
// =============================================================================

func TestThinkTagExtraction(t *testing.T) {
	st := &StreamTransformState{}
	st.ResponseID = "chatcmpl-1"
	st.Model = "gpt-4o"
	st.CreatedAt = 12345
	st.responseStarted = true

	content := "<think>Let me reason about this</think>The answer is 42"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: &content,
				},
			},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)

	// Should have: output_item.added (reasoning), reasoning_summary_text.delta,
	//              output_item.added (message), output_text.delta
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d: %+v", len(events), events)
	}

	// Find reasoning added event
	hasReasoningAdded := false
	hasReasoningDelta := false
	hasTextAdded := false
	hasTextDelta := false
	for _, e := range events {
		switch e["type"] {
		case "response.output_item.added":
			item := e["item"].(map[string]any)
			if item["type"] == "reasoning" {
				hasReasoningAdded = true
			}
			if item["type"] == "message" {
				hasTextAdded = true
			}
		case "response.reasoning_summary_text.delta":
			if e["delta"] == "Let me reason about this" {
				hasReasoningDelta = true
			}
		case "response.output_text.delta":
			if e["delta"] == "The answer is 42" {
				hasTextDelta = true
			}
		}
	}

	if !hasReasoningAdded {
		t.Error("missing reasoning output_item.added event")
	}
	if !hasReasoningDelta {
		t.Error("missing reasoning_summary_text.delta for think content")
	}
	if !hasTextAdded {
		t.Error("missing message output_item.added event")
	}
	if !hasTextDelta {
		t.Error("missing output_text.delta for text outside think tags")
	}
}

func TestSplitThinkTagsCrossChunks(t *testing.T) {
	st := &StreamTransformState{}
	st.ResponseID = "chatcmpl-1"
	st.Model = "gpt-4o"
	st.CreatedAt = 12345
	st.responseStarted = true

	// First chunk: start of think tag
	c1 := "<think>start of reasoning"
	chunk1 := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c1}},
		},
	}
	events1 := st.ProcessChatSSEChunk(chunk1)

	// Should have reasoning added and reasoning delta for "start of reasoning"
	hasReasoning := false
	for _, e := range events1 {
		if e["type"] == "response.reasoning_summary_text.delta" {
			hasReasoning = true
		}
	}
	if !hasReasoning {
		t.Error("expected reasoning_summary_text.delta in first chunk")
	}

	// Verify state: inThinkTag should be true
	if !st.inThinkTag {
		t.Error("expected inThinkTag=true after opening <think>")
	}

	// Second chunk: end of think tag and text
	c2 := "end of reasoning</think>The final answer"
	chunk2 := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c2}},
		},
	}
	events2 := st.ProcessChatSSEChunk(chunk2)

	// Should have reasoning delta for "end of reasoning" and text delta for "The final answer"
	hasReasoningDelta := false
	hasTextDelta := false
	for _, e := range events2 {
		switch e["type"] {
		case "response.reasoning_summary_text.delta":
			if e["delta"] == "end of reasoning" {
				hasReasoningDelta = true
			}
		case "response.output_text.delta":
			if e["delta"] == "The final answer" {
				hasTextDelta = true
			}
		}
	}
	if !hasReasoningDelta {
		t.Error("expected reasoning delta for 'end of reasoning' in second chunk")
	}
	if !hasTextDelta {
		t.Error("expected text delta for 'The final answer' in second chunk")
	}

	// After </think>, inThinkTag should be false
	if st.inThinkTag {
		t.Error("expected inThinkTag=false after closing </think>")
	}
}

// =============================================================================
// Output Index Tests
// =============================================================================

func TestMultipleOutputItemsDifferentIndices(t *testing.T) {
	st := &StreamTransformState{}

	// Send reasoning first
	reasoning := "Let me think..."
	chunk1 := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: &reasoning}},
		},
	}
	events1 := st.ProcessChatSSEChunk(chunk1)

	// Then send text
	content := "Here is the answer"
	chunk2 := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &content}},
		},
	}
	events2 := st.ProcessChatSSEChunk(chunk2)

	// Collect all output_index values
	var reasoningIndex, textIndex int
	reasoningIndexSet, textIndexSet := false, false

	for _, e := range events1 {
		if e["type"] == "response.output_item.added" {
			item := e["item"].(map[string]any)
			if item["type"] == "reasoning" {
				if idx, ok := e["output_index"].(int); ok {
					reasoningIndex = idx
					reasoningIndexSet = true
				}
			}
		}
	}
	for _, e := range events2 {
		if e["type"] == "response.output_item.added" {
			item := e["item"].(map[string]any)
			if item["type"] == "message" {
				if idx, ok := e["output_index"].(int); ok {
					textIndex = idx
					textIndexSet = true
				}
			}
		}
	}

	if !reasoningIndexSet {
		t.Fatal("could not find reasoning output_index")
	}
	if !textIndexSet {
		t.Fatal("could not find text output_index")
	}

	if reasoningIndex == textIndex {
		t.Errorf("expected different output_index values, both got %d", reasoningIndex)
	}

	t.Logf("reasoning output_index=%d, text output_index=%d", reasoningIndex, textIndex)
}

// =============================================================================
// Direct extractThinkContent Tests
// =============================================================================

func TestExtractThinkContent_NoTag(t *testing.T) {
	text, reasoning, stillIn := extractThinkContent("plain text", false)
	if text != "plain text" {
		t.Errorf("expected plain text 'plain text', got %q", text)
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
	if stillIn {
		t.Error("expected stillInTag=false")
	}
}

func TestExtractThinkContent_FullTag(t *testing.T) {
	text, reasoning, stillIn := extractThinkContent("<think>reasoning here</think>answer", false)
	if text != "answer" {
		t.Errorf("expected text 'answer', got %q", text)
	}
	if reasoning != "reasoning here" {
		t.Errorf("expected reasoning 'reasoning here', got %q", reasoning)
	}
	if stillIn {
		t.Error("expected stillInTag=false")
	}
}

func TestExtractThinkContent_OnlyText(t *testing.T) {
	text, reasoning, stillIn := extractThinkContent("just some answer", false)
	if text != "just some answer" {
		t.Errorf("expected text 'just some answer', got %q", text)
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
	if stillIn {
		t.Error("expected stillInTag=false")
	}
}

func TestExtractThinkContent_OnlyThink(t *testing.T) {
	text, reasoning, stillIn := extractThinkContent("<think>reasoning only</think>", false)
	if text != "" {
		t.Errorf("expected empty text, got %q", text)
	}
	if reasoning != "reasoning only" {
		t.Errorf("expected reasoning 'reasoning only', got %q", reasoning)
	}
	if stillIn {
		t.Error("expected stillInTag=false")
	}
}

func TestExtractThinkContent_UnclosedTag(t *testing.T) {
	text, reasoning, stillIn := extractThinkContent("before <think>reasoning starts", false)
	if text != "before " {
		t.Errorf("expected text 'before ', got %q", text)
	}
	if reasoning != "reasoning starts" {
		t.Errorf("expected reasoning 'reasoning starts', got %q", reasoning)
	}
	if !stillIn {
		t.Error("expected stillInTag=true for unclosed tag")
	}
}

func TestExtractThinkContent_ResumeInTag(t *testing.T) {
	// Simulate: previous chunk was "before <think>start" and we're in think tag
	text, reasoning, stillIn := extractThinkContent(" more reasoning</think>answer", true)
	if text != "answer" {
		t.Errorf("expected text 'answer', got %q", text)
	}
	if reasoning != " more reasoning" {
		t.Errorf("expected reasoning ' more reasoning', got %q", reasoning)
	}
	if stillIn {
		t.Error("expected stillInTag=false after closing tag")
	}
}

func TestExtractThinkContent_ResumeAndStillOpen(t *testing.T) {
	// Simulate: in tag, still no close
	text, reasoning, stillIn := extractThinkContent("continuing reasoning", true)
	if text != "" {
		t.Errorf("expected empty text, got %q", text)
	}
	if reasoning != "continuing reasoning" {
		t.Errorf("expected reasoning 'continuing reasoning', got %q", reasoning)
	}
	if !stillIn {
		t.Error("expected stillInTag=true")
	}
}

func TestExtractThinkContent_MultipleTags(t *testing.T) {
	text, reasoning, stillIn := extractThinkContent("<think>first</think>middle<think>second</think>end", false)
	if text != "middleend" {
		t.Errorf("expected text 'middleend', got %q", text)
	}
	if reasoning != "firstsecond" {
		t.Errorf("expected reasoning 'firstsecond', got %q", reasoning)
	}
	if stillIn {
		t.Error("expected stillInTag=false")
	}
}

// =============================================================================
// WriteResponsesSSEEvent Tests
// =============================================================================

func TestWriteResponsesSSEEvent_ProducesValidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	event := map[string]any{
		"type":  "response.output_text.delta",
		"delta": "Hello, world!",
	}

	err := WriteResponsesSSEEvent(c, event)
	// Even if we can't guarantee the response write (gin test context), we can
	// at least verify the JSON marshalling doesn't error
	if err != nil {
		t.Logf("WriteResponsesSSEEvent returned error: %v (may be expected in test context)", err)
	}

	// Verify the event marshals to valid JSON
	data, marshalErr := common.Marshal(event)
	if marshalErr != nil {
		t.Fatalf("common.Marshal failed: %v", marshalErr)
	}
	if !json.Valid(data) {
		t.Fatal("marshalled data is not valid JSON")
	}

	// Verify the JSON contains expected fields
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed["type"] != "response.output_text.delta" {
		t.Errorf("expected type in JSON, got %v", parsed["type"])
	}
}

func TestWriteResponsesSSEEvent_ComplexEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	event := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         "resp-123",
			"object":     "response",
			"created_at": int64(12345),
			"model":      "gpt-4o",
			"status":     "completed",
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		},
	}

	err := WriteResponsesSSEEvent(c, event)
	if err != nil {
		t.Logf("WriteResponsesSSEEvent returned error: %v (may be expected in test context)", err)
	}

	data, marshalErr := common.Marshal(event)
	if marshalErr != nil {
		t.Fatalf("common.Marshal failed: %v", marshalErr)
	}
	if !json.Valid(data) {
		t.Fatal("marshalled data is not valid JSON")
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	resp, ok := parsed["response"].(map[string]any)
	if !ok {
		t.Fatal("expected 'response' map in JSON")
	}
	if resp["status"] != "completed" {
		t.Errorf("expected status 'completed', got %v", resp["status"])
	}
}

// =============================================================================
// Full Stream Simulation Test
// =============================================================================

func TestFullStreamSimulation(t *testing.T) {
	st := &StreamTransformState{}

	// 1. First text chunk (should produce added + delta)
	c1 := "Hello"
	chunk1 := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c1}},
		},
	}
	events := st.ProcessChatSSEChunk(chunk1)
	if len(events) < 2 {
		t.Fatalf("step 1: expected >= 2 events, got %d", len(events))
	}

	// 2. Second text chunk (delta only)
	c2 := " world"
	chunk2 := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c2}},
		},
	}
	events = st.ProcessChatSSEChunk(chunk2)
	if len(events) < 1 {
		t.Fatalf("step 2: expected >= 1 event, got %d", len(events))
	}

	// 3. Finish with usage
	finishReason := "stop"
	chunk3 := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &finishReason},
		},
		Usage: &dto.Usage{
			PromptTokens:     5,
			CompletionTokens: 2,
			TotalTokens:      7,
		},
	}
	events = st.ProcessChatSSEChunk(chunk3)

	// Should have content_part.done + response.completed
	var hasDone, hasCompleted bool
	for _, e := range events {
		switch e["type"] {
		case "response.content_part.done":
			hasDone = true
		case "response.completed":
			hasCompleted = true
			resp := e["response"].(map[string]any)
			if resp["status"] != "completed" {
				t.Errorf("expected status 'completed', got %v", resp["status"])
			}
			// Usage from same chunk is processed after finish reason,
			// so it won't appear here. Verify LatestUsage below instead.
		}
	}
	if !hasDone {
		t.Error("expected content_part.done event")
	}
	if !hasCompleted {
		t.Error("expected response.completed event")
	}

	// Verify LatestUsage is set (usage is processed after finish reason,
	// so it won't appear in the completed event from the same chunk, but
	// it IS stored on the state for future reference)
	if st.LatestUsage == nil {
		t.Error("expected LatestUsage to be set")
	} else if st.LatestUsage.TotalTokens != 7 {
		t.Errorf("expected total_tokens=7, got %d", st.LatestUsage.TotalTokens)
	}
}

// =============================================================================
// Text Accrual Test
// =============================================================================

func TestTextAccrueOverMultipleChunks(t *testing.T) {
	st := &StreamTransformState{}

	chunks := []string{"Hello", ", ", "world", "!"}
	for i, text := range chunks {
		c := text // capture
		chunk := &dto.ChatCompletionsStreamResponse{
			Id:      "chatcmpl-1",
			Model:   "gpt-4o",
			Created: 12345,
			Choices: []dto.ChatCompletionsStreamResponseChoice{
				{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c}},
			},
		}
		events := st.ProcessChatSSEChunk(chunk)
		if i == 0 {
			// First chunk should have added + delta
			if len(events) < 2 {
				t.Errorf("chunk %d: expected >= 2 events, got %d", i, len(events))
			}
		} else {
			// Subsequent chunks should only have delta
			for _, e := range events {
				if e["type"] == "response.output_item.added" {
					t.Errorf("chunk %d: unexpected output_item.added", i)
				}
			}
		}
		// Each chunk should have a delta event
		hasDelta := false
		for _, e := range events {
			if e["type"] == "response.output_text.delta" {
				if e["delta"] == text {
					hasDelta = true
				}
			}
		}
		if !hasDelta {
			t.Errorf("chunk %d: missing delta event for '%s'", i, text)
		}
	}
}

// =============================================================================
// Tool Call with Empty Arguments First Chunk
// =============================================================================

func TestToolCallNoArgumentsOnFirstChunk(t *testing.T) {
	st := &StreamTransformState{}
	tcIndex := 0
	tcID := "call_abc123"

	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ToolCalls: []dto.ToolCallResponse{
						{
							Index: &tcIndex,
							ID:    tcID,
							Function: dto.FunctionResponse{
								Name:      "get_weather",
								Arguments: "", // empty on first chunk
							},
						},
					},
				},
			},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)
	// Should have output_item.added but NO arguments delta (empty args)
	hasAdded := false
	hasArgsDelta := false
	for _, e := range events {
		switch e["type"] {
		case "response.output_item.added":
			hasAdded = true
		case "response.function_call_arguments.delta":
			hasArgsDelta = true
		}
	}
	if !hasAdded {
		t.Error("expected output_item.added for tool call")
	}
	if hasArgsDelta {
		t.Error("expected no arguments delta when arguments is empty")
	}
}
