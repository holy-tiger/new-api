package codexchat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if len(events) < 5 {
		t.Fatalf("expected at least 5 events (created + in_progress + added + part added + delta), got %d", len(events))
	}

	// First event should be response.created
	if events[0]["type"] != "response.created" {
		t.Errorf("expected first event 'response.created', got %v", events[0]["type"])
	}
	createdResp, ok := events[0]["response"].(map[string]any)
	if !ok {
		t.Fatal("expected response.created to include response object")
	}
	if id, _ := createdResp["id"].(string); !strings.HasPrefix(id, "resp_") {
		t.Fatalf("expected Responses-style id with resp_ prefix, got %v", createdResp["id"])
	}

	if events[1]["type"] != "response.in_progress" {
		t.Fatalf("expected second event 'response.in_progress', got %v", events[1]["type"])
	}

	// Third event should be output_item.added
	if events[2]["type"] != "response.output_item.added" {
		t.Fatalf("expected third event 'response.output_item.added', got %v", events[2]["type"])
	}

	// Verify item details in added event
	item, ok := events[2]["item"].(map[string]any)
	if !ok {
		t.Fatal("expected 'item' map in added event")
	}
	if item["type"] != "message" {
		t.Errorf("expected item type 'message', got %v", item["type"])
	}
	if item["role"] != "assistant" {
		t.Errorf("expected item role 'assistant', got %v", item["role"])
	}
	if content, ok := item["content"].([]any); !ok || len(content) != 0 {
		t.Fatalf("expected message added item to include empty content array, got %#v", item["content"])
	}

	if events[3]["type"] != "response.content_part.added" {
		t.Fatalf("expected fourth event 'response.content_part.added', got %v", events[3]["type"])
	}

	// Fifth event should be output_text.delta
	if events[4]["type"] != "response.output_text.delta" {
		t.Errorf("expected fifth event 'response.output_text.delta', got %v", events[4]["type"])
	}
	if events[4]["delta"] != "Hello" {
		t.Errorf("expected delta 'Hello', got %v", events[4]["delta"])
	}
}

func TestSingleTextChunk_EmitsResponseCreatedAndContentPartContext(t *testing.T) {
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
	if len(events) < 5 {
		t.Fatalf("expected at least 5 events (created + in_progress + item added + part added + delta), got %d", len(events))
	}

	if events[0]["type"] != "response.created" {
		t.Fatalf("expected first event response.created, got %v", events[0]["type"])
	}
	if events[1]["type"] != "response.in_progress" {
		t.Fatalf("expected second event response.in_progress, got %v", events[1]["type"])
	}
	if events[2]["type"] != "response.output_item.added" {
		t.Fatalf("expected third event response.output_item.added, got %v", events[2]["type"])
	}
	if events[3]["type"] != "response.content_part.added" {
		t.Fatalf("expected fourth event response.content_part.added, got %v", events[3]["type"])
	}
	if events[4]["type"] != "response.output_text.delta" {
		t.Fatalf("expected fifth event response.output_text.delta, got %v", events[4]["type"])
	}

	if events[4]["content_index"] != 0 {
		t.Fatalf("expected output_text.delta content_index=0, got %v", events[4]["content_index"])
	}
}

