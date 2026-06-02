package codexchat

import (
	"encoding/json"
	"fmt"

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

	// Check for function_call_output items whose matching function_call is missing
	cacheLookups := make(map[string]*CachedFunctionCall)
	needsEnrichment := false

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
					cacheLookups[fc.CallID] = &fc
					existingCallIDs[fc.CallID] = true // Mark as found
					needsEnrichment = true
					break
				}
			}
		}
	}

	if !needsEnrichment {
		return nil
	}

	// Prepend recovered function_call items to the input
	var enrichedInput []any
	for _, fc := range cacheLookups {
		fcItem := map[string]any{
			"type":      "function_call",
			"call_id":   fc.CallID,
			"name":      fc.Name,
			"arguments": fc.Arguments,
		}
		enrichedInput = append(enrichedInput, fcItem)
	}

	// Append original items after the recovered ones
	enrichedInput = append(enrichedInput, inputItems...)

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
func ResponsesRequestToChatCompletionsRequest(responsesReq *dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, error) {
	if responsesReq == nil {
		return nil, fmt.Errorf("responses request is nil")
	}

	// 1. Build messages from the input field
	messages, err := buildMessagesFromInput(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("build messages: %w", err)
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
	tools, toolChoice, err := buildChatTools(responsesReq.Tools, responsesReq.ToolChoice)
	if err != nil {
		return nil, fmt.Errorf("build tools: %w", err)
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

	return chatReq, nil
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
			return []dto.Message{{Role: roleVal, Content: item["content"]}}, nil
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
		return []dto.Message{{Role: role, Content: item["content"]}}, nil

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

// buildFunctionCallMessages creates an assistant message with ToolCalls for a function_call-type item.
// The prefix is prepended to the function name (e.g., "custom_" for custom_tool_call).
func buildFunctionCallMessages(item map[string]any, namePrefix string) ([]dto.Message, error) {
	callID := common.Interface2String(item["call_id"])
	if callID == "" {
		return nil, fmt.Errorf("function_call is missing required field 'call_id'")
	}
	name := namePrefix + common.Interface2String(item["name"])
	arguments := common.Interface2String(item["arguments"])
	if arguments == "" {
		// Try to marshal arguments if it's a non-string value
		if rawArgs, ok := item["arguments"]; ok && rawArgs != nil {
			data, err := common.Marshal(rawArgs)
			if err == nil {
				arguments = string(data)
			}
		}
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
	output := common.Interface2String(item["output"])
	if output == "" {
		// Try to marshal output if it's a non-string value
		if rawOutput, ok := item["output"]; ok && rawOutput != nil {
			data, err := common.Marshal(rawOutput)
			if err == nil {
				output = string(data)
			}
		}
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

// buildChatTools converts Responses-format tools ([]map[string]any) and tool_choice into
// Chat-format []ToolCallRequest and tool_choice (any).
func buildChatTools(toolsRaw json.RawMessage, toolChoiceRaw json.RawMessage) ([]dto.ToolCallRequest, any, error) {
	var tools []dto.ToolCallRequest
	if len(toolsRaw) > 0 {
		var items []map[string]any
		if err := common.Unmarshal(toolsRaw, &items); err != nil {
			return nil, nil, fmt.Errorf("unmarshal tools: %w", err)
		}

		for _, item := range items {
			toolType := common.Interface2String(item["type"])

			// Extract function name and description
			name := common.Interface2String(item["name"])
			description := common.Interface2String(item["description"])

			// Prefix name for non-standard tool types so they can be reverse-mapped later
			switch toolType {
			case "custom":
				name = "custom_" + name
			case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
				name = "web_search_" + name
			case "file_search":
				name = "file_search_" + name
			case "code_interpreter":
				name = "code_interpreter_" + name
			case "local_shell":
				name = "local_shell_" + name
			case "image_generation":
				name = "image_generation_" + name
			// "function" and empty type pass through as-is
			}

			// If name is still empty, try to derive it from other fields
			if name == "" {
				name = common.Interface2String(item["function_name"])
			}
			if name == "" {
				// Fallback: use type as name
				name = toolType
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
					Name:        name,
					Description: description,
					Parameters:  params,
				},
			})
		}
	}

	// Parse tool_choice
	var toolChoice any
	if len(toolChoiceRaw) > 0 {
		if err := common.Unmarshal(toolChoiceRaw, &toolChoice); err != nil {
			return nil, nil, fmt.Errorf("unmarshal tool_choice: %w", err)
		}
	}

	return tools, toolChoice, nil
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
