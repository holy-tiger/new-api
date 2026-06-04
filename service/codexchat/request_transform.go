package codexchat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

const (
	toolSearchProxyName  = "tool_search"
	customToolInputField = "input"
)

const codexReviewFormattingGuidance = "Codex code review format: start with Findings only, ordered by severity with file/line references. Do not include Scope, Overview, Observations, Overall assessment, or preamble sections. If there are no findings, state that explicitly and then mention residual risks or testing gaps. End with a concise completion sentence."

// EnrichRequestWithHistory checks if function_call_output items are present
// without corresponding function_call items, and recovers them from the cache.
// This enables continuation requests (previous_response_id) to work correctly
// when the client only sends function results without the original calls.
func EnrichRequestWithHistory(
	store *HistoryStore,
	ownerScope string,
	channelID int,
	sessionScope string,
	req *dto.OpenAIResponsesRequest,
) error {
	if store == nil || req == nil {
		return nil
	}
	if req.PreviousResponseID == "" && sessionScope == "" {
		return nil // No continuation context
	}

	// Parse the input to understand what items we already have
	var input any
	if err := common.Unmarshal(req.Input, &input); err != nil {
		return fmt.Errorf("failed to parse input for enrichment: %w", err)
	}

	inputItems, ok := input.([]any)
	if !ok {
		// Single item or string — no enrichment needed
		return nil
	}

	// Collect existing function_call IDs from the input
	existingCallIDs := make(map[string]bool)
	for _, item := range inputItems {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType == "function_call" || itemType == "custom_tool_call" {
			if callID := common.Interface2String(itemMap["call_id"]); callID != "" {
				existingCallIDs[callID] = true
			}
		}
	}

	// Check for function_call_output items whose matching function_call is missing,
	// and recover them from cache. We track recovered calls in order so they can be
	// inserted in-place (immediately before their matching output) rather than
	// prepended at the front of the entire array.
	type recoveredCall struct {
		callID string
		fc     *CachedFunctionCall
	}
	var recoveredCalls []recoveredCall

	for _, item := range inputItems {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType != "function_call_output" {
			continue
		}
		callID := common.Interface2String(itemMap["call_id"])
		if callID == "" {
			continue
		}
		if existingCallIDs[callID] {
			continue // Already have the matching function_call
		}

		// Try to recover from cache
		var cached *CachedResponse
		// Try exact response_id lookup first
		if req.PreviousResponseID != "" {
			cached = store.LookupByResponseID(ownerScope, channelID, req.PreviousResponseID)
		}
		// Fallback: session-scoped call_id lookup
		if cached == nil && sessionScope != "" {
			cached = store.LookupByCallID(ownerScope, channelID, sessionScope, callID)
		}

		if cached != nil {
			for _, fc := range cached.FunctionCalls {
				if fc.CallID == callID && !existingCallIDs[fc.CallID] {
					fcCopy := fc // copy to avoid reference issues
					recoveredCalls = append(recoveredCalls, recoveredCall{
						callID: fc.CallID,
						fc:     &fcCopy,
					})
					existingCallIDs[fc.CallID] = true // Mark as found
					break
				}
			}
		}
	}

	if len(recoveredCalls) == 0 {
		return nil
	}

	// Build a lookup from callID to recoveredCall for fast in-place insertion
	recoveredByCallID := make(map[string]*CachedFunctionCall, len(recoveredCalls))
	for _, rc := range recoveredCalls {
		recoveredByCallID[rc.callID] = rc.fc
	}

	// Rebuild the input in order, inserting recovered function_call items
	// immediately before their matching function_call_output.
	var enrichedInput []any
	for _, item := range inputItems {
		itemMap, ok := item.(map[string]any)
		if ok {
			itemType, _ := itemMap["type"].(string)
			if itemType == "function_call_output" {
				callID := common.Interface2String(itemMap["call_id"])
				if fc, found := recoveredByCallID[callID]; found {
					// Insert the recovered function_call before this output
					enrichedInput = append(enrichedInput, map[string]any{
						"type":      "function_call",
						"call_id":   fc.CallID,
						"name":      fc.Name,
						"arguments": fc.Arguments,
					})
					delete(recoveredByCallID, callID) // avoid double insertion
				}
			}
		}
		enrichedInput = append(enrichedInput, item)
	}

	// Marshal back into req.Input
	enrichedJSON, err := common.Marshal(enrichedInput)
	if err != nil {
		return fmt.Errorf("failed to marshal enriched input: %w", err)
	}
	req.Input = enrichedJSON

	return nil
}

