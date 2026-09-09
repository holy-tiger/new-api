package deepseek

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

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

func transformDeepSeekResponsesLiteResponse(data []byte) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := common.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode DeepSeek Responses body: %w", err)
	}
	outputRaw, exists := response["output"]
	if !exists || common.GetJsonType(outputRaw) != "array" {
		return data, nil
	}
	var output []json.RawMessage
	if err := common.Unmarshal(outputRaw, &output); err != nil {
		return nil, fmt.Errorf("decode DeepSeek Responses output: %w", err)
	}

	changed := false
	convertedOutput := make([]json.RawMessage, len(output))
	for index, raw := range output {
		convertedOutput[index] = raw
		if common.GetJsonType(raw) != "object" {
			continue
		}
		var item map[string]json.RawMessage
		if err := common.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decode DeepSeek Responses output[%d]: %w", index, err)
		}
		converted, itemChanged, err := transformDeepSeekResponsesLiteOutputItem(item)
		if err != nil {
			return nil, fmt.Errorf("convert DeepSeek Responses output[%d]: %w", index, err)
		}
		if !itemChanged {
			continue
		}
		convertedRaw, err := common.Marshal(converted)
		if err != nil {
			return nil, fmt.Errorf("encode DeepSeek Responses output[%d]: %w", index, err)
		}
		convertedOutput[index] = convertedRaw
		changed = true
	}
	if !changed {
		return data, nil
	}
	encodedOutput, err := common.Marshal(convertedOutput)
	if err != nil {
		return nil, fmt.Errorf("encode DeepSeek Responses output: %w", err)
	}
	response["output"] = encodedOutput
	converted, err := common.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode DeepSeek Responses body: %w", err)
	}
	return converted, nil
}

func transformDeepSeekResponsesLiteOutputItem(item map[string]json.RawMessage) (map[string]json.RawMessage, bool, error) {
	itemType, typeOK := rawJSONStringMapField(item, "type")
	name, nameOK := rawJSONStringMapField(item, "name")
	if !typeOK || !nameOK || itemType != "function_call" || name != responsesLiteExecToolName {
		return item, false, nil
	}
	callID, ok := rawJSONStringMapField(item, "call_id")
	if !ok || callID == "" {
		return nil, false, fmt.Errorf("exec function call is missing call_id")
	}
	argumentsRaw, ok := item["arguments"]
	if !ok {
		return nil, false, fmt.Errorf("exec function arguments are missing")
	}
	var argumentsString string
	if err := common.Unmarshal(argumentsRaw, &argumentsString); err != nil {
		return nil, false, fmt.Errorf("exec function arguments must be a JSON string: %w", err)
	}
	var arguments map[string]json.RawMessage
	if err := common.Unmarshal([]byte(argumentsString), &arguments); err != nil {
		return nil, false, fmt.Errorf("exec function arguments contain invalid JSON: %w", err)
	}
	sourceRaw, ok := arguments["source"]
	if !ok {
		return nil, false, fmt.Errorf("exec function arguments are missing string source")
	}
	var source string
	if err := common.Unmarshal(sourceRaw, &source); err != nil {
		return nil, false, fmt.Errorf("exec function arguments source must be a string: %w", err)
	}

	converted := make(map[string]json.RawMessage, len(item)+1)
	for key, value := range item {
		converted[key] = value
	}
	customType, err := common.Marshal("custom_tool_call")
	if err != nil {
		return nil, false, err
	}
	downstreamID, err := common.Marshal("ctc_" + callID)
	if err != nil {
		return nil, false, err
	}
	input, err := common.Marshal(source)
	if err != nil {
		return nil, false, err
	}
	converted["type"] = customType
	converted["id"] = downstreamID
	converted["input"] = input
	delete(converted, "arguments")
	return converted, true, nil
}

