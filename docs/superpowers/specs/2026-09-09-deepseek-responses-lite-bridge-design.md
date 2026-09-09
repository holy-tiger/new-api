# DeepSeek Responses Lite Bidirectional Bridge Design

## Goal

Allow a Codex client that sends the GPT Responses Lite tool contract to use a
model mapped to a native DeepSeek Responses model. The gateway must translate
the Lite `exec` custom tool into the function tool DeepSeek accepts and restore
DeepSeek's function-call response to the custom-tool protocol Codex expects.

The bridge must work for non-streaming responses, streaming SSE responses, and
subsequent requests containing tool-call history. It must not reconstruct tools
that the Lite client did not register.

## Evidence and Root Cause

The supplied captures show two different Codex request contracts:

- The DeepSeek-native request has top-level `instructions`, 15 top-level
  `tools`, and `parallel_tool_calls: true`.
- The GPT Responses Lite request begins with an `additional_tools` input item.
  It registers `custom exec`, `function wait`, and
  `function request_user_input`, followed by a developer message containing the
  base instructions. It sets `parallel_tool_calls: false`.
- The base developer text is exactly equal to the native capture's top-level
  `instructions`.

The first implementation promoted the three Lite definitions without changing
their protocol type. A real request to DeepSeek then failed with:

```text
Unsupported custom tool: 'exec'. Only 'apply_patch' is supported.
```

Changing only `exec` to a function tool with a required `source` string made
the same upstream return a structured `function_call` whose arguments contained
the expected JavaScript source. A separate test established that thinking mode
rejects `tool_choice: required`, while the captured `tool_choice: auto` works.

The root cause is therefore a bidirectional protocol mismatch, not missing
prompt text: Codex Lite registers and consumes a custom tool, while DeepSeek can
invoke the same logical operation only as a function tool.

## Selected Approach

Add a DeepSeek-specific, bidirectional bridge around the existing native
Responses relay:

```text
Codex Lite                                      DeepSeek Responses

custom exec definition     -- request -->      function exec(source)
custom_tool_call history    -- request -->      function_call history
custom_tool_call_output     -- request -->      function_call_output

custom_tool_call exec       <-- response --     function_call exec
custom input                <-- response --     arguments.source
custom input SSE events     <-- response --     function argument SSE events
```

The bridge remains on the native `/responses` route. It does not downgrade the
whole request to Chat Completions. Existing `service/codexchat` custom-tool
conversion is the protocol reference, especially for custom-tool item fields
and SSE event names, but the DeepSeek bridge owns only the narrow `exec`
translation.

## Activation and Isolation

Request conversion activates only when all of these conditions hold:

1. The selected channel uses the DeepSeek adaptor. This includes requests that
   reached the adaptor through model mapping.
2. `input` is an array containing an `additional_tools` item.
3. That item contains a custom tool named `exec`.

When activated, request conversion records a private flag in the current Gin
context. `DoResponse` checks the same flag before wrapping the upstream
response. Ordinary DeepSeek-native requests and Responses requests without the
Lite signature continue through the existing OpenAI response handler unchanged.

Body pass-through mode intentionally bypasses adaptor conversion, so the bridge
also remains inactive in pass-through mode.

## Request Transformation

### Tool definitions

For each `additional_tools` item:

- Convert `{"type":"custom","name":"exec",...}` into a top-level
  Responses function tool named `exec`.
- Preserve its description.
- Replace its custom grammar/format with this function schema:

```json
{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "JavaScript source to execute."
    }
  },
  "required": ["source"],
  "additionalProperties": false
}
```

- Preserve regular function definitions such as `wait` and
  `request_user_input`.
- Remove processed `additional_tools` items from `input`.
- Merge the resulting definitions with existing top-level tools. Existing
  top-level definitions remain first and win conflicts identified by type and
  name.

The bridge does not force `tool_choice` or `parallel_tool_calls`; explicit
values from the client are preserved. In particular, it does not introduce
`tool_choice: required` for thinking models.

### Instructions

If top-level `instructions` is absent, the bridge inspects the message
immediately following the first `additional_tools` item. If it is a developer
message containing exactly one string `input_text` part, the text is moved to
top-level `instructions` and that message is removed from `input`.

Existing instructions are never overwritten. All unrelated input items retain
their order and JSON content.

### Tool-call history

While walking the input array, the bridge also converts Lite history for the
logical `exec` tool:

- `custom_tool_call` named `exec` becomes `function_call` named `exec`.
- Its `input` value becomes the JSON-string `arguments` value
  `{"source": <input>}`.
- The bridge records the converted call's `call_id`.
- A `custom_tool_call_output` whose `call_id` matches a converted `exec` call
  becomes `function_call_output`.
- `call_id`, item IDs, status, and output content are preserved.

An unmatched `custom_tool_call_output` is left unchanged because the bridge
cannot safely infer which custom tool produced it. Malformed matched history
produces a request-conversion error before any upstream request is sent. This
avoids partially translating a conversation.

### JSON behavior

The implementation decodes flexible input and tool items with raw JSON maps so
unknown fields survive. All marshaling and unmarshaling uses `common.Marshal`
and `common.Unmarshal`. Explicit zero and false values on the outer Responses
request remain unchanged.

## Non-Streaming Response Transformation

When the bridge flag is active, the DeepSeek adaptor reads the upstream JSON,
walks `response.output`, and converts only `function_call` items named `exec`:

