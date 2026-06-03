# Codex Tool Compatibility Design

## Context

The current `Responses -> Chat Completions -> Responses` bridge in `new-api` can handle plain text responses and basic `function_call` items, but it does not preserve several Codex-specific tool semantics that `cc-switch` restores in its local proxy layer.

This causes Codex requests that rely on typed tool items to degrade into generic `function_call` output or mismatched streaming events. In practice, Codex can fail to continue execution or render no visible result because the bridge no longer matches the protocol shape it expects.

The missing compatibility is limited to Codex protocol tool variants, not OpenAI hosted tools.

## Goal

Add Codex tool compatibility to the existing Go bridge so that `new-api` correctly preserves request, non-streaming response, and streaming SSE semantics for:

- `custom_tool_call` / `custom_tool_call_output`
- `tool_search_call` / `tool_search_output`
- namespace-backed function tools

## Non-Goals

This design does not add support for OpenAI hosted tools or multi-step hosted tool execution. Explicitly out of scope:

- `web_search`
- `file_search`
- `computer_use`
- `image_generation`
- hosted `local_shell`
- tool execution loops that require the proxy to run the tool and feed results back into the model

## Problem Summary

Today the bridge mostly uses a single mapping:

- request side: convert all tool-like items into generic Chat Completions function tools
- response side: restore almost everything as `function_call`
- stream side: emit only `response.function_call_arguments.*` tool delta events

That is enough for plain function tools, but it is not enough for Codex-native tool variants:

- `custom_tool_call` should carry `input`, not generic `arguments`
- `tool_search_call` should restore its original type, not appear as a plain `function_call`
- namespace-backed tools must round-trip both `name` and `namespace`
- streaming custom tool calls need `response.custom_tool_call_input.delta/done`, not only `response.function_call_arguments.delta/done`

## Reference Pattern From cc-switch

`cc-switch` solves this with a typed tool context that:

- classifies tool kinds separately (`function`, `custom`, `tool_search`, namespace-backed function)
- stores a reversible mapping from chat-visible function names back to the original Codex tool metadata
- uses the same context in both non-streaming and streaming response restoration

`cc-switch` also emits different SSE event families for different tool kinds, especially custom tools.

This design follows that pattern while keeping the existing Go structure.

## Proposed Approach

Use a typed `ChatToolContext` in `service/codexchat` as the single source of truth for Codex tool restoration.

The bridge will:

1. Build richer tool metadata during `Responses -> Chat` conversion.
2. Preserve that metadata through the upstream Chat request lifecycle.
3. Use the same metadata in:
   - non-streaming `Chat -> Responses` restoration
   - streaming `Chat SSE -> Responses SSE` restoration

This keeps current bridge architecture intact while aligning protocol semantics with the `cc-switch` reference.

## Design Details

### 1. Typed Tool Context

Extend `ChatToolContext` so it stores more than a `chatName -> responseType` string.

Each tracked tool needs:

- tool kind
  - `function`
  - `custom`
  - `tool_search`
- original tool name
- optional namespace
- chat-visible function name

This enables reversible mapping in both directions:

- request direction: original Codex tool -> chat-visible function tool
- response direction: chat-visible function call -> original Codex tool item

Namespace-backed tools need a reversible flattening strategy such as `namespace__name`, but the exact flattened form is internal. The original `name` and `namespace` must be recoverable when rebuilding Responses output.

### 2. Request Conversion Rules

File: `service/codexchat/request_transform.go`

#### `custom_tool_call`

Input shape:

- `type: "custom_tool_call"`
- `call_id`
- `name`
- `input`

Convert to a Chat tool call with:

- `type: "function"`
- `function.name = original name`
- `function.arguments = {"input": <input>}`

This mirrors the `cc-switch` pattern and preserves the custom tool payload in a form Chat Completions can carry.

#### `tool_search_call`

Input shape:

- `type: "tool_search_call"`
- `call_id`
- `arguments`

Convert to a Chat tool call with:

- `type: "function"`
- `function.name = internal proxy function name`
- `function.arguments = original arguments`

The bridge must remember that this chat-visible function is actually a `tool_search_call`.

#### Namespace-backed function tools

Tool definitions that include namespace metadata are flattened into a chat-visible function name for upstream compatibility.

The context must remember:

- original tool name
- original namespace
- flattened chat-visible name

When the upstream later returns a chat function call for that flattened name, the bridge must restore:

- `type: "function_call"`
- original `name`
- original `namespace`

#### Tool output items

The request converter must also preserve output item semantics:

- `function_call_output` stays a standard tool message
- `custom_tool_call_output` becomes a tool message without discarding that it originated from a custom tool
- `tool_search_output` becomes a tool message without discarding that it originated from tool search

No hosted tool behavior is inferred from these items.

### 3. Non-Streaming Response Restoration

File: `service/codexchat/response_transform.go`

Current behavior restores most upstream tool calls as generic `function_call`.

The new behavior must branch by tool kind:

#### Standard function tool

Restore:

- `type: "function_call"`
- `call_id`
- `name`
- `arguments`

