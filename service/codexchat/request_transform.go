package codexchat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

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

	// 1. Build messages from the input field
	messages, err := buildMessagesFromInput(responsesReq)
	if err != nil {
		return nil, nil, fmt.Errorf("build messages: %w", err)
	}

	// 2. Prepend instructions as a system message if present
	instructions := extractInstructions(responsesReq.Instructions)
	if instructions != "" {
		systemMsg := dto.Message{
			Role:    "system",
			Content: instructions,
		}
		messages = append([]dto.Message{systemMsg}, messages...)
	}

	// 3. Build tools and tool_choice
	tools, toolChoice, toolCtx, err := buildChatTools(responsesReq.Tools, responsesReq.ToolChoice)
	if err != nil {
		return nil, nil, fmt.Errorf("build tools: %w", err)
	}

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

	// Map StreamOptions
	if responsesReq.StreamOptions != nil {
		chatReq.StreamOptions = responsesReq.StreamOptions
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
func buildMessagesFromInput(req *dto.OpenAIResponsesRequest) ([]dto.Message, error) {
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
		for i, item := range items {
			switch v := item.(type) {
			case string:
				messages = append(messages, dto.Message{Role: "user", Content: v})
			case map[string]any:
				msgs, err := parseInputItem(v)
				if err != nil {
					return nil, fmt.Errorf("parse input item %d: %w", i, err)
				}
				messages = append(messages, msgs...)
			default:
				// Coerce to string via marshal
				data, _ := common.Marshal(v)
				messages = append(messages, dto.Message{Role: "user", Content: string(data)})
			}
		}
		return messages, nil

	case "object":
		var item map[string]any
		if err := common.Unmarshal(req.Input, &item); err != nil {
			return nil, fmt.Errorf("unmarshal input object: %w", err)
		}
		return parseInputItem(item)

	default:
		return nil, fmt.Errorf("unsupported input type: %s", jsonType)
	}
}