// ResponsesRequestToChatCompletionsRequest converts a Codex Responses API request
// into an OpenAI Chat Completions request so the bridge can forward it to Chat-only
// upstream providers.
func ResponsesRequestToChatCompletionsRequest(responsesReq *dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, *ChatToolContext, error) {
	if responsesReq == nil {
		return nil, nil, fmt.Errorf("responses request is nil")
	}

	// 1. Build tools and tool_choice so input conversion can use reversible tool metadata.
	tools, toolChoice, toolCtx, err := buildChatTools(responsesReq.Tools, responsesReq.ToolChoice)
	if err != nil {
		return nil, nil, fmt.Errorf("build tools: %w", err)
	}

	// 2. Build messages from the input field.
	messages, err := buildMessagesFromInput(responsesReq, toolCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("build messages: %w", err)
	}

	// 3. Prepend instructions as a system message if present
	instructions := extractInstructions(responsesReq.Instructions)
	if instructions != "" {
		systemMsg := dto.Message{
			Role:    "system",
			Content: instructions,
		}
		messages = append([]dto.Message{systemMsg}, messages...)
	}
	if shouldAddCodexReviewFormattingGuidance(responsesReq.Input) {
		messages = append(messages, dto.Message{
			Role:    "system",
			Content: codexReviewFormattingGuidance,
		})
	}
	messages = collapseSystemMessagesToHead(messages)

	// 4. Build response_format from text field
	responseFormat := buildChatResponseFormat(responsesReq.Text)

	// 5. Assemble the chat request
	chatReq := &dto.GeneralOpenAIRequest{
		Model:    responsesReq.Model,
		Messages: messages,
		Stream:   responsesReq.Stream,
	}

	// Map shared scalar params
	if responsesReq.MaxOutputTokens != nil {
		chatReq.MaxCompletionTokens = responsesReq.MaxOutputTokens
	}
	if responsesReq.Temperature != nil {
		chatReq.Temperature = responsesReq.Temperature
	}
	if responsesReq.TopP != nil {
		chatReq.TopP = responsesReq.TopP
	}
	if responsesReq.TopLogProbs != nil {
		chatReq.TopLogProbs = responsesReq.TopLogProbs
	}

	// Map reasoning
	if responsesReq.Reasoning != nil {
		if responsesReq.Reasoning.Effort != "" {
			chatReq.ReasoningEffort = responsesReq.Reasoning.Effort
		}
		// Summary is not directly mappable to Chat fields; store in reasoning json if needed
		// For now, the summary is dropped during conversion as Chat has no equivalent.
	}

	// Map tools and tool_choice
	if len(tools) > 0 {
		chatReq.Tools = tools
	}
	if toolChoice != nil {
		chatReq.ToolChoice = toolChoice
	}

	// Map response_format
	if responseFormat != nil {
		chatReq.ResponseFormat = responseFormat
	}

	// Map parallel_tool_calls
	if responsesReq.ParallelToolCalls != nil {
		// Try to parse as a bool
		if common.GetJsonType(responsesReq.ParallelToolCalls) == "boolean" {
			var b bool
			if err := common.Unmarshal(responsesReq.ParallelToolCalls, &b); err == nil {
				chatReq.ParallelTooCalls = &b
			}
		}
	}

	// Map StreamOptions. Chat-compatible upstreams often omit usage in stream
	// chunks unless include_usage is explicitly requested.
	if responsesReq.StreamOptions != nil {
		chatReq.StreamOptions = responsesReq.StreamOptions
	}
	if responsesReq.Stream != nil && *responsesReq.Stream {
		if chatReq.StreamOptions == nil {
			chatReq.StreamOptions = &dto.StreamOptions{}
		}
		chatReq.StreamOptions.IncludeUsage = true
	}

	// Map Metadata
	if len(responsesReq.Metadata) > 0 {
		chatReq.Metadata = responsesReq.Metadata
	}

	// Map User
	if len(responsesReq.User) > 0 {
		chatReq.User = responsesReq.User
	}

	// Map ServiceTier
	if responsesReq.ServiceTier != "" {
		if data, err := common.Marshal(responsesReq.ServiceTier); err == nil {
			chatReq.ServiceTier = json.RawMessage(data)
		}
	}

	// Map PromptCacheKey
	if len(responsesReq.PromptCacheKey) > 0 {
		var pck string
		if err := common.Unmarshal(responsesReq.PromptCacheKey, &pck); err == nil && pck != "" {
			chatReq.PromptCacheKey = pck
		}
	}

	return chatReq, toolCtx, nil
}

