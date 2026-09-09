# DeepSeek Responses Lite Bidirectional Bridge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Translate mapped Codex Responses Lite `custom exec` traffic to DeepSeek's supported `function exec(source)` protocol and restore the response to Codex custom-tool semantics.

**Architecture:** A DeepSeek-only request bridge converts tool definitions and tool history before the native Responses request. A private Gin-context flag enables a matching non-streaming/SSE response bridge; existing OpenAI Responses handlers continue to own downstream writing, errors, and usage accounting.

**Tech Stack:** Go 1.22+, Gin, project `common` JSON wrappers, `json.RawMessage`, `net/http`, `bufio`, existing OpenAI Responses relay.

---

## File Structure

- Create `relay/channel/deepseek/responses_lite_bridge.go` for request, response-item, SSE, and response-body conversion.
- Create `relay/channel/deepseek/responses_lite_bridge_test.go` for pure conversion tests.
- Modify `relay/channel/deepseek/adaptor.go` only for activation and delegation.
- Modify `relay/channel/deepseek/adaptor_test.go` for mapped-model integration and isolation.

### Task 1: Request Definitions and Instructions

**Files:**
- Create: `relay/channel/deepseek/responses_lite_bridge.go`
- Create: `relay/channel/deepseek/responses_lite_bridge_test.go`

- [ ] **Step 1: Write the failing capture-derived tests**

Construct a request containing `additional_tools` with custom `exec`, function
`wait`, and function `request_user_input`, followed by the adjacent base
developer message. Assert activation, removal of `additional_tools`, instruction
lifting, preservation of remaining input order and `parallel_tool_calls: false`,
and the following `exec` definition:

```go
map[string]any{
    "type": "function",
    "name": "exec",
    "description": "Run JavaScript",
    "parameters": map[string]any{
        "type": "object",
        "properties": map[string]any{
            "source": map[string]any{
                "type": "string",
                "description": "JavaScript source to execute.",
            },
        },
        "required": []any{"source"},
        "additionalProperties": false,
    },
    "strict": true,
}
```

Add cases for existing-tool precedence, existing instructions, malformed tool
arrays, and requests without custom `exec` remaining byte-for-byte unchanged.

- [ ] **Step 2: Run the focused test and verify RED**

```bash
go test ./relay/channel/deepseek -run 'TestNormalizeDeepSeekResponsesLiteBridgeRequest' -count=1
```

Expected: build failure because the normalizer is undefined.

- [ ] **Step 3: Implement atomic normalization**

Define:

```go
const (
    responsesLiteAdditionalToolsType = "additional_tools"
    responsesLiteExecToolName = "exec"
    responsesLiteBridgeContextKey = "deepseek_responses_lite_bridge"
)

func normalizeDeepSeekResponsesLiteBridgeRequest(
    request *dto.OpenAIResponsesRequest,
) (bool, error)
```

Decode input into `[]json.RawMessage`; first detect a custom `exec` inside an
`additional_tools.tools` array. Return `(false, nil)` without mutation when the
signature is absent. For an active request, build all replacement values
locally, convert only custom `exec` to a function with a required string
`source` and `additionalProperties: false`, preserve ordinary functions, merge
tools by `type + "\x00" + name`, and lift only the eligible adjacent developer
message. Assign `Input`, `Tools`, and `Instructions` only after all operations
succeed. Use only `common.Marshal` and `common.Unmarshal` for JSON operations.

- [ ] **Step 4: Run the request tests and verify GREEN**

```bash
go test ./relay/channel/deepseek -run 'TestNormalizeDeepSeekResponsesLiteBridgeRequest' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/channel/deepseek/responses_lite_bridge.go relay/channel/deepseek/responses_lite_bridge_test.go
git commit -m "feat(deepseek): translate Responses Lite exec definition"
```

### Task 2: Multi-Turn History

**Files:**
- Modify: `relay/channel/deepseek/responses_lite_bridge.go`
- Modify: `relay/channel/deepseek/responses_lite_bridge_test.go`

- [ ] **Step 1: Write failing history tests**

Use this input pair after the Lite tool declaration:

```json
{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"exec","input":"text(\"ok\");","status":"completed"}
{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"}
```

Require `function_call` with JSON-string arguments
`{"source":"text(\"ok\");"}` and matching `function_call_output`. Assert that
unrelated function history and unmatched custom output remain unchanged, and
that malformed matched input returns an error without request mutation.

- [ ] **Step 2: Run the history tests and verify RED**

```bash
go test ./relay/channel/deepseek -run 'TestNormalizeDeepSeekResponsesLiteBridgeRequest.*History' -count=1
```

Expected: FAIL because custom history is not converted.

- [ ] **Step 3: Implement call-ID-aware conversion**

Record `call_id` for each custom call named `exec`. Decode its input as a
string, encode arguments with:

```go
data, err := common.Marshal(map[string]string{"source": source})
```

