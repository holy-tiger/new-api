package deepseek

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

const (
	responsesLiteAdditionalToolsType = "additional_tools"
	responsesLiteExecToolName        = "exec"
	responsesLiteBridgeContextKey    = "deepseek_responses_lite_bridge"
)

func normalizeDeepSeekResponsesLiteBridgeRequest(request *dto.OpenAIResponsesRequest) (bool, error) {
	if request == nil || common.GetJsonType(request.Input) != "array" {
		return false, nil
	}

	var inputItems []json.RawMessage
	if err := common.Unmarshal(request.Input, &inputItems); err != nil {
		return false, fmt.Errorf("decode Responses input: %w", err)
	}

	additionalIndexes := make(map[int]struct{})
	firstAdditionalIndex := -1
	containsExec := false
	var promotedTools []json.RawMessage
	for index, item := range inputItems {
		itemType, ok := rawJSONStringField(item, "type")
		if !ok || itemType != responsesLiteAdditionalToolsType {
			continue
		}

		var fields map[string]json.RawMessage
		if err := common.Unmarshal(item, &fields); err != nil {
			return false, fmt.Errorf("decode additional_tools item at input[%d]: %w", index, err)
		}
		toolsRaw, exists := fields["tools"]
		if !exists || common.GetJsonType(toolsRaw) != "array" {
			return false, fmt.Errorf("input[%d].tools must be an array for additional_tools", index)
		}
		var tools []json.RawMessage
		if err := common.Unmarshal(toolsRaw, &tools); err != nil {
			return false, fmt.Errorf("decode input[%d].tools: %w", index, err)
		}

		converted := make([]json.RawMessage, 0, len(tools))
		for toolIndex, tool := range tools {
			toolType, typeOK := rawJSONStringField(tool, "type")
			toolName, nameOK := rawJSONStringField(tool, "name")
			if typeOK && nameOK && toolType == "custom" && toolName == responsesLiteExecToolName {
				functionTool, err := deepSeekResponsesExecFunctionTool(tool)
				if err != nil {
					return false, fmt.Errorf("convert input[%d].tools[%d] exec: %w", index, toolIndex, err)
				}
				converted = append(converted, functionTool)
				containsExec = true
				continue
			}
			converted = append(converted, tool)
		}
		additionalIndexes[index] = struct{}{}
		if firstAdditionalIndex < 0 {
			firstAdditionalIndex = index
		}
		promotedTools = append(promotedTools, converted...)
	}

	if !containsExec {
		return false, nil
	}

	mergedTools, err := mergeResponsesLiteTools(request.Tools, promotedTools)
	if err != nil {
		return false, err
	}
	toolsJSON, err := common.Marshal(mergedTools)
	if err != nil {
		return false, fmt.Errorf("encode promoted Responses tools: %w", err)
	}

	convertedItems := append([]json.RawMessage(nil), inputItems...)
	execCallIDs := make(map[string]struct{})
	for index, item := range convertedItems {
		if _, additional := additionalIndexes[index]; additional {
			continue
		}
		itemType, _ := rawJSONStringField(item, "type")
		itemName, _ := rawJSONStringField(item, "name")
		if itemType != "custom_tool_call" || itemName != responsesLiteExecToolName {
			continue
		}
		converted, callID, err := convertResponsesLiteExecHistoryCall(item)
		if err != nil {
			return false, fmt.Errorf("convert exec history at input[%d]: %w", index, err)
		}
		convertedItems[index] = converted
		execCallIDs[callID] = struct{}{}
	}
	for index, item := range convertedItems {
		if _, additional := additionalIndexes[index]; additional {
			continue
		}
		itemType, _ := rawJSONStringField(item, "type")
		if itemType != "custom_tool_call_output" {
			continue
		}
		callID, ok := rawJSONStringField(item, "call_id")
		if !ok {
			continue
		}
		if _, matchesExec := execCallIDs[callID]; !matchesExec {
			continue
		}
		converted, err := replaceRawJSONStringField(item, "type", "function_call_output")
		if err != nil {
			return false, fmt.Errorf("convert exec output history at input[%d]: %w", index, err)
		}
		convertedItems[index] = converted
	}

	instructions := request.Instructions
	liftIndex := -1
	if len(bytes.TrimSpace(instructions)) == 0 && firstAdditionalIndex+1 < len(convertedItems) {
		if text, ok := responsesLiteDeveloperInstructions(convertedItems[firstAdditionalIndex+1]); ok {
			instructions, err = common.Marshal(text)
			if err != nil {
				return false, fmt.Errorf("encode promoted Responses instructions: %w", err)
			}
			liftIndex = firstAdditionalIndex + 1
		}
	}

	remaining := make([]json.RawMessage, 0, len(convertedItems)-len(additionalIndexes))
	for index, item := range convertedItems {
		_, removeAdditional := additionalIndexes[index]
		if removeAdditional || index == liftIndex {
			continue
		}
		remaining = append(remaining, item)
	}
	inputJSON, err := common.Marshal(remaining)
	if err != nil {
		return false, fmt.Errorf("encode normalized Responses input: %w", err)
	}

	request.Input = inputJSON
	request.Tools = toolsJSON
	request.Instructions = instructions
	return true, nil
}