// buildMessagesFromInput parses the Responses `input` field into Chat []Message.
// The input can be a JSON string, a JSON object, or a JSON array.
func buildMessagesFromInput(req *dto.OpenAIResponsesRequest, toolCtx *ChatToolContext) ([]dto.Message, error) {
	if req.Input == nil || len(req.Input) == 0 {
		return nil, fmt.Errorf("input is required")
	}

	jsonType := common.GetJsonType(req.Input)

	switch jsonType {
	case "string":
		var str string
		if err := common.Unmarshal(req.Input, &str); err != nil {
			return nil, fmt.Errorf("unmarshal input string: %w", err)
		}
		return []dto.Message{{Role: "user", Content: str}}, nil

	case "array":
		var items []any
		if err := common.Unmarshal(req.Input, &items); err != nil {
			return nil, fmt.Errorf("unmarshal input array: %w", err)
		}
		var messages []dto.Message
		var pendingToolCalls []dto.ToolCallResponse
		var pendingReasoning string
		lastAssistantIndex := -1
		for i, item := range items {
			switch v := item.(type) {
			case string:
				if len(pendingToolCalls) > 0 {
					messages = append(messages, dto.Message{Role: "user", Content: v})
					lastAssistantIndex = -1
					continue
				}
				if err := flushPendingToolCallMessages(&messages, &pendingToolCalls, &pendingReasoning, &lastAssistantIndex); err != nil {
					return nil, fmt.Errorf("flush pending tool calls before input item %d: %w", i, err)
				}
				if pendingReasoning != "" {
					if !attachReasoningToLastAssistant(messages, lastAssistantIndex, pendingReasoning) {
						messages = append(messages, buildReasoningAssistantMessage(pendingReasoning))
						lastAssistantIndex = len(messages) - 1
					}
					pendingReasoning = ""
				}
				messages = append(messages, dto.Message{Role: "user", Content: v})
				lastAssistantIndex = -1
			case map[string]any:
				typeVal := common.Interface2String(v["type"])
				switch typeVal {
				case "reasoning":
					reasoning := responsesItemReasoningText(v)
					if len(pendingToolCalls) == 0 && !attachReasoningToLastAssistant(messages, lastAssistantIndex, reasoning) {
						pendingReasoning = mergeReasoningText(pendingReasoning, reasoning)
					} else if len(pendingToolCalls) > 0 {
						pendingReasoning = mergeReasoningText(pendingReasoning, reasoning)
					}
					continue
				case "function_call":
					tc, reasoning, err := buildFunctionCallToolCall(v, toolCtx)
					if err != nil {
						return nil, fmt.Errorf("parse input item %d: %w", i, err)
					}
					pendingToolCalls = append(pendingToolCalls, tc)
					pendingReasoning = mergeReasoningText(pendingReasoning, reasoning)
					continue
				case "custom_tool_call":
					tc, reasoning, err := buildCustomToolCallToolCall(v, toolCtx)
					if err != nil {
						return nil, fmt.Errorf("parse input item %d: %w", i, err)
					}
					pendingToolCalls = append(pendingToolCalls, tc)
					pendingReasoning = mergeReasoningText(pendingReasoning, reasoning)
					continue
				case "tool_search_call":
					tc, reasoning, err := buildToolSearchCallToolCall(v, toolCtx)
					if err != nil {
						return nil, fmt.Errorf("parse input item %d: %w", i, err)
					}
					pendingToolCalls = append(pendingToolCalls, tc)
					pendingReasoning = mergeReasoningText(pendingReasoning, reasoning)
					continue
				default:
					if len(pendingToolCalls) > 0 && isResponsesMessageLike(v) {
						start := len(messages)
						msgs, err := parseInputItem(v, toolCtx)
						if err != nil {
							return nil, fmt.Errorf("parse input item %d: %w", i, err)
						}
						messages = append(messages, msgs...)
						updateLastAssistantIndexForAppendedMessages(messages, start, &lastAssistantIndex)
						continue
					}
					if err := flushPendingToolCallMessages(&messages, &pendingToolCalls, &pendingReasoning, &lastAssistantIndex); err != nil {
						return nil, fmt.Errorf("flush pending tool calls before input item %d: %w", i, err)
					}
					if pendingReasoning != "" {
						if !attachReasoningToLastAssistant(messages, lastAssistantIndex, pendingReasoning) {
							messages = append(messages, buildReasoningAssistantMessage(pendingReasoning))
							lastAssistantIndex = len(messages) - 1
						}
						pendingReasoning = ""
					}
				}
				start := len(messages)
				msgs, err := parseInputItem(v, toolCtx)
				if err != nil {
					return nil, fmt.Errorf("parse input item %d: %w", i, err)
				}
				messages = append(messages, msgs...)
				updateLastAssistantIndexForAppendedMessages(messages, start, &lastAssistantIndex)
			default:
				if len(pendingToolCalls) > 0 {
					data, _ := common.Marshal(v)
					messages = append(messages, dto.Message{Role: "user", Content: string(data)})
					lastAssistantIndex = -1
					continue
				}
				if err := flushPendingToolCallMessages(&messages, &pendingToolCalls, &pendingReasoning, &lastAssistantIndex); err != nil {
					return nil, fmt.Errorf("flush pending tool calls before input item %d: %w", i, err)
				}
				if pendingReasoning != "" {
					if !attachReasoningToLastAssistant(messages, lastAssistantIndex, pendingReasoning) {
						messages = append(messages, buildReasoningAssistantMessage(pendingReasoning))
						lastAssistantIndex = len(messages) - 1
					}
					pendingReasoning = ""
				}
				// Coerce to string via marshal
				data, _ := common.Marshal(v)
				messages = append(messages, dto.Message{Role: "user", Content: string(data)})
				lastAssistantIndex = -1
			}
		}
		if err := flushPendingToolCallMessages(&messages, &pendingToolCalls, &pendingReasoning, &lastAssistantIndex); err != nil {
			return nil, fmt.Errorf("flush pending tool calls at end: %w", err)
		}
		if pendingReasoning != "" {
			if !attachReasoningToLastAssistant(messages, lastAssistantIndex, pendingReasoning) {
				messages = append(messages, buildReasoningAssistantMessage(pendingReasoning))
			}
		}
		messages = normalizeAssistantToolCallAdjacency(messages)
		ensureAssistantToolCallReasoningContent(messages)
		return messages, nil

	case "object":
		var item map[string]any
		if err := common.Unmarshal(req.Input, &item); err != nil {
			return nil, fmt.Errorf("unmarshal input object: %w", err)
		}
		return parseInputItem(item, toolCtx)

	default:
		return nil, fmt.Errorf("unsupported input type: %s", jsonType)
	}
}

func isResponsesMessageLike(item map[string]any) bool {
	if item == nil {
		return false
	}
	itemType := common.Interface2String(item["type"])
	if itemType == "message" {
		return true
	}
	if itemType != "" {
		return false
	}
	return common.Interface2String(item["role"]) != "" || item["content"] != nil
}

func normalizeAssistantToolCallAdjacency(messages []dto.Message) []dto.Message {
	if len(messages) == 0 {
		return messages
	}
	normalized := make([]dto.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		normalized = append(normalized, msg)
		expected := assistantToolCallIDs(msg)
		if len(expected) == 0 {
			continue
		}

		found := make(map[string]dto.Message, len(expected))
		interleaved := make([]dto.Message, 0)
		j := i + 1
		for ; j < len(messages) && len(found) < len(expected); j++ {
			next := messages[j]
			if next.Role == "tool" && containsString(expected, next.ToolCallId) {
				found[next.ToolCallId] = next
				continue
			}
			interleaved = append(interleaved, next)
		}
		if len(found) != len(expected) {
			continue
		}
		for _, id := range expected {
			normalized = append(normalized, found[id])
		}
		normalized = append(normalized, interleaved...)
		i = j - 1
	}
	return normalized
}

func assistantToolCallIDs(msg dto.Message) []string {
	if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
		return nil
	}
	var toolCalls []dto.ToolCallResponse
	if err := common.Unmarshal(msg.ToolCalls, &toolCalls); err != nil {
		return nil
	}
	ids := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.ID != "" {
			ids = append(ids, tc.ID)
		}
	}
	return ids
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func collapseSystemMessagesToHead(messages []dto.Message) []dto.Message {
	if len(messages) == 0 {
		return messages
	}
	systemChunks := make([]string, 0)
	rest := make([]dto.Message, 0, len(messages))
	for _, msg := range messages {
		msg.Role = responsesRoleToChatRole(msg.Role)
		if msg.Role == "system" {
			if text := strings.TrimSpace(msg.StringContent()); text != "" {
				systemChunks = append(systemChunks, text)
			}
			continue
		}
		rest = append(rest, msg)
	}
	if len(systemChunks) == 0 {
		return rest
	}
	out := make([]dto.Message, 0, len(rest)+1)
	out = append(out, dto.Message{
		Role:    "system",
		Content: strings.Join(systemChunks, "\n\n"),
	})
	out = append(out, rest...)
	return out
}

