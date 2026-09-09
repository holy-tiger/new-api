package deepseek

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

func TestNormalizeDeepSeekResponsesLiteBridgeRequestPromotesCapturedTools(t *testing.T) {
	t.Parallel()

	parallelFalse := json.RawMessage(`false`)
	request := &dto.OpenAIResponsesRequest{
		Model: "deepseek-v4-flash",
		Input: json.RawMessage(`[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"custom","name":"exec","description":"Run JavaScript","format":{"type":"grammar","syntax":"lark","definition":"start: SOURCE"}},
				{"type":"function","name":"wait","description":"Wait","parameters":{"type":"object"},"strict":false},
				{"type":"function","name":"request_user_input","description":"Ask","parameters":{"type":"object"},"strict":false}
			]},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"base instructions"}]},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"runtime context"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run a command"}]}
		]`),
		ParallelToolCalls: parallelFalse,
	}

	enabled, err := normalizeDeepSeekResponsesLiteBridgeRequest(request)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if !enabled {
		t.Fatal("expected bridge activation")
	}

	var instructions string
	if err := common.Unmarshal(request.Instructions, &instructions); err != nil {
		t.Fatalf("decode instructions: %v", err)
	}
	if instructions != "base instructions" {
		t.Fatalf("unexpected instructions %q", instructions)
	}

	var tools []map[string]any
	if err := common.Unmarshal(request.Tools, &tools); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected three tools, got %#v", tools)
	}
	if tools[0]["type"] != "function" || tools[0]["name"] != "exec" || tools[0]["strict"] != true {
		t.Fatalf("unexpected exec tool: %#v", tools[0])
	}
	if _, exists := tools[0]["format"]; exists {
		t.Fatalf("custom format leaked into function tool: %#v", tools[0])
	}
	parameters, ok := tools[0]["parameters"].(map[string]any)
	if !ok || parameters["type"] != "object" || parameters["additionalProperties"] != false {
		t.Fatalf("unexpected exec parameters: %#v", tools[0]["parameters"])
	}
	required, ok := parameters["required"].([]any)
	if !ok || !reflect.DeepEqual(required, []any{"source"}) {
		t.Fatalf("unexpected required fields: %#v", parameters["required"])
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected properties: %#v", parameters["properties"])
	}
	source, ok := properties["source"].(map[string]any)
	if !ok || source["type"] != "string" {
		t.Fatalf("unexpected source schema: %#v", properties["source"])
	}
	if tools[1]["name"] != "wait" || tools[2]["name"] != "request_user_input" {
		t.Fatalf("regular tools changed: %#v", tools)
	}

	var remaining []map[string]any
	if err := common.Unmarshal(request.Input, &remaining); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if len(remaining) != 2 || remaining[0]["role"] != "developer" || remaining[1]["role"] != "user" {
		t.Fatalf("unexpected remaining input: %#v", remaining)
	}
	if !bytes.Equal(request.ParallelToolCalls, parallelFalse) {
		t.Fatalf("parallel_tool_calls changed: %s", request.ParallelToolCalls)
	}
}

func TestNormalizeDeepSeekResponsesLiteBridgeRequestLeavesNonLiteRequestUntouched(t *testing.T) {
	t.Parallel()

	request := &dto.OpenAIResponsesRequest{
		Model:        "deepseek-v4-flash",
		Input:        json.RawMessage(` [ {"type":"message","role":"user","content":"hello"} ] `),
		Tools:        json.RawMessage(` [ {"type":"function","name":"lookup"} ] `),
		Instructions: json.RawMessage(` "keep formatting" `),
	}
	wantInput := append(json.RawMessage(nil), request.Input...)
	wantTools := append(json.RawMessage(nil), request.Tools...)
	wantInstructions := append(json.RawMessage(nil), request.Instructions...)

	enabled, err := normalizeDeepSeekResponsesLiteBridgeRequest(request)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if enabled {
		t.Fatal("standard request activated bridge")
	}
	if !bytes.Equal(request.Input, wantInput) || !bytes.Equal(request.Tools, wantTools) || !bytes.Equal(request.Instructions, wantInstructions) {
		t.Fatalf("inactive request changed: %+v", request)
	}
}

func TestNormalizeDeepSeekResponsesLiteBridgeRequestConvertsExecHistory(t *testing.T) {
	t.Parallel()

	request := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`[
		{"type":"additional_tools","tools":[{"type":"custom","name":"exec","description":"Run JavaScript"}]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"base"}]},
		{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"exec","input":"text(\"ok\");","status":"completed"},
		{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"},
		{"type":"custom_tool_call_output","call_id":"call_unmatched","output":"keep"}
	]`)}

	enabled, err := normalizeDeepSeekResponsesLiteBridgeRequest(request)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if !enabled {
		t.Fatal("expected bridge activation")
	}

	var input []map[string]any
	if err := common.Unmarshal(request.Input, &input); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if len(input) != 3 {
		t.Fatalf("unexpected input length: %#v", input)
	}
	call := input[0]
	if call["type"] != "function_call" || call["name"] != "exec" || call["call_id"] != "call_1" {
		t.Fatalf("exec call not converted: %#v", call)
	}
	if _, exists := call["input"]; exists {
		t.Fatalf("custom input remained: %#v", call)
	}
	var arguments map[string]string
	if err := common.UnmarshalJsonStr(call["arguments"].(string), &arguments); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if arguments["source"] != `text("ok");` {
		t.Fatalf("unexpected source: %#v", arguments)
	}
	if input[1]["type"] != "function_call_output" || input[2]["type"] != "custom_tool_call_output" {
		t.Fatalf("outputs converted incorrectly: %#v", input)
	}
}