#### Namespace-backed function tool

Restore:

- `type: "function_call"`
- `call_id`
- `name`
- `namespace`
- `arguments`

#### `custom_tool_call`

Restore:

- `type: "custom_tool_call"`
- `call_id`
- `name`
- `input`
- `status`

Important: the output field must be `input`, not `arguments`.

#### `tool_search_call`

Restore:

- `type: "tool_search_call"`
- `call_id`
- `arguments`
- `status`

If no matching tool metadata exists, the bridge must still fall back to generic `function_call` behavior for safety.

### 4. Streaming SSE Restoration

File: `service/codexchat/stream_transform.go`

The stream transformer must keep the recently fixed text protocol events:

- `response.created`
- `response.content_part.added`
- stable `output_index`
- `response.output_text.done`
- `response.output_item.done`

On top of that, tool events must become type-aware.

#### Standard function tool and namespace-backed function tool

Keep:

- `response.output_item.added`
- `response.function_call_arguments.delta`
- `response.function_call_arguments.done`
- `response.output_item.done`

But namespace-backed items must restore namespace metadata inside `item`.

#### `custom_tool_call`

Use:

- `response.output_item.added`
- `response.custom_tool_call_input.delta`
- `response.custom_tool_call_input.done`
- `response.output_item.done`

Do not emit only `response.function_call_arguments.*` for custom tools.

The `item` shape in `output_item.added/done` must use:

- `type: "custom_tool_call"`
- `call_id`
- `name`
- `input` on completion

#### `tool_search_call`

Use:

- `response.output_item.added`
- `response.function_call_arguments.delta`
- `response.function_call_arguments.done`
- `response.output_item.done`

But the `item.type` must restore to `tool_search_call`, not `function_call`.

### 5. Error Handling

This feature must not widen the blast radius for unsupported tool kinds.

Rules:

- unknown or untracked tool names fall back to generic `function_call`
- unsupported hosted tools remain unchanged and unsupported
- malformed custom tool arguments should degrade safely rather than panic
- empty custom tool input is valid and should round-trip as an empty string

### 6. Backward Compatibility

The change must preserve:

- existing plain text response handling
- existing reasoning handling
- existing plain `function_call` behavior
- current DeepSeek auto bridge behavior
- current developer role normalization

Pure text Codex requests must behave exactly as they do now.

## File-Level Responsibilities

### `service/codexchat/request_transform.go`

Owns:

- tool context creation
- request-side tool flattening
- request-side output item to chat message conversion

### `service/codexchat/response_transform.go`

Owns:

- non-streaming chat response to responses output restoration

### `service/codexchat/stream_transform.go`

Owns:

- streaming chat SSE to responses SSE restoration
- typed tool delta event selection

### Tests

- `service/codexchat/request_transform_test.go`
- `service/codexchat/response_transform_test.go`
- `service/codexchat/stream_transform_test.go`

## Testing Strategy

Use TDD and add failing tests before implementation.

### Request conversion tests

Add tests proving:

- `custom_tool_call` becomes chat arguments with `input`
- `tool_search_call` maps to the internal proxy function name
- namespace-backed tools flatten into reversible chat names
- `custom_tool_call_output` and `tool_search_output` convert into valid tool messages

### Non-streaming response tests

Add tests proving:

- custom tools restore to `custom_tool_call`
- custom tool outputs use `input`, not `arguments`
- tool search restores to `tool_search_call`
- namespace-backed tools restore both `name` and `namespace`
- generic fallback still works when no context exists

### Streaming response tests

Add tests proving:

- custom tool calls emit `response.custom_tool_call_input.delta`
- custom tool calls emit `response.custom_tool_call_input.done`
- tool search item type restores to `tool_search_call`
- namespace-backed items restore namespace metadata
- generic function call behavior remains unchanged

## Success Criteria

The work is successful when:

- Codex requests that rely on `custom_tool_call`, `tool_search_call`, or namespace tools no longer degrade into generic `function_call` semantics
- Codex receives the correct non-streaming output item types and fields
- Codex receives the correct streaming SSE event families for custom tools
- existing pure text requests and generic function tools continue to pass current tests

## Risks

### Risk: breaking existing generic function tool behavior

Mitigation:

- keep fallback behavior for unknown tools
- add explicit regression tests for plain `function_call`

### Risk: stream event shape mismatch

Mitigation:

- add event-name-level assertions in streaming tests
- keep current text event ordering unchanged

### Risk: overreaching into hosted tools

Mitigation:

- explicitly exclude hosted tools from this scope
- do not add heuristic handling for `web_search` or `file_search`

## Recommended Implementation Sequence

1. Extend tool context and request-side mappings.
2. Add failing request conversion tests.
3. Add failing non-streaming restoration tests.
4. Add failing streaming restoration tests.
5. Implement non-streaming typed restoration.
6. Implement streaming typed SSE restoration.
7. Run focused tests, then full `service/codexchat` tests.

## Scope Check

This design stays focused on a single subsystem: Codex tool protocol compatibility within the existing `codexchat` bridge. It does not require decomposition into separate features.