func responsesRoleToChatRole(role string) string {
	switch role {
	case "system", "developer":
		return "system"
	case "assistant":
		return "assistant"
	case "tool":
		return "tool"
	case "user", "latest_reminder":
		return "user"
	default:
		return "user"
	}
}

func shouldAddCodexReviewFormattingGuidance(input json.RawMessage) bool {
	text := strings.ToLower(strings.Join(extractInputTextForIntent(input), "\n"))
	if strings.TrimSpace(text) == "" {
		return false
	}
	hasReviewIntent := strings.Contains(text, "review") ||
		strings.Contains(text, "code review") ||
		strings.Contains(text, "审查") ||
		strings.Contains(text, "代码检查")
	if !hasReviewIntent {
		return false
	}
	return strings.Contains(text, "code") ||
		strings.Contains(text, "代码") ||
		strings.Contains(text, "commit") ||
		strings.Contains(text, "diff") ||
		strings.Contains(text, "修改") ||
		strings.Contains(text, "最近")
}

func extractInputTextForIntent(input json.RawMessage) []string {
	if len(input) == 0 {
		return nil
	}
	var value any
	if err := common.Unmarshal(input, &value); err != nil {
		return nil
	}
	var texts []string
	collectInputText(value, &texts)
	return texts
}

func collectInputText(value any, texts *[]string) {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			*texts = append(*texts, v)
		}
	case []any:
		for _, item := range v {
			collectInputText(item, texts)
		}
	case map[string]any:
		if role := common.Interface2String(v["role"]); role != "" && role != "user" && role != "latest_reminder" {
			return
		}
		if content, ok := v["content"]; ok {
			collectInputText(content, texts)
		}
		if text := common.Interface2String(v["text"]); text != "" {
			collectInputText(text, texts)
		}
	default:
	}
}

func flushPendingToolCallMessages(messages *[]dto.Message, pendingToolCalls *[]dto.ToolCallResponse, pendingReasoning *string, lastAssistantIndex *int) error {
	if len(*pendingToolCalls) == 0 {
		return nil
	}
	msg, err := buildAssistantToolCallMessage(*pendingToolCalls, *pendingReasoning)
	if err != nil {
		return err
	}
	*messages = append(*messages, msg)
	*lastAssistantIndex = len(*messages) - 1
	*pendingToolCalls = nil
	*pendingReasoning = ""
	return nil
}

func buildAssistantToolCallMessage(toolCalls []dto.ToolCallResponse, reasoning string) (dto.Message, error) {
	tcJSON, err := common.Marshal(toolCalls)
	if err != nil {
		return dto.Message{}, fmt.Errorf("marshal tool calls: %w", err)
	}
	msg := dto.Message{
		Role:      "assistant",
		ToolCalls: tcJSON,
	}
	if strings.TrimSpace(reasoning) != "" {
		msg.ReasoningContent = reasoning
	}
	return msg, nil
}

func buildFunctionCallToolCall(item map[string]any, toolCtx *ChatToolContext) (dto.ToolCallResponse, string, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return dto.ToolCallResponse{}, "", fmt.Errorf("function_call is missing required field 'call_id'")
	}
	name := common.Interface2String(item["name"])
	if name == "" {
		return dto.ToolCallResponse{}, "", fmt.Errorf("function_call is missing required field 'name'")
	}
	namespace := common.Interface2String(item["namespace"])
	chatName := name
	if toolCtx != nil {
		chatName = toolCtx.ChatNameForResponseFunction(name, namespace)
	} else if namespace != "" {
		chatName = flattenNamespaceToolName(namespace, name)
	}
	if toolCtx != nil {
		kind := ChatToolKindFunction
		if namespace != "" {
			kind = ChatToolKindNamespace
		}
		toolCtx.trackToolSpec(ChatToolSpec{
			Kind:      kind,
			Name:      name,
			Namespace: namespace,
			ChatName:  chatName,
		})
	}
	arguments, err := stringifyJSONLikeValue(item["arguments"])
	if err != nil {
		return dto.ToolCallResponse{}, "", fmt.Errorf("stringify function_call arguments: %w", err)
	}
	return dto.ToolCallResponse{
		ID:   callID,
		Type: "function",
		Function: dto.FunctionResponse{
			Name:      chatName,
			Arguments: arguments,
		},
	}, responsesItemReasoningText(item), nil
}

func buildCustomToolCallToolCall(item map[string]any, toolCtx *ChatToolContext) (dto.ToolCallResponse, string, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return dto.ToolCallResponse{}, "", fmt.Errorf("custom_tool_call is missing required field 'call_id'")
	}
	name := common.Interface2String(item["name"])
	if name == "" {
		return dto.ToolCallResponse{}, "", fmt.Errorf("custom_tool_call is missing required field 'name'")
	}
	if toolCtx != nil {
		toolCtx.trackToolSpec(ChatToolSpec{
			Kind:     ChatToolKindCustom,
			Name:     name,
			ChatName: name,
		})
	}
	arguments, err := customToolCallArguments(item[customToolInputField])
	if err != nil {
		return dto.ToolCallResponse{}, "", fmt.Errorf("stringify custom_tool_call input: %w", err)
	}
	return dto.ToolCallResponse{
		ID:   callID,
		Type: "function",
		Function: dto.FunctionResponse{
			Name:      name,
			Arguments: arguments,
		},
	}, responsesItemReasoningText(item), nil
}

func buildToolSearchCallToolCall(item map[string]any, toolCtx *ChatToolContext) (dto.ToolCallResponse, string, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return dto.ToolCallResponse{}, "", fmt.Errorf("tool_search_call is missing required field 'call_id'")
	}
	if toolCtx != nil {
		toolCtx.trackToolSpec(ChatToolSpec{
			Kind:     ChatToolKindToolSearch,
			Name:     toolSearchProxyName,
			ChatName: toolSearchProxyName,
		})
	}
	arguments, err := toolSearchCallArguments(item)
	if err != nil {
		return dto.ToolCallResponse{}, "", fmt.Errorf("stringify tool_search_call arguments: %w", err)
	}
	return dto.ToolCallResponse{
		ID:   callID,
		Type: "function",
		Function: dto.FunctionResponse{
			Name:      toolSearchProxyName,
			Arguments: arguments,
		},
	}, responsesItemReasoningText(item), nil
}