- `type` becomes `custom_tool_call`.
- `name`, `call_id`, status, and unrelated fields are preserved.
- `arguments` is decoded as JSON and its string `source` field becomes `input`.
- `arguments` is removed from the converted item.
- The downstream item ID is normalized to `ctc_<call_id>`, matching the custom
  tool ID convention used by the existing Codex compatibility bridge.

The transformed body is then passed to the existing OpenAI Responses handler,
which continues to own error handling, usage accounting, and response writing.
All non-`exec` output items pass through unchanged.

If an `exec` call has malformed arguments or lacks a string `source`, response
conversion returns a clear bad-upstream-response error rather than sending an
unusable tool call to Codex.

## Streaming SSE Transformation

The DeepSeek adaptor wraps `resp.Body` with a small incremental SSE
transforming reader, then delegates to the existing OpenAI Responses stream
handler. This preserves streaming, usage accounting, timeout behavior, and the
normal event writer without duplicating the shared handler.

For an `exec` function call, the transformer maintains state keyed by upstream
item ID and output index:

1. `response.output_item.added` is emitted immediately with a
   `custom_tool_call` item and downstream ID `ctc_<call_id>`.
2. `response.function_call_arguments.delta` events are buffered and suppressed,
   because arbitrary JSON fragments cannot be translated safely into source
   fragments.
3. On `response.function_call_arguments.done`, the complete arguments are
   decoded. The transformer emits exactly once:
   - `response.custom_tool_call_input.delta` with the complete source;
   - `response.custom_tool_call_input.done` with the complete source.
   If an upstream omits the argument-done event, the full arguments in
   `response.output_item.done` provide the fallback source.
4. `response.output_item.done` is converted to a completed
   `custom_tool_call` item using the same downstream ID and input.
5. Embedded output in `response.completed` or `response.incomplete` is converted
   by the same item function, so the terminal response agrees with preceding
   events.

Every `item_id` reference is rewritten consistently. Reasoning, text, usage,
errors, and tool calls other than `exec` pass through byte-for-byte at the JSON
field level. The wrapper closes the original body when the delegated handler
closes it.

Malformed completed `exec` arguments stop the transformed stream and record a
scanner/handler error. No fabricated JavaScript is emitted.

## Components

### `relay/channel/deepseek/responses_lite_bridge.go`

Owns:

- Lite signature detection;
- request tool, instruction, and history conversion;
- non-streaming response item conversion;
- stateful SSE event conversion;
- the incremental transforming response-body wrapper.

Keeping the protocol bridge in one focused file makes its activation and
symmetry reviewable without changing general DeepSeek reasoning behavior.

### `relay/channel/deepseek/adaptor.go`

Owns integration only:

- calls request conversion before the existing DeepSeek reasoning-suffix logic;
- stores the per-request bridge flag in Gin context;
- wraps native Responses output only when that flag is present;
- otherwise delegates exactly as before.

### Tests

`relay/channel/deepseek/responses_lite_bridge_test.go` covers the pure request,
response, and SSE transformations. `adaptor_test.go` covers activation through
a mapped model and verifies that standard DeepSeek requests remain untouched.

## Error Handling

- Non-array input, absent `additional_tools`, or no custom `exec` is not a
  bridge target and passes through unchanged.
- Once the Lite signature is recognized, missing/non-array tools, malformed
  matched `exec` history, or invalid top-level tools are request-conversion
  errors.
- A malformed upstream `exec` call is a bad-upstream-response error.
- Unknown fields and future event types pass through unchanged.
- No error path mutates and sends only part of a request; conversion builds new
  raw values and assigns them only after validation succeeds.

## Deferred Full Native Tool Reconstruction

The bridge intentionally does not synthesize the 15 tools in the native
DeepSeek capture. The Lite request does not carry complete definitions for
`collaboration`, `tool_search`, `web_search`, and other native capabilities.
Static definitions would drift with Codex releases, and direct native calls
would still need to be compiled back into valid `exec` JavaScript.

A future full bridge may add a versioned tool registry and JavaScript call
compiler, but it is not required for the three tools the current Lite client
actually registered.

## Testing and Validation

Development follows TDD. Tests are added in this order:

1. Request definition conversion and activation boundaries.
2. Multi-turn custom call/output history conversion.
3. Non-streaming response restoration.
4. Streaming event conversion, ID consistency, buffering, and pass-through.
5. Adaptor integration and mapped-model behavior.
6. Regression coverage for explicit zero/false fields and ordinary DeepSeek
   Responses requests.

Verification consists of:

- focused unit and race tests for `relay/channel/deepseek`;
- existing `service/codexchat` tests, because its custom-tool behavior is the
  protocol reference;
- repository-wide Go tests, accounting for already reproduced baseline failures;
- a Docker image built from this worktree;
- a real curl request through the local mapped model, first non-streaming and
  then streaming;
- final testing with the user's Codex client.

## Success Criteria

- A model-mapped Lite request reaches DeepSeek with `function exec(source)`,
  regular `wait` and `request_user_input` tools, and no `additional_tools` item.
- DeepSeek no longer rejects the request as an unsupported custom `exec` tool.
- DeepSeek's structured `function_call exec` is delivered to Codex as a valid
  `custom_tool_call exec` in both non-streaming and streaming modes.
- A following Lite request containing custom-tool history is accepted after
  conversion to function-call history.
- Ordinary DeepSeek-native Responses traffic remains unchanged.
- The gateway does not invent any of the missing 15 native tools.