Change the item type to `function_call`, store the encoded JSON as the string
`arguments`, and remove `input`. On a second pass, change only matching
`custom_tool_call_output` items to `function_call_output`. Preserve all other
raw fields.

- [ ] **Step 4: Run all request tests**

```bash
go test ./relay/channel/deepseek -run 'TestNormalizeDeepSeekResponsesLiteBridgeRequest' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/channel/deepseek/responses_lite_bridge.go relay/channel/deepseek/responses_lite_bridge_test.go
git commit -m "feat(deepseek): translate Responses Lite exec history"
```

### Task 3: Non-Streaming Response Restoration

**Files:**
- Modify: `relay/channel/deepseek/responses_lite_bridge.go`
- Modify: `relay/channel/deepseek/responses_lite_bridge_test.go`

- [ ] **Step 1: Write failing output tests**

Feed this response item to the transformer:

```json
{"type":"function_call","id":"fc_123","call_id":"call_123","name":"exec","arguments":"{\"source\":\"text(\\\"OK\\\");\"}","status":"completed"}
```

Require `type: custom_tool_call`, `id: ctc_call_123`, string `input`, preserved
call ID/status, and no `arguments`. Cover non-exec pass-through and malformed or
missing `source` errors.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./relay/channel/deepseek -run 'TestTransformDeepSeekResponsesLiteResponse' -count=1
```

Expected: build failure because the response transformer is undefined.

- [ ] **Step 3: Implement raw response conversion**

Add:

```go
func transformDeepSeekResponsesLiteResponse(data []byte) ([]byte, error)
func transformDeepSeekResponsesLiteOutputItem(
    item map[string]json.RawMessage,
) (map[string]json.RawMessage, bool, error)
```

For `function_call/exec`, decode the JSON string in `arguments`, require its
`source` field to be a string, set `type`, `id`, and `input`, then delete
`arguments`. Walk top-level `output` as raw maps and return the original bytes
when nothing changed.

- [ ] **Step 4: Run and verify GREEN**

```bash
go test ./relay/channel/deepseek -run 'TestTransformDeepSeekResponsesLiteResponse' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/channel/deepseek/responses_lite_bridge.go relay/channel/deepseek/responses_lite_bridge_test.go
git commit -m "feat(deepseek): restore Responses Lite exec calls"
```

### Task 4: Streaming SSE Restoration

**Files:**
- Modify: `relay/channel/deepseek/responses_lite_bridge.go`
- Modify: `relay/channel/deepseek/responses_lite_bridge_test.go`

- [ ] **Step 1: Write failing state-machine tests**

Feed `output_item.added(function_call exec)`, three split argument deltas,
`function_call_arguments.done`, `output_item.done`, and `response.completed`.
Require the sequence:

```text
response.output_item.added(custom_tool_call exec)
response.custom_tool_call_input.delta
response.custom_tool_call_input.done
response.output_item.done(custom_tool_call exec)
response.completed(with converted embedded output)
```

Require consistent `ctc_<call_id>` references, exactly one pair of custom input
events, no downstream function-argument events for `exec`, semantic pass-through
for non-exec events, and a clear error for malformed complete arguments.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./relay/channel/deepseek -run 'TestDeepSeekResponsesLiteSSE' -count=1
```

Expected: build failure because the SSE transformer is undefined.

- [ ] **Step 3: Implement the event state machine**

Define:

```go
type responsesLiteExecStreamCall struct {
    upstreamItemID string
    downstreamItemID string
    callID string
    outputIndex int
    arguments strings.Builder
    inputEmitted bool
}

type responsesLiteSSETransformer struct {
    callsByItemID map[string]*responsesLiteExecStreamCall
}

func (t *responsesLiteSSETransformer) Transform(data string) ([]string, error)
```

Track only item-added events for `function_call/exec`. Suppress their argument
deltas while buffering them. On argument-done, prefer its complete `arguments`
or fall back to the buffer, then emit custom input delta/done exactly once.
Convert item-done and terminal embedded response output using Task 3's item
helper. Return the original data string for unrelated events.

- [ ] **Step 4: Implement an incremental response-body wrapper**

Define an `io.ReadCloser` with an inner `bufio.Scanner`, transformer, pending
buffer, and terminal error. Its `Read` method scans until it can return output,
turns each transformed JSON event into `data: <json>\n\n`, passes `[DONE]`, and
ignores upstream `event:` lines because the existing handler recreates them
from JSON `type`. Set the scanner maximum to 64 MiB and make `Close` close the
original body.

- [ ] **Step 5: Run SSE and race tests**

```bash
go test ./relay/channel/deepseek -run 'TestDeepSeekResponsesLiteSSE' -count=1
go test -race ./relay/channel/deepseek -run 'ResponsesLite' -count=1
```

Expected: PASS with no races.

- [ ] **Step 6: Commit**

```bash
git add relay/channel/deepseek/responses_lite_bridge.go relay/channel/deepseek/responses_lite_bridge_test.go
git commit -m "feat(deepseek): restore Responses Lite exec SSE events"
```