func TestTextStream_UsesStableOutputIndexForSameMessage(t *testing.T) {
	st := &StreamTransformState{}
	c1 := "Hello"
	c2 := " world"

	events1 := st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c1}},
		},
	})
	events2 := st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &c2}},
		},
	})

	var firstDelta map[string]any
	for _, e := range events1 {
		if e["type"] == "response.output_text.delta" {
			firstDelta = e
			break
		}
	}
	if firstDelta == nil {
		t.Fatal("expected first chunk to contain response.output_text.delta")
	}
	if len(events2) == 0 {
		t.Fatal("expected second chunk to emit at least one event")
	}
	if events2[0]["type"] != "response.output_text.delta" {
		t.Fatalf("expected second chunk first event response.output_text.delta, got %v", events2[0]["type"])
	}

	if firstDelta["output_index"] != events2[0]["output_index"] {
		t.Fatalf("expected stable output_index across text deltas, got %v then %v", firstDelta["output_index"], events2[0]["output_index"])
	}
	if events2[0]["content_index"] != 0 {
		t.Fatalf("expected second delta content_index=0, got %v", events2[0]["content_index"])
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

func TestSuppressReasoningOutputHidesReasoningEvents(t *testing.T) {
	st := &StreamTransformState{SuppressReasoningOutput: true}
	reasoning := "The review is complete. Let me now write the final summary."
	content := "Findings first."
	finishReason := "stop"

	events := st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "deepseek-v4-pro",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ReasoningContent: &reasoning,
					Content:          &content,
				},
			},
		},
	})
	events = append(events, st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-1",
		Model:   "deepseek-v4-pro",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index:        0,
				FinishReason: &finishReason,
			},
		},
	})...)
	events = append(events, st.Finalize()...)

	for _, event := range events {
		eventType, _ := event["type"].(string)
		if strings.HasPrefix(eventType, "response.reasoning_") {
			t.Fatalf("expected no visible reasoning events, got %#v", event)
		}
		if eventType == "response.output_item.added" {
			item, _ := event["item"].(map[string]any)
			if item["type"] == "reasoning" {
				t.Fatalf("expected no reasoning output item, got %#v", item)
			}
		}
	}

	completed := events[len(events)-1]
	if completed["type"] != "response.completed" {
		t.Fatalf("expected final event response.completed, got %v", completed["type"])
	}
	response := completed["response"].(map[string]any)
	output := response["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected only visible message output, got %#v", output)
	}
	item := output[0].(map[string]any)
	if item["type"] != "message" {
		t.Fatalf("expected message output, got %#v", item)
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
	if len(events) < 5 {
		t.Fatalf("expected at least 5 events (created + in_progress + added + summary part + delta), got %d", len(events))
	}

	if events[0]["type"] != "response.created" {
		t.Errorf("expected first event 'response.created', got %v", events[0]["type"])
	}

	if events[1]["type"] != "response.in_progress" {
		t.Errorf("expected second event 'response.in_progress', got %v", events[1]["type"])
	}
	if events[2]["type"] != "response.output_item.added" {
		t.Errorf("expected third event 'response.output_item.added', got %v", events[2]["type"])
	}
	item, ok := events[2]["item"].(map[string]any)
	if !ok {
		t.Fatal("expected 'item' map in added event")
	}
	if item["type"] != "reasoning" {
		t.Errorf("expected item type 'reasoning', got %v", item["type"])
	}

	if events[3]["type"] != "response.reasoning_summary_part.added" {
		t.Errorf("expected fourth event 'response.reasoning_summary_part.added', got %v", events[3]["type"])
	}
	// Fifth event should be reasoning_summary_text.delta
	if events[4]["type"] != "response.reasoning_summary_text.delta" {
		t.Errorf("expected fifth event 'response.reasoning_summary_text.delta', got %v", events[4]["type"])
	}
	if events[4]["delta"] != reasoning {
		t.Errorf("expected delta '%s', got %v", reasoning, events[4]["delta"])
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
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events (created + in_progress + added + arguments delta), got %d", len(events))
	}

	if events[0]["type"] != "response.created" {
		t.Errorf("expected first event 'response.created', got %v", events[0]["type"])
	}

	if events[1]["type"] != "response.in_progress" {
		t.Errorf("expected second event 'response.in_progress', got %v", events[1]["type"])
	}
	// Third event should be output_item.added (function_call)
	if events[2]["type"] != "response.output_item.added" {
		t.Errorf("expected third event 'response.output_item.added', got %v", events[2]["type"])
	}
	item, ok := events[2]["item"].(map[string]any)
	if !ok {
		t.Fatal("expected 'item' map in added event")
	}
	if item["type"] != "function_call" {
		t.Errorf("expected item type 'function_call', got %v", item["type"])
	}
	if item["call_id"] != tcID {
		t.Errorf("expected call_id '%s', got %v", tcID, item["call_id"])
	}
	if item["id"] != "fc_"+tcID {
		t.Errorf("expected deterministic function item id %q, got %v", "fc_"+tcID, item["id"])
	}
	if item["arguments"] != "" {
		t.Errorf("expected added function_call arguments to start as empty string, got %#v", item["arguments"])
	}
	if item["name"] != "get_weather" {
		t.Errorf("expected name 'get_weather', got %v", item["name"])
	}

	// Fourth event should be function_call_arguments.delta
	if events[3]["type"] != "response.function_call_arguments.delta" {
		t.Errorf("expected fourth event 'response.function_call_arguments.delta', got %v", events[3]["type"])
	}
	if events[3]["delta"] != `{"city":"NYC"}` {
		t.Errorf("expected delta '{\"city\":\"NYC\"}', got %v", events[3]["delta"])
	}
}