func buildReasoningAssistantMessage(reasoning string) dto.Message {
	return dto.Message{
		Role:    "assistant",
		Content: "Reasoning: " + reasoning,
	}
}

func mergeReasoningText(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	switch {
	case existing == "":
		return next
	case next == "":
		return existing
	case existing == next:
		return existing
	default:
		return existing + "\n\n" + next
	}
}

func attachReasoningToLastAssistant(messages []dto.Message, lastAssistantIndex int, reasoning string) bool {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return true
	}
	if lastAssistantIndex < 0 || lastAssistantIndex >= len(messages) {
		return false
	}
	if messages[lastAssistantIndex].Role != "assistant" {
		return false
	}
	messages[lastAssistantIndex].ReasoningContent = mergeReasoningText(messages[lastAssistantIndex].ReasoningContent, reasoning)
	return true
}

func updateLastAssistantIndexForAppendedMessages(messages []dto.Message, start int, lastAssistantIndex *int) {
	for i := start; i < len(messages); i++ {
		switch messages[i].Role {
		case "assistant":
			*lastAssistantIndex = i
		case "tool":
		default:
			*lastAssistantIndex = -1
		}
	}
}

func ensureAssistantToolCallReasoningContent(messages []dto.Message) {
	for i := range messages {
		if messages[i].Role != "assistant" {
			continue
		}
		if len(messages[i].ToolCalls) == 0 {
			continue
		}
		if strings.TrimSpace(messages[i].ReasoningContent) != "" {
			continue
		}
		messages[i].ReasoningContent = "tool call"
	}
}

func responsesItemReasoningText(item map[string]any) string {
	if item == nil {
		return ""
	}
	if text := strings.TrimSpace(common.Interface2String(item["reasoning_content"])); text != "" {
		return text
	}
	if text := strings.TrimSpace(common.Interface2String(item["text"])); text != "" {
		return text
	}
	if text := strings.TrimSpace(common.Interface2String(item["content"])); text != "" {
		return text
	}
	if summary := strings.TrimSpace(common.Interface2String(item["summary"])); summary != "" {
		return summary
	}
	if summary, ok := item["summary"].([]any); ok {
		parts := make([]string, 0, len(summary))
		for _, part := range summary {
			switch v := part.(type) {
			case string:
				if s := strings.TrimSpace(v); s != "" {
					parts = append(parts, s)
				}
			case map[string]any:
				if s := strings.TrimSpace(common.Interface2String(v["text"])); s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// parseInputItem converts a single Responses input item (as a map) into one or more Chat messages.
func parseInputItem(item map[string]any, toolCtx *ChatToolContext) ([]dto.Message, error) {
	typeVal := common.Interface2String(item["type"])
	roleVal := common.Interface2String(item["role"])

	// No type field
	if typeVal == "" {
		if roleVal != "" {
			// Has role → use role and content directly
			chatContent, err := responsesContentToChatContent(item["content"])
			if err != nil {
				return nil, err
			}
			return []dto.Message{{Role: responsesRoleToChatRole(roleVal), Content: chatContent}}, nil
		}
		// No role either → marshal entire item as user content
		data, _ := common.Marshal(item)
		return []dto.Message{{Role: "user", Content: string(data)}}, nil
	}

	switch typeVal {
	case "message":
		role, _ := item["role"].(string)
		if role == "" {
			role = "user"
		}
		role = responsesRoleToChatRole(role)
		chatContent, err := responsesContentToChatContent(item["content"])
		if err != nil {
			return nil, err
		}
		return []dto.Message{{Role: role, Content: chatContent}}, nil

	case "reasoning":
		text := common.Interface2String(item["text"])
		if text == "" {
			text = common.Interface2String(item["content"])
		}
		return []dto.Message{{
			Role:    "assistant",
			Content: "Reasoning: " + text,
		}}, nil

	case "function_call":
		return buildFunctionCallMessages(item, toolCtx, "")

	case "function_call_output":
		return buildToolOutputMessages(item)

	case "custom_tool_call":
		return buildCustomToolCallMessages(item, toolCtx, "")

	case "custom_tool_call_output":
		return buildToolOutputMessages(item)

	case "tool_search_call":
		return buildToolSearchCallMessages(item, toolCtx, "")

	case "tool_search_output":
		return buildToolOutputMessages(item)

	case "image_generation_call":
		return nil, fmt.Errorf("image_generation_call not supported in codex chat bridge")

	default:
		return nil, fmt.Errorf("unsupported input item type: %s", typeVal)
	}
}

// responsesContentToChatContent converts a Responses API message content value
// into the equivalent Chat Completions content value.
//
// Conversion rules:
//   - string           → string (passthrough)
//   - []T (array) of content parts → []any of Chat content parts:
//   - input_text  → {"type":"text","text":"..."}
//   - output_text → {"type":"text","text":"..."}
//   - text        → {"type":"text","text":"..."}
//   - input_image → {"type":"image_url","image_url":{"url":"..."}}
//   - input_file / input_audio / unknown → error
func responsesContentToChatContent(content any) (any, error) {
	if content == nil {
		return nil, nil
	}

	// Simple string → passthrough
	if s, ok := content.(string); ok {
		return s, nil
	}

	// Try to interpret as an array of content parts
	var parts []any
	switch v := content.(type) {
	case []map[string]any:
		for _, m := range v {
			parts = append(parts, m)
		}
	case []any:
		parts = v
	default:
		// Not a string or array — return as-is (e.g., a single map)
		return content, nil
	}

	// If the array is empty, return as-is
	if len(parts) == 0 {
		return content, nil
	}

	// Convert each part
	chatParts := make([]any, 0, len(parts))
	for i, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			// Non-map part in the array; return the content as-is
			// (not a structured content array, just a generic array)
			return content, nil
		}

		partType, _ := partMap["type"].(string)
		switch partType {
		case "input_text", "output_text", "text":
			text, _ := partMap["text"].(string)
			chatParts = append(chatParts, map[string]any{
				"type": "text",
				"text": text,
			})
		case "input_image":
			url := common.Interface2String(partMap["image_url"])
			chatParts = append(chatParts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": url,
				},
			})
		case "input_file":
			return nil, fmt.Errorf("content part %d: input_file is not supported in Chat Completions", i)
		case "input_audio":
			return nil, fmt.Errorf("content part %d: input_audio is not supported in Chat Completions", i)
		default:
			// Unknown content part type — if it looks like a Responses content part
			// (has a "type" that starts with "input_" or "output_"), reject it.
			// Otherwise, return as-is to avoid breaking unknown array content.
			if strings.HasPrefix(partType, "input_") || strings.HasPrefix(partType, "output_") {
				return nil, fmt.Errorf("content part %d: unsupported content type %q", i, partType)
			}
			// Not a recognized content part pattern — return original content as-is
			return content, nil
		}
	}

	return chatParts, nil
}