### Task 5: Adaptor Activation and Isolation

**Files:**
- Modify: `relay/channel/deepseek/adaptor.go`
- Modify: `relay/channel/deepseek/adaptor_test.go`

- [ ] **Step 1: Write failing request integration tests**

Use Gin test contexts and `RelayInfo` with `RelayModeResponses`. Prove that
`IsModelMapped: true`, origin `gpt-5.6-luna`, and upstream
`deepseek-v4-flash` activates normalization and stores the context flag. Prove
that `IsModelMapped: false` and a non-Lite request do not activate it. Keep the
existing explicit-zero and reasoning-suffix assertions.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./relay/channel/deepseek -run 'TestConvertOpenAIResponsesRequest.*ResponsesLite' -count=1
```

Expected: FAIL because the adaptor does not call the bridge.

- [ ] **Step 3: Integrate mapped request activation**

Use this gate before the existing reasoning suffix conversion:

```go
if info != nil && info.IsModelMapped {
    enabled, err := normalizeDeepSeekResponsesLiteBridgeRequest(&request)
    if err != nil {
        return nil, fmt.Errorf("normalize DeepSeek Responses Lite bridge request: %w", err)
    }
    if enabled && c != nil {
        c.Set(responsesLiteBridgeContextKey, true)
    }
}
```

- [ ] **Step 4: Write failing response integration tests**

Build streaming and non-streaming `http.Response` fixtures. Require restoration
only when the private context flag is set and unchanged delegation otherwise.

```bash
go test ./relay/channel/deepseek -run 'TestDoResponse.*ResponsesLite' -count=1
```

Expected: FAIL before response wrapping is connected.

- [ ] **Step 5: Integrate response wrapping**

Before delegating to `openai.Adaptor.DoResponse`, branch only when relay mode is
Responses and the context flag is true. For streams, replace `resp.Body` with
the Task 4 wrapper. For non-streams, read and transform the body, replace it
with `io.NopCloser(bytes.NewReader(data))`, update `ContentLength`, and delete
the stale `Content-Length` header. Convert transformation failures into
`types.ErrorCodeBadResponseBody` with HTTP 502.

- [ ] **Step 6: Run focused verification**

```bash
go test ./relay/channel/deepseek -count=1
go test -race ./relay/channel/deepseek -count=1
go vet ./relay/channel/deepseek
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add relay/channel/deepseek/adaptor.go relay/channel/deepseek/adaptor_test.go
git commit -m "feat(deepseek): bridge mapped Responses Lite exec calls"
```

### Task 6: Docker and Real Curl Verification

**Files:**
- No source files expected.
- Do not modify root-workspace `docker-compose.yml`, deployment notes, databases, or volumes.

- [ ] **Step 1: Format and run local verification**

```bash
gofmt -w relay/channel/deepseek/responses_lite_bridge.go relay/channel/deepseek/responses_lite_bridge_test.go relay/channel/deepseek/adaptor.go relay/channel/deepseek/adaptor_test.go
git diff --check
go test ./service/codexchat ./relay/channel/deepseek -count=1
go test -race ./relay/channel/deepseek -count=1
go vet ./relay/channel/deepseek
```

Expected: all PASS.

- [ ] **Step 2: Run repository tests**

```bash
go test ./... -count=1
```

Expected: no new failures. Report the already reproduced failures in
`relay/channel/claude` and `relay/helper` separately if they remain.

- [ ] **Step 3: Build and deploy an isolated image**

```bash
docker build -t new-api:deepseek-responses-lite-bridge .
```

Preserve the stable image tag. Recreate only the application container using a
temporary Compose override or equivalent command; keep PostgreSQL, Redis, and
their volumes. Do not edit the user's compose file. Verify
`curl --fail --silent http://127.0.0.1:3000/api/status` returns HTTP 200.

- [ ] **Step 4: Test non-streaming curl**

Send the capture-derived Lite body to local `/v1/responses` with the existing
test token, model `gpt-5.6-luna`, `stream: false`, and `tool_choice: auto`.
Require HTTP 200 and `output[].type == custom_tool_call`, name `exec`, and input
containing the requested JavaScript. Reject DSML assistant text.

- [ ] **Step 5: Test streaming curl**

Repeat with `stream: true` and `curl -N`. Require events
`response.output_item.added`, `response.custom_tool_call_input.delta`,
`response.custom_tool_call_input.done`, `response.output_item.done`, and
`response.completed`. Require no `response.function_call_arguments.*` events
for `exec` and no DSML assistant text.

- [ ] **Step 6: Test one history round-trip**

Submit the returned custom call plus a matching `custom_tool_call_output` in a
new Lite request. Require HTTP 200 rather than an invalid input-item error.

- [ ] **Step 7: Handle any upstream-discovered correction with TDD**

For each real-upstream discrepancy, first add one focused failing test, then
make the smallest correction, rerun Steps 1-6, and commit it. If no correction
is needed, leave the worktree clean.
