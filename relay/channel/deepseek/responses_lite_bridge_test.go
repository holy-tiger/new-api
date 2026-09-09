package deepseek

import (
	"bytes"
	"encoding/json"
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