// parseInputItem converts a single Responses input item (as a map) into one or more Chat messages.
func parseInputItem(item map[string]any) ([]dto.Message, error) {
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
			return []dto.Message{{Role: roleVal, Content: chatContent}}, nil
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
		return buildFunctionCallMessages(item, "")

	case "function_call_output":
		return buildToolOutputMessages(item)

	case "custom_tool_call":
		return buildFunctionCallMessages(item, "custom_")

	case "custom_tool_call_output":
		return buildToolOutputMessages(item)

	case "tool_search_call":
		// Synthesize a function_call with name="tool_search"
		synthetic := map[string]any{
			"call_id": item["call_id"],
			"name":    "tool_search",
		}
		if args, ok := item["arguments"]; ok {
			synthetic["arguments"] = args
		} else if args, ok := item["input"]; ok {
			synthetic["arguments"] = args
		} else {
			synthetic["arguments"] = ""
		}
		return buildFunctionCallMessages(synthetic, "")

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
//     - input_text  → {"type":"text","text":"..."}
//     - output_text → {"type":"text","text":"..."}
//     - text        → {"type":"text","text":"..."}
//     - input_image → {"type":"image_url","image_url":{"url":"..."}}
//     - input_file / input_audio / unknown → error
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
func buildFunctionCallMessages(item map[string]any, namePrefix string) ([]dto.Message, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return nil, fmt.Errorf("function_call is missing required field 'call_id'")
	}
	name := namePrefix + common.Interface2String(item["name"])
	arguments, err := stringifyJSONLikeValue(item["arguments"])
	if err != nil {
		return nil, fmt.Errorf("stringify function_call arguments: %w", err)
	}

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

	return []dto.Message{{
		Role:      "assistant",
		ToolCalls: tcJSON,
	}}, nil
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
type ChatToolContext struct {
	responseNameToChatName map[string]string
	chatNameToResponseType  map[string]string // chat-visible name -> original Responses type
}

// RestoreToolType returns the original Responses output item type for a Chat
// tool call with the given function name. If the name is not tracked, it
// returns the provided fallback (typically "function_call").
func (ctx *ChatToolContext) RestoreToolType(chatFunctionName string, fallback string) string {
	if ctx == nil {
		return fallback
	}
	if responseType, ok := ctx.chatNameToResponseType[chatFunctionName]; ok {
		return responseType
	}
	return fallback
}

// IsCustomTool checks if a Chat tool function name corresponds to a custom tool.
func (ctx *ChatToolContext) IsCustomTool(chatFunctionName string) bool {
	if ctx == nil {
		return false
	}
	rt, ok := ctx.chatNameToResponseType[chatFunctionName]
	return ok && rt == "custom_tool_call"
}

// buildChatTools converts Responses-format tools ([]map[string]any) and tool_choice into
// Chat-format []ToolCallRequest, a remapped tool_choice (any), and a chatToolContext
// that tracks the name mapping for later reverse-mapping.
func buildChatTools(toolsRaw json.RawMessage, toolChoiceRaw json.RawMessage) ([]dto.ToolCallRequest, any, *ChatToolContext, error) {
	ctx := &ChatToolContext{
		responseNameToChatName: make(map[string]string),
		chatNameToResponseType:  make(map[string]string),
	}

	var tools []dto.ToolCallRequest
	if len(toolsRaw) > 0 {
		var items []map[string]any
		if err := common.Unmarshal(toolsRaw, &items); err != nil {
			return nil, nil, nil, fmt.Errorf("unmarshal tools: %w", err)
		}

		for _, item := range items {
			toolType := common.Interface2String(item["type"])

			// Extract function name and description
			originalName := common.Interface2String(item["name"])
			description := common.Interface2String(item["description"])

			// Compute the Chat-visible name (possibly prefixed)
			chatName := originalName
			switch toolType {
			case "custom":
				chatName = "custom_" + originalName
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
			// "function" and empty type pass through as-is
			}

			// Track the mapping from original name to Chat name
			if originalName != "" {
				ctx.responseNameToChatName[originalName] = chatName
			}

			// Track the original Responses tool type for each Chat name,
			// so the response transform can restore the correct output type.
			if chatName != "" {
				switch toolType {
				case "custom":
					ctx.chatNameToResponseType[chatName] = "custom_tool_call"
				case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
					// web_search doesn't have a distinct Responses output type,
					// but track it for future use
					ctx.chatNameToResponseType[chatName] = "function_call"
				case "file_search":
					ctx.chatNameToResponseType[chatName] = "function_call"
				case "code_interpreter":
					ctx.chatNameToResponseType[chatName] = "function_call"
				case "local_shell":
					ctx.chatNameToResponseType[chatName] = "function_call"
				case "image_generation":
					ctx.chatNameToResponseType[chatName] = "function_call"
				default:
					// "function" or empty -> standard function_call
					ctx.chatNameToResponseType[chatName] = "function_call"
				}
			}

			// If name is still empty, try to derive it from other fields
			if chatName == "" {
				chatName = common.Interface2String(item["function_name"])
			}
			if chatName == "" {
				// Fallback: use type as name
				chatName = toolType
			}
			if description == "" {
				description = common.Interface2String(item["function_description"])
			}

			// Extract parameters
			var params any
			if rawParams, ok := item["parameters"]; ok && rawParams != nil {
				params = rawParams
			} else if rawParams, ok := item["schema"]; ok && rawParams != nil {
				params = rawParams
			}

			tools = append(tools, dto.ToolCallRequest{
				Type: "function",
				Function: dto.FunctionRequest{
					Name:        chatName,
					Description: description,
					Parameters:  params,
				},
			})
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

	// Extract the tool name from the tool_choice object
	name, _ := m["name"].(string)
	if name == "" {
		// No name to remap — return as-is (shouldn't happen in practice)
		return tc
	}

	// Look up the Chat-visible name from the context
	chatName := name
	if mapped, exists := ctx.responseNameToChatName[name]; exists {
		chatName = mapped
	}

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
	return buildMessagesFromInput(req)
}