func rawJSONStringMapField(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok || common.GetJsonType(raw) != "string" {
		return "", false
	}
	var value string
	if common.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

type responsesLiteExecStreamCall struct {
	upstreamItemID   string
	downstreamItemID string
	callID           string
	arguments        strings.Builder
	inputEmitted     bool
}

type responsesLiteSSETransformer struct {
	callsByItemID map[string]*responsesLiteExecStreamCall
}

func (t *responsesLiteSSETransformer) Transform(data string) ([]string, error) {
	var event map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(data, &event); err != nil {
		return nil, fmt.Errorf("decode DeepSeek Responses SSE event: %w", err)
	}
	eventType, ok := rawJSONStringMapField(event, "type")
	if !ok {
		return []string{data}, nil
	}

	switch eventType {
	case "response.output_item.added":
		return t.transformOutputItemAdded(data, event)
	case "response.function_call_arguments.delta":
		return t.transformFunctionArgumentsDelta(data, event)
	case "response.function_call_arguments.done":
		return t.transformFunctionArgumentsDone(data, event)
	case "response.output_item.done":
		return t.transformOutputItemDone(data, event)
	case "response.completed", "response.incomplete":
		return transformResponsesLiteTerminalEvent(data, event)
	default:
		return []string{data}, nil
	}
}

func (t *responsesLiteSSETransformer) transformOutputItemAdded(data string, event map[string]json.RawMessage) ([]string, error) {
	itemRaw, ok := event["item"]
	if !ok || common.GetJsonType(itemRaw) != "object" {
		return []string{data}, nil
	}
	var item map[string]json.RawMessage
	if err := common.Unmarshal(itemRaw, &item); err != nil {
		return nil, fmt.Errorf("decode output_item.added item: %w", err)
	}
	itemType, typeOK := rawJSONStringMapField(item, "type")
	name, nameOK := rawJSONStringMapField(item, "name")
	if !typeOK || !nameOK || itemType != "function_call" || name != responsesLiteExecToolName {
		return []string{data}, nil
	}
	upstreamItemID, idOK := rawJSONStringMapField(item, "id")
	callID, callIDOK := rawJSONStringMapField(item, "call_id")
	if !idOK || upstreamItemID == "" || !callIDOK || callID == "" {
		return nil, fmt.Errorf("exec output_item.added is missing id or call_id")
	}
	if t.callsByItemID == nil {
		t.callsByItemID = make(map[string]*responsesLiteExecStreamCall)
	}
	call := &responsesLiteExecStreamCall{
		upstreamItemID:   upstreamItemID,
		downstreamItemID: "ctc_" + callID,
		callID:           callID,
	}
	t.callsByItemID[upstreamItemID] = call

	customType, err := common.Marshal("custom_tool_call")
	if err != nil {
		return nil, err
	}
	downstreamID, err := common.Marshal(call.downstreamItemID)
	if err != nil {
		return nil, err
	}
	item["type"] = customType
	item["id"] = downstreamID
	delete(item, "arguments")
	convertedItem, err := common.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("encode custom output_item.added item: %w", err)
	}
	event["item"] = convertedItem
	convertedEvent, err := common.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode custom output_item.added event: %w", err)
	}
	return []string{string(convertedEvent)}, nil
}

func (t *responsesLiteSSETransformer) transformFunctionArgumentsDelta(data string, event map[string]json.RawMessage) ([]string, error) {
	itemID, ok := rawJSONStringMapField(event, "item_id")
	if !ok || t.callsByItemID == nil {
		return []string{data}, nil
	}
	call, tracked := t.callsByItemID[itemID]
	if !tracked {
		return []string{data}, nil
	}
	delta, ok := rawJSONStringMapField(event, "delta")
	if ok {
		call.arguments.WriteString(delta)
	}
	return nil, nil
}

func (t *responsesLiteSSETransformer) transformFunctionArgumentsDone(data string, event map[string]json.RawMessage) ([]string, error) {
	itemID, ok := rawJSONStringMapField(event, "item_id")
	if !ok || t.callsByItemID == nil {
		return []string{data}, nil
	}
	call, tracked := t.callsByItemID[itemID]
	if !tracked {
		return []string{data}, nil
	}
	if call.inputEmitted {
		return nil, nil
	}
	arguments, ok := rawJSONStringMapField(event, "arguments")
	if !ok || arguments == "" {
		arguments = call.arguments.String()
	}
	return t.customInputEvents(call, event["output_index"], arguments)
}

