package codexchat

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/gin-gonic/gin"
)

// StreamTransformState tracks state for rebuilding Responses SSE from Chat SSE.
type StreamTransformState struct {
	mu sync.Mutex

	ResponseID   string
	Model        string
	CreatedAt    int64
	FinishReason string

	// Tool context for restoring original tool types in Responses output
	ToolCtx *ChatToolContext

	// Text output item being built
	currentTextStarted bool
	currentTextItemID  string
	currentTextBuffer  strings.Builder

	// Reasoning item being built
	currentReasoningBuffer strings.Builder
	currentReasoningItemID string

	// Tool calls being built by index
	toolCalls map[int]*toolCallBuilder

	// Finished output items (for history storage after stream)
	CompletedFunctionCalls []CachedFunctionCall

	// Think tag extraction state
	inThinkTag  bool
	thinkBuffer strings.Builder

	// Usage
	LatestUsage *dto.Usage

	// Track whether we've sent initial events
	responseStarted bool

	// Counter for output_index values (incremented per new output item)
	outputIndexCounter int
}

type toolCallBuilder struct {
	ItemID    string
	CallID    string
	Name      string
	Arguments strings.Builder
	Finished  bool
	Reasoning strings.Builder // optional reasoning attached to this tool call
}

// ProcessChatSSEChunk processes a single Chat Completions SSE chunk
// and returns zero or more Responses SSE events.
func (st *StreamTransformState) ProcessChatSSEChunk(chunk *dto.ChatCompletionsStreamResponse) []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()

	var events []map[string]any

	// On first chunk, store metadata
	if !st.responseStarted && chunk.Id != "" {
		st.ResponseID = chunk.Id
		st.Model = chunk.Model
		st.CreatedAt = chunk.Created
		st.responseStarted = true
	}

	if len(chunk.Choices) == 0 {
		return events
	}
	choice := chunk.Choices[0]
	delta := choice.Delta

	// Handle role (first delta often has role: "assistant")
	// We don't emit this as a separate Responses event

	// Handle reasoning content
	if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
		events = append(events, st.handleReasoningDelta(*delta.ReasoningContent)...)
	}

	// Handle text content (with <think> tag extraction for providers that use it)
	if delta.Content != nil && *delta.Content != "" {
		events = append(events, st.handleTextDelta(*delta.Content)...)
	}

	// Handle tool calls
	for i := range delta.ToolCalls {
		tc := &delta.ToolCalls[i]
		events = append(events, st.handleToolCallDelta(tc)...)
	}

	// Handle finish reason
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		st.FinishReason = *choice.FinishReason
		events = append(events, st.finalizeOutputs()...)
		events = append(events, st.buildCompletedEvent())
	}

	// Handle usage
	if chunk.Usage != nil {
		st.LatestUsage = chunk.Usage
	}

	return events
}

// handleTextDelta handles incoming text content, extracting <think> tags into
// reasoning and emitting output_item.added / output_text.delta events.
func (st *StreamTransformState) handleTextDelta(content string) []map[string]any {
	var events []map[string]any

	text, reasoning, stillIn := extractThinkContent(content, st.inThinkTag)
	st.inThinkTag = stillIn

	// Emit reasoning part if any
	if reasoning != "" {
		events = append(events, st.handleReasoningDelta(reasoning)...)
	}

	// Emit text part if any
	if text != "" {
		if !st.currentTextStarted {
			// First text delta — emit output_item.added
			st.currentTextItemID = generateResponsesID()
			st.currentTextStarted = true
			events = append(events, map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type": "message",
					"id":   st.currentTextItemID,
					"role": "assistant",
				},
				"output_index": st.nextOutputIndex(),
			})
		}
		st.currentTextBuffer.WriteString(text)
		events = append(events, map[string]any{
			"type":         "response.output_text.delta",
			"delta":        text,
			"item_id":      st.currentTextItemID,
			"output_index": st.nextOutputIndex(),
		})
	}

	return events
}

// handleReasoningDelta handles incoming reasoning content, emitting
// output_item.added and reasoning_summary_text.delta events.
func (st *StreamTransformState) handleReasoningDelta(content string) []map[string]any {
	if st.currentReasoningItemID == "" {
		st.currentReasoningItemID = generateResponsesID()
		st.currentReasoningBuffer.WriteString(content)
		return []map[string]any{
			{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type": "reasoning",
					"id":   st.currentReasoningItemID,
				},
				"output_index": st.nextOutputIndex(),
			},
			{
				"type":    "response.reasoning_summary_text.delta",
				"delta":   content,
				"item_id": st.currentReasoningItemID,
			},
		}
	}
	st.currentReasoningBuffer.WriteString(content)
	return []map[string]any{{
		"type":    "response.reasoning_summary_text.delta",
		"delta":   content,
		"item_id": st.currentReasoningItemID,
	}}
}