func TestNormalizeDeepSeekResponsesLiteBridgeRequestRejectsMalformedToolsAtomically(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`[
		{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"base"}]}
	]`)
	request := &dto.OpenAIResponsesRequest{
		Input: input,
		Tools: json.RawMessage(`{"type":"function","name":"bad"}`),
	}
	wantInput := append(json.RawMessage(nil), request.Input...)
	wantTools := append(json.RawMessage(nil), request.Tools...)

	enabled, err := normalizeDeepSeekResponsesLiteBridgeRequest(request)
	if err == nil || !strings.Contains(err.Error(), "top-level Responses tools must be an array") {
		t.Fatalf("expected tools error, got enabled=%v err=%v", enabled, err)
	}
	if !bytes.Equal(request.Input, wantInput) || !bytes.Equal(request.Tools, wantTools) || len(request.Instructions) != 0 {
		t.Fatalf("request mutated after error: %+v", request)
	}
}

func TestTransformDeepSeekResponsesLiteResponseRestoresExecCustomCall(t *testing.T) {
	t.Parallel()

	input := []byte(`{
		"id":"resp_1",
		"object":"response",
		"status":"completed",
		"output":[
			{"type":"reasoning","id":"rs_1","summary":[]},
			{"type":"function_call","id":"fc_123","call_id":"call_123","name":"exec","arguments":"{\"source\":\"text(\\\"OK\\\");\"}","status":"completed"}
		]
	}`)

	output, err := transformDeepSeekResponsesLiteResponse(input)
	if err != nil {
		t.Fatalf("transform response: %v", err)
	}
	var response map[string]any
	if err := common.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items := response["output"].([]any)
	if len(items) != 2 {
		t.Fatalf("unexpected output: %#v", items)
	}
	call := items[1].(map[string]any)
	if call["type"] != "custom_tool_call" || call["id"] != "ctc_call_123" || call["call_id"] != "call_123" || call["name"] != "exec" {
		t.Fatalf("unexpected custom call: %#v", call)
	}
	if call["input"] != `text("OK");` || call["status"] != "completed" {
		t.Fatalf("unexpected custom input: %#v", call)
	}
	if _, exists := call["arguments"]; exists {
		t.Fatalf("function arguments leaked downstream:	parser %#v", call)
	}
}

func TestTransformDeepSeekResponsesLiteResponseLeavesNonExecCallUnchanged(t *testing.T) {
	t.Parallel()

	input := []byte(`{"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"wait","arguments":"{}"}]}`)
	output, err := transformDeepSeekResponsesLiteResponse(input)
	if err != nil {
		t.Fatalf("transform response: %v", err)
	}
	if !bytes.Equal(output, input) {
		t.Fatalf("non-exec response changed:\nwant %s\n got %s", input, output)
	}
}

func TestTransformDeepSeekResponsesLiteResponseRejectsMalformedExecArguments(t *testing.T) {
	t.Parallel()

	for _, arguments := range []string{`not-json`, `{}`, `{"source":42}`} {
		argumentsJSON, err := common.Marshal(arguments)
		if err != nil {
			t.Fatalf("encode arguments: %v", err)
		}
		input := []byte(`{"output":[{"type":"function_call","call_id":"call_1","name":"exec","arguments":` + string(argumentsJSON) + `}]}`)
		if _, err := transformDeepSeekResponsesLiteResponse(input); err == nil || !strings.Contains(err.Error(), "exec function arguments") {
			t.Fatalf("expected malformed arguments error for %q, got %v", arguments, err)
		}
	}
}