func deepSeekResponsesExecFunctionTool(raw json.RawMessage) (json.RawMessage, error) {
	var tool map[string]json.RawMessage
	if err := common.Unmarshal(raw, &tool); err != nil {
		return nil, err
	}
	description := ""
	if descriptionRaw, ok := tool["description"]; ok {
		if err := common.Unmarshal(descriptionRaw, &description); err != nil {
			return nil, fmt.Errorf("decode description: %w", err)
		}
	}
	return common.Marshal(map[string]any{
		"type":        "function",
		"name":        responsesLiteExecToolName,
		"description": description,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{
					"type":        "string",
					"description": "JavaScript source to execute.",
				},
			},
			"required":             []string{"source"},
			"additionalProperties": false,
		},
		"strict": true,
	})
}

func convertResponsesLiteExecHistoryCall(raw json.RawMessage) (json.RawMessage, string, error) {
	var item map[string]json.RawMessage
	if err := common.Unmarshal(raw, &item); err != nil {
		return nil, "", err
	}
	callID, ok := rawJSONStringField(raw, "call_id")
	if !ok || callID == "" {
		return nil, "", fmt.Errorf("custom_tool_call is missing call_id")
	}
	input, ok := item["input"]
	if !ok {
		return nil, "", fmt.Errorf("custom_tool_call is missing input")
	}
	var source string
	if err := common.Unmarshal(input, &source); err != nil {
		return nil, "", fmt.Errorf("exec custom tool input must be a string: %w", err)
	}
	argumentsJSON, err := common.Marshal(map[string]string{"source": source})
	if err != nil {
		return nil, "", fmt.Errorf("encode exec function arguments: %w", err)
	}
	argumentsString, err := common.Marshal(string(argumentsJSON))
	if err != nil {
		return nil, "", fmt.Errorf("encode exec arguments string: %w", err)
	}
	functionCallType, err := common.Marshal("function_call")
	if err != nil {
		return nil, "", err
	}
	item["type"] = functionCallType
	item["arguments"] = argumentsString
	delete(item, "input")
	converted, err := common.Marshal(item)
	if err != nil {
		return nil, "", err
	}
	return converted, callID, nil
}

func replaceRawJSONStringField(raw json.RawMessage, key, value string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields[key] = encoded
	return common.Marshal(fields)
}

func rawJSONStringField(raw json.RawMessage, key string) (string, bool) {
	if common.GetJsonType(raw) != "object" {
		return "", false
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(raw, &fields) != nil || common.GetJsonType(fields[key]) != "string" {
		return "", false
	}
	var value string
	if common.Unmarshal(fields[key], &value) != nil {
		return "", false
	}
	return value, true
}

func responsesLiteDeveloperInstructions(raw json.RawMessage) (string, bool) {
	itemType, typeOK := rawJSONStringField(raw, "type")
	role, roleOK := rawJSONStringField(raw, "role")
	if !typeOK || itemType != "message" || !roleOK || role != "developer" {
		return "", false
	}
	var item map[string]json.RawMessage
	if common.Unmarshal(raw, &item) != nil || common.GetJsonType(item["content"]) != "array" {
		return "", false
	}
	var parts []json.RawMessage
	if common.Unmarshal(item["content"], &parts) != nil || len(parts) != 1 {
		return "", false
	}
	partType, partTypeOK := rawJSONStringField(parts[0], "type")
	if !partTypeOK || partType != "input_text" {
		return "", false
	}
	return rawJSONStringField(parts[0], "text")
}

func mergeResponsesLiteTools(existingRaw json.RawMessage, promoted []json.RawMessage) ([]json.RawMessage, error) {
	var existing []json.RawMessage
	if len(bytes.TrimSpace(existingRaw)) > 0 {
		if common.GetJsonType(existingRaw) != "array" {
			return nil, fmt.Errorf("top-level Responses tools must be an array when promoting additional_tools")
		}
		if err := common.Unmarshal(existingRaw, &existing); err != nil {
			return nil, fmt.Errorf("decode top-level Responses tools: %w", err)
		}
	}

	merged := make([]json.RawMessage, 0, len(existing)+len(promoted))
	merged = append(merged, existing...)
	identities := make(map[string]struct{}, len(existing))
	for _, tool := range existing {
		if identity, ok := responsesToolIdentity(tool); ok {
			identities[identity] = struct{}{}
		}
	}
	for _, tool := range promoted {
		if identity, ok := responsesToolIdentity(tool); ok {
			if _, duplicate := identities[identity]; duplicate {
				continue
			}
			identities[identity] = struct{}{}
		} else if containsRawJSON(merged, tool) {
			continue
		}
		merged = append(merged, tool)
	}
	return merged, nil
}

func responsesToolIdentity(raw json.RawMessage) (string, bool) {
	toolType, typeOK := rawJSONStringField(raw, "type")
	name, nameOK := rawJSONStringField(raw, "name")
	if !typeOK || !nameOK || name == "" {
		return "", false
	}
	return toolType + "\x00" + name, true
}

func containsRawJSON(values []json.RawMessage, candidate json.RawMessage) bool {
	candidate = bytes.TrimSpace(candidate)
	for _, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), candidate) {
			return true
		}
	}
	return false
}
