package codexchat

import (
	"encoding/json"
	"sort"
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

	// SuppressReasoningOutput keeps provider reasoning_content out of the visible
	// Responses stream while retaining it internally for history/tool-call context.
	SuppressReasoningOutput bool

	// Text output item being built
	currentTextStarted     bool
	currentTextItemID      string
	currentTextOutputIndex int
	currentTextBuffer      strings.Builder

	// Reasoning item being built
	currentReasoningBuffer      strings.Builder
	currentReasoningItemID      string
	currentReasoningOutputIndex int

	// Tool calls being built by index
	toolCalls map[int]*toolCallBuilder

	// Finished output items (for history storage after stream)
	CompletedFunctionCalls []CachedFunctionCall
	completedOutputItems   []completedOutputItem

	// Think tag extraction state
	inThinkTag  bool
	thinkBuffer strings.Builder

	// Usage
	LatestUsage *dto.Usage

	// Track whether we've sent initial events
	responseStarted        bool
	responseCreatedEmitted bool

	// Counter for output_index values (incremented per new output item)
	outputIndexCounter int

	// finishFinalized prevents duplicate response.completed events
	finishFinalized bool
}

type toolCallBuilder struct {
	ItemID      string
	CallID      string
	Name        string
	OutputIndex int
	Arguments   strings.Builder
	Finished    bool
	Reasoning   strings.Builder // optional reasoning attached to this tool call
}

type completedOutputItem struct {
	OutputIndex int
	Item        map[string]any
}

func streamEventValueFromRaw(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var parsed any
	if err := common.Unmarshal(raw, &parsed); err == nil {
		return parsed
	}
	return string(raw)
}

func (st *StreamTransformState) buildToolEventItem(itemID string, callID string, chatName string, status string, arguments string) map[string]any {
	output := restoreResponsesToolOutput(dto.ToolCallResponse{
		ID: callID,
		Function: dto.FunctionResponse{
			Name:      chatName,
			Arguments: arguments,
		},
	}, st.ToolCtx)

	item := map[string]any{
		"type":    output.Type,
		"id":      itemID,
		"call_id": callID,
		"status":  status,
	}
	if output.Name != "" {
		item["name"] = output.Name
	}
	if output.Namespace != "" {
		item["namespace"] = output.Namespace
	}
	if output.Type == "tool_search_call" {
		item["execution"] = "client"
	}
	if value := streamEventValueFromRaw(output.Arguments); value != nil {
		item["arguments"] = value
	}
	if value := streamEventValueFromRaw(output.Input); value != nil {
		item["input"] = value
	}
	return item
}

func cloneEventItem(item map[string]any) map[string]any {
	if item == nil {
		return nil
	}
	cloned := make(map[string]any, len(item))
	for k, v := range item {
		cloned[k] = v
	}
	return cloned
}

func (st *StreamTransformState) recordCompletedOutputItem(outputIndex int, item map[string]any) {
	if item == nil {
		return
	}
	st.completedOutputItems = append(st.completedOutputItems, completedOutputItem{
		OutputIndex: outputIndex,
		Item:        cloneEventItem(item),
	})
}

func (st *StreamTransformState) completedOutput() []map[string]any {
	if len(st.completedOutputItems) == 0 {
		return []map[string]any{}
	}
	items := make([]completedOutputItem, len(st.completedOutputItems))
	copy(items, st.completedOutputItems)
	sort.Slice(items, func(i, j int) bool {
		return items[i].OutputIndex < items[j].OutputIndex
	})
	output := make([]map[string]any, 0, len(items))
	for _, item := range items {
		output = append(output, cloneEventItem(item.Item))
	}
	return output
}

func responsesUsageFromChatUsage(usage *dto.Usage) map[string]any {
	if usage == nil {
		return map[string]any{
			"input_tokens":          0,
			"output_tokens":         0,
			"total_tokens":          0,
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		}
	}

	inputTokens := usage.PromptTokens
	if inputTokens == 0 && usage.InputTokens != 0 {
		inputTokens = usage.InputTokens
	}
	outputTokens := usage.CompletionTokens
	if outputTokens == 0 && usage.OutputTokens != 0 {
		outputTokens = usage.OutputTokens
	}
	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}

	respUsage := map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"total_tokens":  totalTokens,
		"output_tokens_details": map[string]any{
			"text_tokens":      usage.CompletionTokenDetails.TextTokens,
			"audio_tokens":     usage.CompletionTokenDetails.AudioTokens,
			"image_tokens":     usage.CompletionTokenDetails.ImageTokens,
			"reasoning_tokens": usage.CompletionTokenDetails.ReasoningTokens,
		},
	}

	cachedTokens := usage.PromptTokensDetails.CachedTokens
	if cachedTokens == 0 && usage.InputTokensDetails != nil {
		cachedTokens = usage.InputTokensDetails.CachedTokens
	}
	if cachedTokens > 0 {
		respUsage["input_tokens_details"] = map[string]any{
			"cached_tokens": cachedTokens,
		}
	}

	return respUsage
}