// stringifyJSONLikeValue converts a value to a string in a JSON-preserving way.
// - nil       → ""
// - string    → the string as-is
// - any other → marshal to JSON using common.Marshal
func stringifyJSONLikeValue(v any) (string, error) {
	switch val := v.(type) {
	case nil:
		return "", nil
	case string:
		return val, nil
	default:
		data, err := common.Marshal(val)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

// buildFunctionCallMessages creates an assistant message with ToolCalls for a function_call-type item.
// The prefix is prepended to the function name (e.g., "custom_" for custom_tool_call).
func buildFunctionCallMessage(callID string, name string, arguments string, reasoning string) ([]dto.Message, error) {
	tcs := []dto.ToolCallResponse{{
		ID:   callID,
		Type: "function",
		Function: dto.FunctionResponse{
			Name:      name,
			Arguments: arguments,
		},
	}}
	tcJSON, err := common.Marshal(tcs)
	if err != nil {
		return nil, fmt.Errorf("marshal tool calls: %w", err)
	}

	msg := dto.Message{
		Role:      "assistant",
		ToolCalls: tcJSON,
	}
	if reasoning != "" {
		msg.ReasoningContent = reasoning
	}
	return []dto.Message{msg}, nil
}

func buildFunctionCallMessages(item map[string]any, toolCtx *ChatToolContext, pendingReasoning string) ([]dto.Message, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return nil, fmt.Errorf("function_call is missing required field 'call_id'")
	}
	name := common.Interface2String(item["name"])
	if name == "" {
		return nil, fmt.Errorf("function_call is missing required field 'name'")
	}
	namespace := common.Interface2String(item["namespace"])
	chatName := name
	if toolCtx != nil {
		chatName = toolCtx.ChatNameForResponseFunction(name, namespace)
	} else if namespace != "" {
		chatName = flattenNamespaceToolName(namespace, name)
	}
	if toolCtx != nil {
		kind := ChatToolKindFunction
		if namespace != "" {
			kind = ChatToolKindNamespace
		}
		toolCtx.trackToolSpec(ChatToolSpec{
			Kind:      kind,
			Name:      name,
			Namespace: namespace,
			ChatName:  chatName,
		})
	}
	arguments, err := stringifyJSONLikeValue(item["arguments"])
	if err != nil {
		return nil, fmt.Errorf("stringify function_call arguments: %w", err)
	}
	reasoning := mergeReasoningText(pendingReasoning, responsesItemReasoningText(item))
	return buildFunctionCallMessage(callID, chatName, arguments, reasoning)
}

func customToolCallArguments(input any) (string, error) {
	data, err := common.Marshal(map[string]any{customToolInputField: input})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func buildCustomToolCallMessages(item map[string]any, toolCtx *ChatToolContext, pendingReasoning string) ([]dto.Message, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return nil, fmt.Errorf("custom_tool_call is missing required field 'call_id'")
	}
	name := common.Interface2String(item["name"])
	if name == "" {
		return nil, fmt.Errorf("custom_tool_call is missing required field 'name'")
	}
	if toolCtx != nil {
		toolCtx.trackToolSpec(ChatToolSpec{
			Kind:     ChatToolKindCustom,
			Name:     name,
			ChatName: name,
		})
	}
	arguments, err := customToolCallArguments(item[customToolInputField])
	if err != nil {
		return nil, fmt.Errorf("stringify custom_tool_call input: %w", err)
	}
	reasoning := mergeReasoningText(pendingReasoning, responsesItemReasoningText(item))
	return buildFunctionCallMessage(callID, name, arguments, reasoning)
}

func toolSearchCallArguments(item map[string]any) (string, error) {
	if arguments, ok := item["arguments"]; ok {
		return stringifyJSONLikeValue(arguments)
	}
	if input, ok := item["input"]; ok {
		return stringifyJSONLikeValue(input)
	}
	return "{}", nil
}

func buildToolSearchCallMessages(item map[string]any, toolCtx *ChatToolContext, pendingReasoning string) ([]dto.Message, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return nil, fmt.Errorf("tool_search_call is missing required field 'call_id'")
	}
	if toolCtx != nil {
		toolCtx.trackToolSpec(ChatToolSpec{
			Kind:     ChatToolKindToolSearch,
			Name:     toolSearchProxyName,
			ChatName: toolSearchProxyName,
		})
	}
	arguments, err := toolSearchCallArguments(item)
	if err != nil {
		return nil, fmt.Errorf("stringify tool_search_call arguments: %w", err)
	}
	reasoning := mergeReasoningText(pendingReasoning, responsesItemReasoningText(item))
	return buildFunctionCallMessage(callID, toolSearchProxyName, arguments, reasoning)
}

// buildToolOutputMessages creates a tool message for a function_call_output-type item.
func buildToolOutputMessages(item map[string]any) ([]dto.Message, error) {
	callID := common.Interface2String(item["call_id"])
	output, err := stringifyJSONLikeValue(item["output"])
	if err != nil {
		return nil, fmt.Errorf("stringify function_call_output: %w", err)
	}

	return []dto.Message{{
		Role:       "tool",
		ToolCallId: callID,
		Content:    output,
	}}, nil
}