func TestDeepSeekResponsesLiteSSETransformsExecEventSequence(t *testing.T) {
	t.Parallel()

	transformer := &responsesLiteSSETransformer{}
	added := `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_123","call_id":"call_123","name":"exec","arguments":"","status":"in_progress"}}`
	addedOutput, err := transformer.Transform(added)
	if err != nil {
		t.Fatalf("transform added: %v", err)
	}
	if len(addedOutput) != 1 {
		t.Fatalf("expected one added event, got %#v", addedOutput)
	}
	addedEvent := decodeJSONMapForTest(t, addedOutput[0])
	addedItem := addedEvent["item"].(map[string]any)
	if addedItem["type"] != "custom_tool_call" || addedItem["id"] != "ctc_call_123" || addedItem["name"] != "exec" {
		t.Fatalf("unexpected added item: %#v", addedItem)
	}

	for _, delta := range []string{`{"type":"response.function_call_arguments.delta","item_id":"fc_123","output_index":1,"delta":"{\"sou"}`, `{"type":"response.function_call_arguments.delta","item_id":"fc_123","output_index":1,"delta":"rce\":\"text("}`, `{"type":"response.function_call_arguments.delta","item_id":"fc_123","output_index":1,"delta":"\\\"OK\\\");\"}"}`} {
		output, err := transformer.Transform(delta)
		if err != nil {
			t.Fatalf("transform delta: %v", err)
		}
		if len(output) != 0 {
			t.Fatalf("exec function delta leaked downstream: %#v", output)
		}
	}

	done := `{"type":"response.function_call_arguments.done","item_id":"fc_123","output_index":1,"arguments":"{\"source\":\"text(\\\"OK\\\");\"}"}`
	doneOutput, err := transformer.Transform(done)
	if err != nil {
		t.Fatalf("transform arguments done: %v", err)
	}
	if len(doneOutput) != 2 {
		t.Fatalf("expected custom input delta and done, got %#v", doneOutput)
	}
	deltaEvent := decodeJSONMapForTest(t, doneOutput[0])
	doneEvent := decodeJSONMapForTest(t, doneOutput[1])
	if deltaEvent["type"] != "response.custom_tool_call_input.delta" || deltaEvent["item_id"] != "ctc_call_123" || deltaEvent["delta"] != `text("OK");` {
		t.Fatalf("unexpected custom delta: %#v", deltaEvent)
	}
	if doneEvent["type"] != "response.custom_tool_call_input.done" || doneEvent["item_id"] != "ctc_call_123" || doneEvent["input"] != `text("OK");` {
		t.Fatalf("unexpected custom done: %#v", doneEvent)
	}

	itemDone := `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_123","call_id":"call_123","name":"exec","arguments":"{\"source\":\"text(\\\"OK\\\");\"}","status":"completed"}}`
	itemDoneOutput, err := transformer.Transform(itemDone)
	if err != nil {
		t.Fatalf("transform item done: %v", err)
	}
	if len(itemDoneOutput) != 1 {
		t.Fatalf("expected one item done event, got %#v", itemDoneOutput)
	}
	itemDoneEvent := decodeJSONMapForTest(t, itemDoneOutput[0])
	completedItem := itemDoneEvent["item"].(map[string]any)
	if completedItem["type"] != "custom_tool_call" || completedItem["id"] != "ctc_call_123" || completedItem["input"] != `text("OK");` {
		t.Fatalf("unexpected completed item: %#v", completedItem)
	}

	completed := `{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_123","call_id":"call_123","name":"exec","arguments":"{\"source\":\"text(\\\"OK\\\");\"}","status":"completed"}]}}`
	completedOutput, err := transformer.Transform(completed)
	if err != nil {
		t.Fatalf("transform response completed: %v", err)
	}
	completedEvent := decodeJSONMapForTest(t, completedOutput[0])
	response := completedEvent["response"].(map[string]any)
	terminalItem := response["output"].([]any)[0].(map[string]any)
	if terminalItem["type"] != "custom_tool_call" || terminalItem["id"] != "ctc_call_123" {
		t.Fatalf("unexpected terminal output: %#v", terminalItem)
	}
}

func TestDeepSeekResponsesLiteSSEPassesNonExecEventUnchanged(t *testing.T) {
	t.Parallel()

	transformer := &responsesLiteSSETransformer{}
	input := ` {"type":"response.output_text.delta","delta":"hello"} `
	output, err := transformer.Transform(input)
	if err != nil {
		t.Fatalf("transform text event: %v", err)
	}
	if !reflect.DeepEqual(output, []string{input}) {
		t.Fatalf("non-exec event changed: %#v", output)
	}
}

func TestDeepSeekResponsesLiteSSERejectsMalformedExecDone(t *testing.T) {
	t.Parallel()

	transformer := &responsesLiteSSETransformer{}
	added := `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec"}}`
	if _, err := transformer.Transform(added); err != nil {
		t.Fatalf("transform added: %v", err)
	}
	badDone := `{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{}"}`
	if _, err := transformer.Transform(badDone); err == nil || !strings.Contains(err.Error(), "exec function arguments") {
		t.Fatalf("expected malformed arguments error, got %v", err)
	}
}

func TestDeepSeekResponsesLiteSSEBodyTransformsIncrementally(t *testing.T) {
	t.Parallel()

	source := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"source\":\"text(\\\"OK\\\");\"}"}`,
		``,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"source\":\"text(\\\"OK\\\");\"}"}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	body := newResponsesLiteTransformingBody(io.NopCloser(strings.NewReader(source)))
	output, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read transformed body: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("close transformed body: %v", err)
	}
	text := string(output)
	if !strings.Contains(text, `"type":"response.custom_tool_call_input.done"`) || !strings.Contains(text, `data: [DONE]`) {
		t.Fatalf("missing transformed events:\n%s", text)
	}
	if strings.Contains(text, `response.function_call_arguments`) {
		t.Fatalf("function argument event leaked:\n%s", text)
	}
}

func decodeJSONMapForTest(t *testing.T, data string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := common.UnmarshalJsonStr(data, &value); err != nil {
		t.Fatalf("decode JSON %q: %v", data, err)
	}
	return value
}