func (st *StreamTransformState) baseResponse(status string, output []any) map[string]any {
	return map[string]any{
		"id":         st.ResponseID,
		"object":     "response",
		"created_at": st.CreatedAt,
		"model":      st.Model,
		"status":     status,
		"output":     output,
		"usage":      responsesUsageFromChatUsage(st.LatestUsage),
	}
}

func (st *StreamTransformState) isCustomToolChatName(chatName string) bool {
	if st.ToolCtx == nil {
		return false
	}
	if spec, ok := st.ToolCtx.LookupToolSpec(chatName); ok {
		return spec.Kind == ChatToolKindCustom
	}
	return st.ToolCtx.RestoreToolType(chatName, "function_call") == "custom_tool_call"
}

func customToolInputString(arguments string) string {
	input := customToolInputFromArguments(arguments)
	if len(input) == 0 {
		return ""
	}
	var value any
	if err := common.Unmarshal(input, &value); err != nil {
		return string(input)
	}
	if s, ok := value.(string); ok {
		return s
	}
	data, err := common.Marshal(value)
	if err != nil {
		return string(input)
	}
	return string(data)
}

// ProcessChatSSEChunk processes a single Chat Completions SSE chunk
// and returns zero or more Responses SSE events.
func (st *StreamTransformState) ProcessChatSSEChunk(chunk *dto.ChatCompletionsStreamResponse) []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()

	var events []map[string]any

	// On first chunk, store metadata
	if !st.responseStarted && chunk.Id != "" {
		st.ResponseID = responseIDFromChatID(chunk.Id)
		st.Model = chunk.Model
		st.CreatedAt = chunk.Created
		st.responseStarted = true
	}

	// Always capture usage before any early return — trailing usage chunks
	// may arrive after the finish reason chunk with no choices.
	if chunk.Usage != nil {
		st.LatestUsage = chunk.Usage
	}

	if len(chunk.Choices) == 0 {
		return events
	}
	if st.responseStarted && !st.responseCreatedEmitted {
		st.responseCreatedEmitted = true
		events = append(events, st.buildCreatedEvent())
		events = append(events, st.buildInProgressEvent())
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

	// Handle finish reason — finalize output items but do NOT emit response.completed
	// yet. That happens in Finalize() after the stream ends, so trailing usage chunks
	// can be captured first.
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		st.FinishReason = *choice.FinishReason
		events = append(events, st.finalizeOutputs()...)
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
			st.currentTextOutputIndex = st.nextOutputIndex()
			events = append(events, map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type":    "message",
					"id":      st.currentTextItemID,
					"role":    "assistant",
					"status":  "in_progress",
					"content": []any{},
				},
				"output_index": st.currentTextOutputIndex,
			})
			events = append(events, map[string]any{
				"type":          "response.content_part.added",
				"item_id":       st.currentTextItemID,
				"output_index":  st.currentTextOutputIndex,
				"content_index": 0,
				"part": map[string]any{
					"type":        "output_text",
					"text":        "",
					"annotations": []any{},
				},
			})
		}
		st.currentTextBuffer.WriteString(text)
		events = append(events, map[string]any{
			"type":          "response.output_text.delta",
			"delta":         text,
			"item_id":       st.currentTextItemID,
			"output_index":  st.currentTextOutputIndex,
			"content_index": 0,
		})
	}

	return events
}