// handleToolCallDelta handles incoming tool call deltas, emitting
// output_item.added and function_call_arguments.delta events.
func (st *StreamTransformState) handleToolCallDelta(tc *dto.ToolCallResponse) []map[string]any {
	var events []map[string]any
	idx := 0
	if tc.Index != nil {
		idx = *tc.Index
	}

	if st.toolCalls == nil {
		st.toolCalls = make(map[int]*toolCallBuilder)
	}

	builder, exists := st.toolCalls[idx]
	toolCallID := tc.ID

	// Tool call ID may arrive on first chunk with function name
	if !exists && tc.Function.Name != "" {
		callID := toolCallID
		if callID == "" {
			callID = generateResponsesID()
		}
		builder = &toolCallBuilder{
			ItemID: generateResponsesID(),
			CallID: callID,
			Name:   tc.Function.Name,
		}
		st.toolCalls[idx] = builder

		// Emit function_call output item added, restoring original tool type from context
		toolItemType := st.ToolCtx.RestoreToolType(tc.Function.Name, "function_call")
		events = append(events, map[string]any{
			"type": "response.output_item.added",
			"item": map[string]any{
				"type":    toolItemType,
				"id":      builder.ItemID,
				"call_id": callID,
				"name":    tc.Function.Name,
				"status":  "in_progress",
			},
			"output_index": st.nextOutputIndex(),
		})

		// Also emit arguments delta if any
		if tc.Function.Arguments != "" {
			builder.Arguments.WriteString(tc.Function.Arguments)
			events = append(events, map[string]any{
				"type":         "response.function_call_arguments.delta",
				"delta":        tc.Function.Arguments,
				"item_id":      builder.ItemID,
				"output_index": st.nextOutputIndex(),
			})
		}
		return events
	}

	// Subsequent chunks: accumulate arguments
	if exists && !builder.Finished && tc.Function.Arguments != "" {
		builder.Arguments.WriteString(tc.Function.Arguments)
		events = append(events, map[string]any{
			"type":         "response.function_call_arguments.delta",
			"delta":        tc.Function.Arguments,
			"item_id":      builder.ItemID,
			"output_index": st.nextOutputIndex(),
		})
	}

	return events
}

// finalizeOutputs emits completion events for all in-progress output items
// (function_call_arguments.done, content_part.done).
func (st *StreamTransformState) finalizeOutputs() []map[string]any {
	var events []map[string]any

	// Finalize tool calls
	for _, builder := range st.toolCalls {
		if builder.Finished {
			continue
		}
		args := builder.Arguments.String()
		// Normalize arguments to valid JSON
		if args != "" {
			if !strings.HasPrefix(args, "{") && !strings.HasPrefix(args, "[") {
				args = `"` + args + `"`
			}
		} else {
			args = "{}"
		}

		// Try to parse as JSON, if it's valid JSON keep it as-is.
		// Re-marshal for consistency.
		var parsedArgs any
		if err := common.Unmarshal([]byte(args), &parsedArgs); err == nil {
			argsJSON, _ := common.Marshal(parsedArgs)
			args = string(argsJSON)
		}

		events = append(events, map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      builder.ItemID,
			"arguments":    args,
			"output_index": st.nextOutputIndex(),
		})

		builder.Finished = true

		// Store for history cache
		st.CompletedFunctionCalls = append(st.CompletedFunctionCalls, CachedFunctionCall{
			CallID:    builder.CallID,
			Name:      builder.Name,
			Arguments: args,
			Reasoning: builder.Reasoning.String(),
		})
	}

	// Finalize text item (emit content_part.done)
	if st.currentTextStarted {
		events = append(events, map[string]any{
			"type":         "response.content_part.done",
			"item_id":      st.currentTextItemID,
			"output_index": st.nextOutputIndex(),
		})
	}

	return events
}

// buildCompletedEvent builds the final response.completed event.
func (st *StreamTransformState) buildCompletedEvent() map[string]any {
	status := "completed"
	if st.FinishReason == "length" {
		status = "incomplete"
	}

	event := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         st.ResponseID,
			"object":     "response",
			"created_at": st.CreatedAt,
			"model":      st.Model,
			"status":     status,
		},
	}

	if st.LatestUsage != nil {
		event["response"].(map[string]any)["usage"] = st.LatestUsage
	}

	if status == "incomplete" {
		event["response"].(map[string]any)["incomplete_details"] = map[string]any{
			"reason": "max_output_tokens",
		}
	}

	return event
}

// extractThinkContent is a simple state machine to handle <think>...</think> tags.
// Some providers embed reasoning in text content using these XML tags.
func extractThinkContent(text string, inThinkTag bool) (plainText string, reasoning string, stillInTag bool) {
	var plain strings.Builder
	var reason strings.Builder
	remaining := text
	wasInTag := inThinkTag

	for len(remaining) > 0 {
		if wasInTag {
			idx := strings.Index(remaining, "</think>")
			if idx >= 0 {
				reason.WriteString(remaining[:idx])
				remaining = remaining[idx+len("</think>"):]
				wasInTag = false
			} else {
				reason.WriteString(remaining)
				return plain.String(), reason.String(), true
			}
		} else {
			idx := strings.Index(remaining, "<think>")
			if idx >= 0 {
				plain.WriteString(remaining[:idx])
				remaining = remaining[idx+len("<think>"):]
				wasInTag = true
			} else {
				plain.WriteString(remaining)
				return plain.String(), reason.String(), false
			}
		}
	}
	return plain.String(), reason.String(), wasInTag
}

// nextOutputIndex returns an incrementing output index for multi-item Responses.
// Each call returns the current value and increments the counter for the next item.
func (st *StreamTransformState) nextOutputIndex() int {
	idx := st.outputIndexCounter
	st.outputIndexCounter++
	return idx
}

// WriteResponsesSSEEvent writes a single Responses SSE event to the client.
func WriteResponsesSSEEvent(c *gin.Context, event map[string]any) error {
	data, err := common.Marshal(event)
	if err != nil {
		return err
	}
	return helper.StringData(c, string(data))
}