// extractInstructions decodes the Instructions json.RawMessage.
// It may be a JSON string (decoded value returned) or nil/empty (returns "").
func extractInstructions(instructionsRaw json.RawMessage) string {
	if len(instructionsRaw) == 0 {
		return ""
	}
	jsonType := common.GetJsonType(instructionsRaw)
	if jsonType == "string" {
		var s string
		if err := common.Unmarshal(instructionsRaw, &s); err == nil {
			return s
		}
	}
	// For other types (object, etc.), return empty — unexpected format
	return ""
}

// ChatToolContext tracks the mapping from original Responses tool names to
// the Chat-visible (possibly prefixed) tool names, and from Chat tool names
// back to the original Responses tool type. This allows tool_choice to be
// remapped and later allows response/stream transforms to restore the
// original tool type (e.g., custom_tool_call, tool_search_call).
type ChatToolKind string

const (
	ChatToolKindFunction   ChatToolKind = "function"
	ChatToolKindNamespace  ChatToolKind = "namespace"
	ChatToolKindCustom     ChatToolKind = "custom"
	ChatToolKindToolSearch ChatToolKind = "tool_search"
)

type ChatToolSpec struct {
	Kind      ChatToolKind
	Name      string
	Namespace string
	ChatName  string
}

type ChatToolContext struct {
	responseNameToChatName map[string]string
	chatNameToSpec         map[string]ChatToolSpec
	namespacedNameToChat   map[string]string
	chatNameToResponseType map[string]string
}

func namespaceToolKey(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "\x00" + name
}

func flattenNamespaceToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "__" + name
}

func (ctx *ChatToolContext) trackToolSpec(spec ChatToolSpec) {
	if ctx == nil || spec.ChatName == "" {
		return
	}
	ctx.chatNameToSpec[spec.ChatName] = spec
	if spec.Namespace != "" {
		ctx.namespacedNameToChat[namespaceToolKey(spec.Namespace, spec.Name)] = spec.ChatName
	}
	if spec.Name != "" {
		ctx.responseNameToChatName[spec.Name] = spec.ChatName
	}
	switch spec.Kind {
	case ChatToolKindCustom:
		ctx.chatNameToResponseType[spec.ChatName] = "custom_tool_call"
	case ChatToolKindToolSearch:
		ctx.chatNameToResponseType[spec.ChatName] = "tool_search_call"
	default:
		ctx.chatNameToResponseType[spec.ChatName] = "function_call"
	}
}

func (ctx *ChatToolContext) LookupToolSpec(chatFunctionName string) (ChatToolSpec, bool) {
	if ctx == nil {
		return ChatToolSpec{}, false
	}
	spec, ok := ctx.chatNameToSpec[chatFunctionName]
	return spec, ok
}

func (ctx *ChatToolContext) ChatNameForResponseFunction(name string, namespace string) string {
	if ctx == nil {
		if namespace != "" {
			return flattenNamespaceToolName(namespace, name)
		}
		return name
	}
	if namespace != "" {
		if chatName, ok := ctx.namespacedNameToChat[namespaceToolKey(namespace, name)]; ok {
			return chatName
		}
		return flattenNamespaceToolName(namespace, name)
	}
	if chatName, ok := ctx.responseNameToChatName[name]; ok {
		return chatName
	}
	return name
}

// RestoreToolType returns the original Responses output item type for a Chat
// tool call with the given function name. If the name is not tracked, it
// returns the provided fallback (typically "function_call").
func (ctx *ChatToolContext) RestoreToolType(chatFunctionName string, fallback string) string {
	if ctx == nil {
		return fallback
	}
	if spec, ok := ctx.chatNameToSpec[chatFunctionName]; ok {
		switch spec.Kind {
		case ChatToolKindCustom:
			return "custom_tool_call"
		case ChatToolKindToolSearch:
			return "tool_search_call"
		default:
			return "function_call"
		}
	}
	return fallback
}

// IsCustomTool checks if a Chat tool function name corresponds to a custom tool.
func (ctx *ChatToolContext) IsCustomTool(chatFunctionName string) bool {
	spec, ok := ctx.LookupToolSpec(chatFunctionName)
	return ok && spec.Kind == ChatToolKindCustom
}

// buildChatTools converts Responses-format tools ([]map[string]any) and tool_choice into
// Chat-format []ToolCallRequest, a remapped tool_choice (any), and a chatToolContext
// that tracks the name mapping for later reverse-mapping.
func buildChatTools(toolsRaw json.RawMessage, toolChoiceRaw json.RawMessage) ([]dto.ToolCallRequest, any, *ChatToolContext, error) {
	ctx := &ChatToolContext{
		responseNameToChatName: make(map[string]string),
		chatNameToSpec:         make(map[string]ChatToolSpec),
		namespacedNameToChat:   make(map[string]string),
		chatNameToResponseType: make(map[string]string),
	}

	var tools []dto.ToolCallRequest
	if len(toolsRaw) > 0 {
		var items []map[string]any
		if err := common.Unmarshal(toolsRaw, &items); err != nil {
			return nil, nil, nil, fmt.Errorf("unmarshal tools: %w", err)
		}

		for _, item := range items {
			toolType := common.Interface2String(item["type"])
			if toolType == "namespace" {
				namespace := common.Interface2String(item["name"])
				var children []any
				switch rawChildren := item["tools"].(type) {
				case []any:
					children = rawChildren
				}
				if len(children) == 0 {
					if rawChildren, ok := item["children"].([]any); ok {
						children = rawChildren
					}
				}
				for _, child := range children {
					childMap, ok := child.(map[string]any)
					if !ok {
						continue
					}
					functionTool := buildChatFunctionTool(childMap, namespace, ctx)
					if functionTool != nil {
						tools = append(tools, *functionTool)
					}
				}
				continue
			}

			var functionTool *dto.ToolCallRequest
			switch toolType {
			case "custom":
				functionTool = buildChatCustomTool(item, ctx)
			case "tool_search":
				functionTool = buildChatToolSearchTool(item, ctx)
			default:
				functionTool = buildChatFunctionTool(item, common.Interface2String(item["namespace"]), ctx)
			}
			if functionTool != nil {
				tools = append(tools, *functionTool)
			}
		}
	}

	// Parse and remap tool_choice
	var toolChoice any
	if len(toolChoiceRaw) > 0 {
		if err := common.Unmarshal(toolChoiceRaw, &toolChoice); err != nil {
			return nil, nil, nil, fmt.Errorf("unmarshal tool_choice: %w", err)
		}
		toolChoice = remapToolChoice(toolChoice, ctx)
	}

	return tools, toolChoice, ctx, nil
}