func TestStreamFunctionCallCompletionKeepsArgumentsAsString(t *testing.T) {
	st := &StreamTransformState{}
	tcIndex := 0
	tcID := "call_abc123"
	startChunk := &dto.ChatCompletionsStreamResponse{
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
	_ = st.ProcessChatSSEChunk(startChunk)

	finishReason := "tool_calls"
	finishEvents := st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &finishReason},
		},
	})

	var doneItem map[string]any
	for _, e := range finishEvents {
		if e["type"] == "response.output_item.done" {
			item, _ := e["item"].(map[string]any)
			if item != nil && item["type"] == "function_call" {
				doneItem = item
				break
			}
		}
	}
	if doneItem == nil {
		t.Fatal("expected function_call output_item.done event")
	}
	if doneItem["arguments"] != `{"city":"NYC"}` {
		t.Fatalf("expected completed function_call arguments string, got %#v", doneItem["arguments"])
	}

	finalEvents := st.Finalize()
	for _, e := range finalEvents {
		if e["type"] != "response.completed" {
			continue
		}
		resp := e["response"].(map[string]any)
		output := resp["output"].([]any)
		for _, itemAny := range output {
			item := itemAny.(map[string]any)
			if item["type"] != "function_call" {
				continue
			}
			if item["arguments"] != `{"city":"NYC"}` {
				t.Fatalf("expected response.completed function_call arguments string, got %#v", item["arguments"])
			}
			return
		}
	}
	t.Fatal("expected response.completed with function_call output item")
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
	// response.completed is now emitted by Finalize(), not during chunk processing
	events = append(events, st.Finalize()...)
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
	if _, ok := resp["output"].([]any); !ok {
		t.Fatalf("expected completed response to include output array, got %T", resp["output"])
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
	events = append(events, st.Finalize()...)
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
	events = append(events, st.Finalize()...)
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

	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage to be map[string]any, got %T", resp["usage"])
	}
	if usage["input_tokens"] != 10 {
		t.Fatalf("expected input_tokens=10, got %#v", usage["input_tokens"])
	}
	if usage["output_tokens"] != 5 {
		t.Fatalf("expected output_tokens=5, got %#v", usage["output_tokens"])
	}
	if usage["total_tokens"] != 15 {
		t.Fatalf("expected total_tokens=15, got %#v", usage["total_tokens"])
	}
	details, ok := usage["output_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("expected output_tokens_details map, got %T", usage["output_tokens_details"])
	}
	if details["reasoning_tokens"] != 0 {
		t.Fatalf("expected reasoning_tokens=0, got %#v", details["reasoning_tokens"])
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

	body := w.Body.String()
	if !strings.Contains(body, "event: response.output_text.delta\n") {
		t.Fatalf("expected SSE event line, got %q", body)
	}
	if !strings.Contains(body, "data: ") {
		t.Fatalf("expected SSE data line, got %q", body)
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
	hasInProgress := false
	for _, e := range events {
		if e["type"] == "response.in_progress" {
			hasInProgress = true
			break
		}
	}
	if !hasInProgress {
		t.Fatal("expected response.in_progress event on first chunk")
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

	// Finish reason no longer emits response.completed during chunk processing.
	// Usage from the same chunk IS captured before completion decisions.
	// Now call Finalize() to emit response.completed (which includes the usage).
	events = append(events, st.Finalize()...)

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

	// Verify LatestUsage is set and was captured before Finalize
	if st.LatestUsage == nil {
		t.Error("expected LatestUsage to be set")
	} else if st.LatestUsage.TotalTokens != 7 {
		t.Errorf("expected total_tokens=7, got %d", st.LatestUsage.TotalTokens)
	}
}

func TestReasoningFinalizeEmitsDoneEvents(t *testing.T) {
	st := &StreamTransformState{}
	reasoning := "Need to think."
	stop := "stop"

	startEvents := st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
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
	})
	if len(startEvents) == 0 {
		t.Fatal("expected reasoning start events")
	}

	finalEvents := st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &stop},
		},
	})

	hasReasoningDone := false
	hasReasoningItemDone := false
	for _, e := range finalEvents {
		switch e["type"] {
		case "response.reasoning_summary_text.done":
			hasReasoningDone = true
		case "response.output_item.done":
			item, _ := e["item"].(map[string]any)
			if item != nil && item["type"] == "reasoning" {
				hasReasoningItemDone = true
			}
		}
	}
	if !hasReasoningDone {
		t.Fatal("expected response.reasoning_summary_text.done event")
	}
	if !hasReasoningItemDone {
		t.Fatal("expected response.output_item.done for reasoning item")
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

// =============================================================================
// Tool Type Restoration Tests
// =============================================================================

func TestStreamToolCallRestoresCustomToolItemType(t *testing.T) {
	toolCtx := &ChatToolContext{
		chatNameToSpec: map[string]ChatToolSpec{
			"my_tool": {
				Kind:     ChatToolKindCustom,
				Name:     "my_tool",
				ChatName: "my_tool",
			},
		},
	}

	st := &StreamTransformState{
		ToolCtx: toolCtx,
	}

	tcIndex := 0
	tcID := "call_custom_1"
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
								Name:      "my_tool",
								Arguments: `{"input":"scan the repo"}`,
							},
						},
					},
				},
			},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)

	// Find the output_item.added event and check the type was restored
	var addedItem map[string]any
	for _, e := range events {
		if e["type"] == "response.output_item.added" {
			if item, ok := e["item"].(map[string]any); ok {
				addedItem = item
				break
			}
		}
	}
	if addedItem == nil {
		t.Fatal("expected output_item.added event")
	}
	if addedItem["type"] != "custom_tool_call" {
		t.Errorf("expected item type 'custom_tool_call', got %v", addedItem["type"])
	}
	if addedItem["id"] != "ctc_"+tcID {
		t.Errorf("expected deterministic custom tool id %q, got %v", "ctc_"+tcID, addedItem["id"])
	}
	if addedItem["name"] != "my_tool" {
		t.Errorf("expected name 'my_tool', got %v", addedItem["name"])
	}
}