// handleReasoningDelta handles incoming reasoning content, emitting
// output_item.added and reasoning_summary_text.delta events.
func (st *StreamTransformState) handleReasoningDelta(content string) []map[string]any {
	if st.SuppressReasoningOutput {
		st.currentReasoningBuffer.WriteString(content)
		return nil
	}
	if st.currentReasoningItemID == "" {
		st.currentReasoningItemID = generateResponsesID()
		st.currentReasoningOutputIndex = st.nextOutputIndex()
		st.currentReasoningBuffer.WriteString(content)
		return []map[string]any{
			{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type":    "reasoning",
					"id":      st.currentReasoningItemID,
					"status":  "in_progress",
					"summary": []any{},
				},
				"output_index": st.currentReasoningOutputIndex,
			},
			{
				"type":          "response.reasoning_summary_part.added",
				"item_id":       st.currentReasoningItemID,
				"output_index":  st.currentReasoningOutputIndex,
				"summary_index": 0,
				"part": map[string]any{
					"type": "summary_text",
					"text": "",
				},
			},
			{
				"type":          "response.reasoning_summary_text.delta",
				"delta":         content,
				"item_id":       st.currentReasoningItemID,
				"output_index":  st.currentReasoningOutputIndex,
				"summary_index": 0,
			},
		}
	}
	st.currentReasoningBuffer.WriteString(content)
	return []map[string]any{{
		"type":          "response.reasoning_summary_text.delta",
		"delta":         content,
		"item_id":       st.currentReasoningItemID,
		"output_index":  st.currentReasoningOutputIndex,
		"summary_index": 0,
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
			CallID:      callID,
			Name:        tc.Function.Name,
			OutputIndex: st.nextOutputIndex(),
		}
		if st.currentReasoningBuffer.Len() > 0 {
			builder.Reasoning.WriteString(st.currentReasoningBuffer.String())
		}
		builder.ItemID = responseToolCallItemID(callID, builder.Name, st.ToolCtx)
		st.toolCalls[idx] = builder

		events = append(events, map[string]any{
			"type":         "response.output_item.added",
			"item":         st.buildToolEventItem(builder.ItemID, callID, builder.Name, "in_progress", ""),
			"output_index": builder.OutputIndex,
		})

		// Also emit arguments delta if any for non-custom tools.
		if tc.Function.Arguments != "" && !st.isCustomToolChatName(builder.Name) {
			builder.Arguments.WriteString(tc.Function.Arguments)
			events = append(events, map[string]any{
				"type":         "response.function_call_arguments.delta",
				"delta":        tc.Function.Arguments,
				"item_id":      builder.ItemID,
				"output_index": builder.OutputIndex,
			})
		} else if tc.Function.Arguments != "" {
			builder.Arguments.WriteString(tc.Function.Arguments)
		}
		return events
	}

	// Subsequent chunks: accumulate arguments
	if exists && !builder.Finished && tc.Function.Arguments != "" {
		builder.Arguments.WriteString(tc.Function.Arguments)
		if !st.isCustomToolChatName(builder.Name) {
			events = append(events, map[string]any{
				"type":         "response.function_call_arguments.delta",
				"delta":        tc.Function.Arguments,
				"item_id":      builder.ItemID,
				"output_index": builder.OutputIndex,
			})
		}
	}

	return events
}

// finalizeOutputs emits completion events for all in-progress output items
// (function_call_arguments.done, content_part.done).
func (st *StreamTransformState) finalizeOutputs() []map[string]any {
	var events []map[string]any

	// Finalize tool calls
	var toolIndexes []int
	for idx := range st.toolCalls {
		toolIndexes = append(toolIndexes, idx)
	}
	sort.Ints(toolIndexes)
	for _, idx := range toolIndexes {
		builder := st.toolCalls[idx]
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

		if st.isCustomToolChatName(builder.Name) {
			input := customToolInputString(args)
			if input != "" {
				events = append(events, map[string]any{
					"type":         "response.custom_tool_call_input.delta",
					"item_id":      builder.ItemID,
					"output_index": builder.OutputIndex,
					"delta":        input,
				})
			}
			events = append(events, map[string]any{
				"type":         "response.custom_tool_call_input.done",
				"item_id":      builder.ItemID,
				"output_index": builder.OutputIndex,
				"input":        input,
			})
		} else {
			events = append(events, map[string]any{
				"type":         "response.function_call_arguments.done",
				"item_id":      builder.ItemID,
				"arguments":    args,
				"output_index": builder.OutputIndex,
			})
		}
		doneItem := st.buildToolEventItem(builder.ItemID, builder.CallID, builder.Name, "completed", args)
		events = append(events, map[string]any{
			"type":         "response.output_item.done",
			"item":         doneItem,
			"output_index": builder.OutputIndex,
		})
		st.recordCompletedOutputItem(builder.OutputIndex, doneItem)

		builder.Finished = true

		// Store for history cache
		st.CompletedFunctionCalls = append(st.CompletedFunctionCalls, CachedFunctionCall{
			CallID:    builder.CallID,
			Name:      builder.Name,
			Arguments: args,
			Reasoning: builder.Reasoning.String(),
		})
	}

	if st.currentReasoningItemID != "" {
		reasoningText := st.currentReasoningBuffer.String()
		reasoningItem := map[string]any{
			"id":     st.currentReasoningItemID,
			"type":   "reasoning",
			"status": "completed",
			"summary": []map[string]any{{
				"type": "summary_text",
				"text": reasoningText,
			}},
		}
		events = append(events, map[string]any{
			"type":          "response.reasoning_summary_text.done",
			"item_id":       st.currentReasoningItemID,
			"output_index":  st.currentReasoningOutputIndex,
			"summary_index": 0,
			"text":          reasoningText,
		})
		events = append(events, map[string]any{
			"type":          "response.reasoning_summary_part.done",
			"item_id":       st.currentReasoningItemID,
			"output_index":  st.currentReasoningOutputIndex,
			"summary_index": 0,
			"part": map[string]any{
				"type": "summary_text",
				"text": reasoningText,
			},
		})
		events = append(events, map[string]any{
			"type":         "response.output_item.done",
			"item":         reasoningItem,
			"output_index": st.currentReasoningOutputIndex,
		})
		st.recordCompletedOutputItem(st.currentReasoningOutputIndex, reasoningItem)
		st.currentReasoningItemID = ""
	}
	if st.SuppressReasoningOutput && st.currentReasoningBuffer.Len() > 0 {
		st.currentReasoningBuffer.Reset()
	}

	// Finalize text item (emit content_part.done)
	if st.currentTextStarted {
		text := st.currentTextBuffer.String()
		events = append(events, map[string]any{
			"type":          "response.output_text.done",
			"item_id":       st.currentTextItemID,
			"output_index":  st.currentTextOutputIndex,
			"content_index": 0,
			"text":          text,
		})
		events = append(events, map[string]any{
			"type":          "response.content_part.done",
			"item_id":       st.currentTextItemID,
			"output_index":  st.currentTextOutputIndex,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			},
		})
		doneItem := map[string]any{
			"type":   "message",
			"id":     st.currentTextItemID,
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		}
		events = append(events, map[string]any{
			"type":         "response.output_item.done",
			"item":         doneItem,
			"output_index": st.currentTextOutputIndex,
		})
		st.recordCompletedOutputItem(st.currentTextOutputIndex, doneItem)
	}

	return events
}