func buildChatFunctionTool(item map[string]any, namespace string, ctx *ChatToolContext) *dto.ToolCallRequest {
	toolType := common.Interface2String(item["type"])
	originalName := common.Interface2String(item["name"])
	if originalName == "" {
		originalName = common.Interface2String(item["function_name"])
	}
	if originalName == "" {
		originalName = toolType
	}

	description := common.Interface2String(item["description"])
	if description == "" {
		description = common.Interface2String(item["function_description"])
	}

	chatName := originalName
	kind := ChatToolKindFunction
	if namespace != "" {
		chatName = flattenNamespaceToolName(namespace, originalName)
		kind = ChatToolKindNamespace
	} else {
		switch toolType {
		case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
			chatName = "web_search_" + originalName
		case "file_search":
			chatName = "file_search_" + originalName
		case "code_interpreter":
			chatName = "code_interpreter_" + originalName
		case "local_shell":
			chatName = "local_shell_" + originalName
		case "image_generation":
			chatName = "image_generation_" + originalName
		}
	}

	var params any
	if rawParams, ok := item["parameters"]; ok && rawParams != nil {
		params = rawParams
	} else if rawParams, ok := item["schema"]; ok && rawParams != nil {
		params = rawParams
	}

	ctx.trackToolSpec(ChatToolSpec{
		Kind:      kind,
		Name:      originalName,
		Namespace: namespace,
		ChatName:  chatName,
	})
	return &dto.ToolCallRequest{
		Type: "function",
		Function: dto.FunctionRequest{
			Name:        chatName,
			Description: description,
			Parameters:  params,
		},
	}
}

func buildChatCustomTool(item map[string]any, ctx *ChatToolContext) *dto.ToolCallRequest {
	name := common.Interface2String(item["name"])
	if name == "" {
		return nil
	}
	description := common.Interface2String(item["description"])
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			customToolInputField: map[string]any{
				"type":        "string",
				"description": "Input to pass to the custom Codex tool.",
			},
		},
		"required": []string{customToolInputField},
	}

	ctx.trackToolSpec(ChatToolSpec{
		Kind:     ChatToolKindCustom,
		Name:     name,
		ChatName: name,
	})
	return &dto.ToolCallRequest{
		Type: "function",
		Function: dto.FunctionRequest{
			Name:        name,
			Description: description,
			Parameters:  params,
		},
	}
}

func buildChatToolSearchTool(item map[string]any, ctx *ChatToolContext) *dto.ToolCallRequest {
	description := common.Interface2String(item["description"])
	if description == "" {
		description = "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task."
	}
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query for tools or connectors to load.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of tool groups to return.",
			},
		},
		"required": []string{"query"},
	}

	ctx.trackToolSpec(ChatToolSpec{
		Kind:     ChatToolKindToolSearch,
		Name:     toolSearchProxyName,
		ChatName: toolSearchProxyName,
	})
	return &dto.ToolCallRequest{
		Type: "function",
		Function: dto.FunctionRequest{
			Name:        toolSearchProxyName,
			Description: description,
			Parameters:  params,
		},
	}
}

// remapToolChoice converts a Responses-format tool_choice into the Chat Completions
// nested selector format. String choices ("auto", "none", "required") are passed
// through as-is. Object choices like {"type":"function","name":"X"} or
// {"type":"custom","name":"Y"} are converted to {"type":"function","function":{"name":"Z"}}
// where Z is the Chat-visible (possibly prefixed) tool name.
func remapToolChoice(tc any, ctx *ChatToolContext) any {
	// String choices pass through unchanged
	if s, ok := tc.(string); ok {
		return s
	}

	// Object choices need to be converted to the Chat nested form
	m, ok := tc.(map[string]any)
	if !ok {
		return tc
	}

	toolType := common.Interface2String(m["type"])
	name, _ := m["name"].(string)
	namespace := common.Interface2String(m["namespace"])

	if toolType == "tool_search" {
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": toolSearchProxyName,
			},
		}
	}
	if name == "" {
		return tc
	}

	chatName := ctx.ChatNameForResponseFunction(name, namespace)

	// Convert to Chat nested selector format
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": chatName,
		},
	}
}

// buildChatResponseFormat extracts a Chat ResponseFormat from the Responses `text` field.
// The Responses `text` field may contain {"format": {...}}.
func buildChatResponseFormat(textRaw json.RawMessage) *dto.ResponseFormat {
	if len(textRaw) == 0 {
		return nil
	}

	// Try to unmarshal as {"format": {...}}
	var wrapper struct {
		Format *struct {
			Type       string          `json:"type"`
			JsonSchema json.RawMessage `json:"json_schema,omitempty"`
		} `json:"format"`
	}
	if err := common.Unmarshal(textRaw, &wrapper); err != nil || wrapper.Format == nil {
		// Non-parseable text format: silently ignored (response_format is optional)
		return nil
	}

	f := wrapper.Format
	if f.Type == "" {
		return nil
	}

	return &dto.ResponseFormat{
		Type:       f.Type,
		JsonSchema: f.JsonSchema,
	}
}

// ResponsesInputToMessages is a convenience wrapper that creates a minimal
// OpenAIResponsesRequest from a raw input json.RawMessage and calls
// the full conversion. This is useful for tests and partial conversions.
func ResponsesInputToMessages(input json.RawMessage) ([]dto.Message, error) {
	req := &dto.OpenAIResponsesRequest{
		Input: input,
	}
	return buildMessagesFromInput(req, nil)
}