func TestStreamCustomToolFinalizesWithInputEvents(t *testing.T) {
	toolCtx := &ChatToolContext{
		chatNameToSpec: map[string]ChatToolSpec{
			"my_tool": {
				Kind:     ChatToolKindCustom,
				Name:     "my_tool",
				ChatName: "my_tool",
			},
		},
	}

	st := &StreamTransformState{ToolCtx: toolCtx}
	tcIndex := 0
	tcID := "call_custom_2"

	startChunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-custom",
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
								Name:      "my_tool",
								Arguments: `{"input":"scan the repo"}`,
							},
						},
					},
				},
			},
		},
	}
	st.ProcessChatSSEChunk(startChunk)

	finishReason := "tool_calls"
	finishChunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-custom",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index:        0,
				FinishReason: &finishReason,
			},
		},
	}

	events := st.ProcessChatSSEChunk(finishChunk)
	hasInputDelta := false
	hasInputDone := false
	for _, e := range events {
		switch e["type"] {
		case "response.custom_tool_call_input.delta":
			hasInputDelta = e["delta"] == "scan the repo"
		case "response.custom_tool_call_input.done":
			hasInputDone = e["input"] == "scan the repo"
		case "response.function_call_arguments.done":
			t.Fatal("did not expect generic function_call_arguments.done for custom tool")
		}
	}
	if !hasInputDelta {
		t.Fatal("expected response.custom_tool_call_input.delta event")
	}
	if !hasInputDone {
		t.Fatal("expected response.custom_tool_call_input.done event")
	}
}