func (t *responsesLiteSSETransformer) transformOutputItemDone(data string, event map[string]json.RawMessage) ([]string, error) {
	itemRaw, ok := event["item"]
	if !ok || common.GetJsonType(itemRaw) != "object" {
		return []string{data}, nil
	}
	var item map[string]json.RawMessage
	if err := common.Unmarshal(itemRaw, &item); err != nil {
		return nil, fmt.Errorf("decode output_item.done item: %w", err)
	}
	itemID, _ := rawJSONStringMapField(item, "id")
	call := t.callsByItemID[itemID]
	converted, changed, err := transformDeepSeekResponsesLiteOutputItem(item)
	if err != nil {
		return nil, fmt.Errorf("convert output_item.done item: %w", err)
	}
	if !changed {
		return []string{data}, nil
	}

	var output []string
	if call != nil && !call.inputEmitted {
		arguments, _ := rawJSONStringMapField(item, "arguments")
		inputEvents, err := t.customInputEvents(call, event["output_index"], arguments)
		if err != nil {
			return nil, err
		}
		output = append(output, inputEvents...)
	}
	convertedItem, err := common.Marshal(converted)
	if err != nil {
		return nil, fmt.Errorf("encode custom output_item.done item: %w", err)
	}
	event["item"] = convertedItem
	convertedEvent, err := common.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode custom output_item.done event: %w", err)
	}
	output = append(output, string(convertedEvent))
	return output, nil
}

func (t *responsesLiteSSETransformer) customInputEvents(call *responsesLiteExecStreamCall, outputIndex json.RawMessage, arguments string) ([]string, error) {
	source, err := responsesLiteExecSource(arguments)
	if err != nil {
		return nil, err
	}
	call.inputEmitted = true
	delta := map[string]any{
		"type":         "response.custom_tool_call_input.delta",
		"item_id":      call.downstreamItemID,
		"output_index": rawJSONValue(outputIndex),
		"delta":        source,
	}
	done := map[string]any{
		"type":         "response.custom_tool_call_input.done",
		"item_id":      call.downstreamItemID,
		"output_index": rawJSONValue(outputIndex),
		"input":        source,
	}
	deltaJSON, err := common.Marshal(delta)
	if err != nil {
		return nil, err
	}
	doneJSON, err := common.Marshal(done)
	if err != nil {
		return nil, err
	}
	return []string{string(deltaJSON), string(doneJSON)}, nil
}

func responsesLiteExecSource(arguments string) (string, error) {
	var fields map[string]json.RawMessage
	if err := common.Unmarshal([]byte(arguments), &fields); err != nil {
		return "", fmt.Errorf("exec function arguments contain invalid JSON: %w", err)
	}
	sourceRaw, ok := fields["source"]
	if !ok {
		return "", fmt.Errorf("exec function arguments are missing string source")
	}
	var source string
	if err := common.Unmarshal(sourceRaw, &source); err != nil {
		return "", fmt.Errorf("exec function arguments source must be a string: %w", err)
	}
	return source, nil
}

func rawJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := common.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

func transformResponsesLiteTerminalEvent(data string, event map[string]json.RawMessage) ([]string, error) {
	responseRaw, ok := event["response"]
	if !ok || common.GetJsonType(responseRaw) != "object" {
		return []string{data}, nil
	}
	convertedResponse, err := transformDeepSeekResponsesLiteResponse(responseRaw)
	if err != nil {
		return nil, fmt.Errorf("convert terminal Responses output: %w", err)
	}
	if bytes.Equal(convertedResponse, responseRaw) {
		return []string{data}, nil
	}
	event["response"] = convertedResponse
	convertedEvent, err := common.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode terminal Responses event: %w", err)
	}
	return []string{string(convertedEvent)}, nil
}

type responsesLiteTransformingBody struct {
	source      io.ReadCloser
	scanner     *bufio.Scanner
	transformer responsesLiteSSETransformer
	pending     bytes.Buffer
	terminalErr error
}

func newResponsesLiteTransformingBody(source io.ReadCloser) io.ReadCloser {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	return &responsesLiteTransformingBody{
		source:  source,
		scanner: scanner,
	}
}

func (b *responsesLiteTransformingBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for b.pending.Len() == 0 && b.terminalErr == nil {
		if !b.scanner.Scan() {
			b.terminalErr = b.scanner.Err()
			if b.terminalErr == nil {
				b.terminalErr = io.EOF
			}
			break
		}
		line := b.scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			b.pending.WriteString("data: [DONE]\n\n")
			continue
		}
		converted, err := b.transformer.Transform(data)
		if err != nil {
			b.terminalErr = err
			break
		}
		for _, event := range converted {
			b.pending.WriteString("data: ")
			b.pending.WriteString(event)
			b.pending.WriteString("\n\n")
		}
	}
	if b.pending.Len() > 0 {
		return b.pending.Read(p)
	}
	return 0, b.terminalErr
}

func (b *responsesLiteTransformingBody) Close() error {
	return b.source.Close()
}