func (st *StreamTransformState) buildCreatedEvent() map[string]any {
	return map[string]any{
		"type":     "response.created",
		"response": st.baseResponse("in_progress", []any{}),
	}
}

func (st *StreamTransformState) buildInProgressEvent() map[string]any {
	return map[string]any{
		"type":     "response.in_progress",
		"response": st.baseResponse("in_progress", []any{}),
	}
}

// buildCompletedEvent builds the final response.completed event.
func (st *StreamTransformState) buildCompletedEvent() map[string]any {
	status := "completed"
	if st.FinishReason == "length" {
		status = "incomplete"
	}

	event := map[string]any{
		"type":     "response.completed",
		"response": st.baseResponse(status, mapsToInterfaces(st.completedOutput())),
	}

	if status == "incomplete" {
		event["response"].(map[string]any)["incomplete_details"] = map[string]any{
			"reason": "max_output_tokens",
		}
	}

	return event
}

func (st *StreamTransformState) BuildFailedEvent(message string, errorType string) map[string]any {
	response := st.baseResponse("failed", mapsToInterfaces(st.completedOutput()))
	errObj := map[string]any{
		"message": message,
	}
	if errorType != "" {
		errObj["type"] = errorType
	}
	response["error"] = errObj
	return map[string]any{
		"type":     "response.failed",
		"response": response,
	}
}

func mapsToInterfaces(items []map[string]any) []any {
	if len(items) == 0 {
		return []any{}
	}
	output := make([]any, 0, len(items))
	for _, item := range items {
		output = append(output, item)
	}
	return output
}

// Finalize emits the final response.completed event after the stream has
// ended. It is idempotent — calling it more than once has no effect.
// This should be called after StreamScannerHandler returns (i.e., after
// [DONE] sentinel, upstream EOF, or connection close).
func (st *StreamTransformState) Finalize() []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Guard against double finalization
	if st.finishFinalized {
		return nil
	}
	st.finishFinalized = true

	// If no finish reason was ever received, finalize outputs now
	if st.FinishReason == "" {
		st.FinishReason = "stop"
		events := st.finalizeOutputs()
		events = append(events, st.buildCompletedEvent())
		return events
	}

	return []map[string]any{st.buildCompletedEvent()}
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

	if eventType, ok := event["type"].(string); ok && eventType != "" {
		c.Render(-1, common.CustomEvent{Data: "event: " + eventType + "\n"})
		c.Render(-1, common.CustomEvent{Data: "data: " + string(data)})
		return helper.FlushWriter(c)
	}

	return helper.StringData(c, string(data))
}