func TestStreamToolCallRestoresNamespaceMetadata(t *testing.T) {
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

	st := &StreamTransformState{ToolCtx: toolCtx}
	tcIndex := 0
	tcID := "call_ns_1"
	startChunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-ns",
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
								Name:      "mcp__codex_apps__gmail__search_threads",
								Arguments: `{"query":"inbox"}`,
							},
						},
					},
				},
			},
		},
	}

	startEvents := st.ProcessChatSSEChunk(startChunk)
	var addedItem map[string]any
	for _, e := range startEvents {
		if e["type"] == "response.output_item.added" {
			addedItem = e["item"].(map[string]any)
			break
		}
	}
	if addedItem == nil {
		t.Fatal("expected output_item.added event")
	}
	if addedItem["name"] != "search_threads" {
		t.Fatalf("expected restored name, got %v", addedItem["name"])
	}
	if addedItem["namespace"] != "mcp__codex_apps__gmail" {
		t.Fatalf("expected restored namespace, got %v", addedItem["namespace"])
	}

	finishReason := "tool_calls"
	finishChunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-ns",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index:        0,
				FinishReason: &finishReason,
			},
		},
	}

	finishEvents := st.ProcessChatSSEChunk(finishChunk)
	for _, e := range finishEvents {
		if e["type"] != "response.output_item.done" {
			continue
		}
		item := e["item"].(map[string]any)
		if item["namespace"] != "mcp__codex_apps__gmail" {
			t.Fatalf("expected restored namespace on done item, got %v", item["namespace"])
		}
		if item["name"] != "search_threads" {
			t.Fatalf("expected restored name on done item, got %v", item["name"])
		}
		return
	}
	t.Fatal("expected response.output_item.done event")
}

func TestStreamToolCallRestoresToolSearchType(t *testing.T) {
	toolCtx := &ChatToolContext{
		chatNameToSpec: map[string]ChatToolSpec{
			"tool_search": {
				Kind:     ChatToolKindToolSearch,
				Name:     "tool_search",
				ChatName: "tool_search",
			},
		},
	}

	st := &StreamTransformState{ToolCtx: toolCtx}
	tcIndex := 0
	tcID := "call_tool_search_1"
	startChunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-tool-search",
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
								Name:      "tool_search",
								Arguments: `{"query":"gmail search emails","limit":10}`,
							},
						},
					},
				},
			},
		},
	}

	startEvents := st.ProcessChatSSEChunk(startChunk)
	var addedItem map[string]any
	for _, e := range startEvents {
		if e["type"] == "response.output_item.added" {
			addedItem = e["item"].(map[string]any)
			break
		}
	}
	if addedItem == nil {
		t.Fatal("expected output_item.added event")
	}
	if addedItem["type"] != "tool_search_call" {
		t.Fatalf("expected tool_search_call item type, got %v", addedItem["type"])
	}
	if addedItem["execution"] != "client" {
		t.Fatalf("expected tool_search_call execution=client, got %v", addedItem["execution"])
	}
}

func TestStreamToolSearchCallCoercesStringArgumentsToObject(t *testing.T) {
	toolCtx := &ChatToolContext{
		chatNameToSpec: map[string]ChatToolSpec{
			"tool_search": {
				Kind:     ChatToolKindToolSearch,
				Name:     "tool_search",
				ChatName: "tool_search",
			},
		},
	}

	st := &StreamTransformState{ToolCtx: toolCtx}
	tcIndex := 0
	tcID := "call_tool_search_raw"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-tool-search-raw",
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
								Name:      "tool_search",
								Arguments: `gmail threads`,
							},
						},
					},
				},
			},
		},
	}

	st.ProcessChatSSEChunk(chunk)

	finishReason := "tool_calls"
	finishChunk := &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl-tool-search-raw",
		Model:   "gpt-4o",
		Created: 12345,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index:        0,
				FinishReason: &finishReason,
			},
		},
	}

	events := st.ProcessChatSSEChunk(finishChunk)
	for _, e := range events {
		if e["type"] != "response.output_item.done" {
			continue
		}
		item := e["item"].(map[string]any)
		args, ok := item["arguments"].(map[string]any)
		if !ok {
			t.Fatalf("expected tool_search_call arguments object, got %#v", item["arguments"])
		}
		if args["query"] != "gmail threads" {
			t.Fatalf("expected string arguments to be coerced into query object, got %#v", args)
		}
		return
	}
	t.Fatal("expected response.output_item.done event")
}

func TestStreamToolCallWithoutCtxDefaultsToFunctionCall(t *testing.T) {
	st := &StreamTransformState{}

	tcIndex := 0
	tcID := "call_1"
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
								Arguments: `{}`,
							},
						},
					},
				},
			},
		},
	}

	events := st.ProcessChatSSEChunk(chunk)

	var addedItem map[string]any
	for _, e := range events {
		if e["type"] == "response.output_item.added" {
			if item, ok := e["item"].(map[string]any); ok {
				addedItem = item
				break
			}
		}
	}
	if addedItem == nil {
		t.Fatal("expected output_item.added event")
	}
	if addedItem["type"] != "function_call" {
		t.Errorf("expected default item type 'function_call', got %v", addedItem["type"])
	}
}

// =============================================================================
// Stream Finalization Tests
// =============================================================================

func TestStreamDoesNotCompleteBeforeFinalize(t *testing.T) {
	st := &StreamTransformState{}

	stop := "stop"
	content := "done"
	events1 := st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Id: "chatcmpl-1",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &content}, FinishReason: &stop},
		},
	})

	for _, e := range events1 {
		if e["type"] == "response.completed" {
			t.Fatal("stream should not emit response.completed before Finalize()")
		}
	}

	// Now finalize
	finalEvents := st.Finalize()
	hasCompleted := false
	for _, e := range finalEvents {
		if e["type"] == "response.completed" {
			hasCompleted = true
		}
	}
	if !hasCompleted {
		t.Fatal("expected response.completed on Finalize()")
	}
}

func TestStreamFinalizeUsesTrailingUsageChunk(t *testing.T) {
	st := &StreamTransformState{}

	stop := "stop"
	content := "done"
	_ = st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Id: "chatcmpl-1",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &content}, FinishReason: &stop},
		},
	})

	// Trailing usage chunk after finish reason
	_ = st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Usage: &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	})

	finalEvents := st.Finalize()
	var completedEvent map[string]any
	for _, e := range finalEvents {
		if e["type"] == "response.completed" {
			completedEvent = e
			break
		}
	}
	if completedEvent == nil {
		t.Fatal("expected response.completed event")
	}

	resp, ok := completedEvent["response"].(map[string]any)
	if !ok {
		t.Fatal("expected 'response' map in completed event")
	}
	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage to be map[string]any, got %T", resp["usage"])
	}
	if usage["total_tokens"] != 12 {
		t.Errorf("expected total_tokens=12 in finalized usage, got %#v", usage["total_tokens"])
	}
}

func TestFinalizeIsIdempotent(t *testing.T) {
	st := &StreamTransformState{}
	st.ResponseID = "chatcmpl-1"
	st.responseStarted = true

	stop := "stop"
	_ = st.ProcessChatSSEChunk(&dto.ChatCompletionsStreamResponse{
		Id: "chatcmpl-1",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &stop},
		},
	})

	first := st.Finalize()
	second := st.Finalize()

	if len(first) == 0 {
		t.Fatal("expected at least one event from first Finalize()")
	}
	if len(second) != 0 {
		t.Fatal("second Finalize() should return no events (idempotent)")
	}
}
