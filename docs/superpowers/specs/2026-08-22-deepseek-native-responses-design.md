# DeepSeek Native Responses Design

Date: 2026-08-22

Status: Approved for implementation

## Context

The current DeepSeek channel predates DeepSeek's native Responses API. Every
DeepSeek `POST /v1/responses` request is therefore forced through the existing
`Responses -> Chat Completions -> Responses` compatibility bridge. The current
DeepSeek adaptor itself cannot handle a native Responses request: it has no
Responses URL branch and `ConvertOpenAIResponsesRequest` returns
`not implemented`.

DeepSeek channels used by this deployment now support the native Responses API.
The compared upstream project added this support by forwarding Responses
requests directly to `{ChannelBaseUrl}/responses` and using the existing OpenAI
Responses response handlers.

## Goal

Make native DeepSeek Responses the default path while preserving the generic,
explicitly configured Responses-to-Chat fallback for other compatibility use
cases.

## Non-Goals

- Do not remove the generic `ResponsesToChatCompletionsPolicy`.
- Do not remove `relay/responses_via_chat_completions.go` or
  `service/codexchat`.
- Do not change Chat Completions behavior for DeepSeek.
- Do not add automatic retry from native Responses to Chat Completions.
- Do not refactor unrelated relay, billing, or frontend code.
- Do not change `/v1/responses/compact`; DeepSeek remains unsupported for that
  endpoint.

## Selected Approach

Use a minimal provider-adaptor change:

1. Add a DeepSeek `RelayModeResponses` URL branch that targets
   `{ChannelBaseUrl}/responses`.
2. Implement DeepSeek `ConvertOpenAIResponsesRequest` as a native Responses
   pass-through with only DeepSeek V4 reasoning-suffix normalization.
3. Remove the unconditional DeepSeek result from the generic bridge policy.
4. Keep the existing explicit policy matcher so a specifically configured
   channel can still use the Chat fallback.

This approach is preferred over deleting the bridge because the bridge remains
a useful opt-in capability for Chat-only upstreams. It is preferred over an
automatic native-then-Chat retry because retrying streamed or billable requests
can duplicate work and charges.

## Request Flow

The target native flow is:

```text
client POST /v1/responses
  -> ResponsesHelper
  -> model mapping and channel settings
  -> DeepSeek ConvertOpenAIResponsesRequest
  -> POST {ChannelBaseUrl}/responses
  -> shared OpenAI Responses response handler
  -> quota settlement
  -> client
```

`ResponsesHelper` continues to check the generic bridge policy before using the
native path. With the default policy disabled, DeepSeek now follows the native
path. If an administrator explicitly enables the policy for a channel ID,
channel type, and matching model pattern, that channel still follows the Chat
fallback.

## DeepSeek Adaptor Behavior

### URL Selection

For `RelayModeResponses`, `GetRequestURL` returns:

```text
{ChannelBaseUrl}/responses
```

The configured base URL remains authoritative. For example:

- `https://api.deepseek.com` becomes `https://api.deepseek.com/responses`.
- A base URL ending in `/v1` becomes `<base>/v1/responses`.

Existing Claude, Completions, and Chat Completions URL behavior is unchanged.

### Request Conversion

For ordinary model names, the adaptor returns the Responses request without
changing its payload.

For DeepSeek V4 synthetic suffixes, it applies the same model normalization
used by the compared upstream implementation:

- Strip `-none` or `-max` from the upstream model name.
- Ensure `request.reasoning` exists.
- Map the suffix to `reasoning.effort`.
- Map disabled thinking to effort `none`.
- Record the resolved reasoning effort in relay information for logging and
  billing metadata.

Optional scalar fields remain pointer-backed. Explicit values such as
`max_output_tokens: 0`, `temperature: 0`, `top_p: 0`, and `stream: false` must
remain present after conversion and marshal; absent fields remain omitted.

When body pass-through is enabled, request conversion is intentionally skipped
by the existing handler, but the new URL branch still sends the original body to
the native Responses endpoint.

## Response and Billing Flow

No new response protocol is introduced. The DeepSeek adaptor delegates to the
existing OpenAI adaptor, which already selects:

- the non-streaming Responses handler for JSON responses;
- the streaming Responses handler for Responses SSE events.

Existing usage extraction, cached-token normalization, tool-call accounting,
status-code mapping, and quota settlement stay in place. DeepSeek-specific
cached-token handling must not be removed.

## Error Handling

- Local request conversion errors use the existing `convert_request_failed`
  path.
- Upstream non-2xx responses use the existing relay error handler and status
  mapping.
- Malformed native Responses JSON or SSE uses the existing OpenAI Responses
  response errors.
- There is no silent fallback to Chat Completions after a native error.

Avoiding automatic fallback ensures a request is sent upstream at most once and
prevents duplicate streamed output or billing.

## Files

Modify:

- `relay/channel/deepseek/adaptor.go`
  - add native Responses URL selection;
  - implement native Responses request conversion;
  - add DeepSeek V4 Responses reasoning normalization.
- `relay/channel/deepseek/adaptor_test.go`
  - add URL, conversion, suffix, and explicit-zero regression tests.
- `service/openaicompat/policy.go`
  - remove the unconditional DeepSeek bridge decision.
- `service/openaicompat/policy_test.go`
  - replace the forced-fallback expectation with native-default and explicit
    fallback coverage.

No production file outside these focused areas is expected to change.

## Test Strategy

Implementation follows test-driven development. Each behavior is introduced by
a focused failing test before production code changes.

### DeepSeek adaptor tests

1. `RelayModeResponses` resolves to `{ChannelBaseUrl}/responses`.
2. An ordinary Responses request is returned without semantic changes.
3. `deepseek-v4-*-none` strips the suffix and sets reasoning effort to `none`.
4. `deepseek-v4-*-max` strips the suffix and sets reasoning effort to `max`.
5. Explicit zero and false optional values survive conversion and marshal.

### Bridge policy tests

1. DeepSeek does not bridge when the global policy is disabled.
2. An explicitly enabled DeepSeek channel still bridges when its model matches.
3. Non-DeepSeek disabled and explicitly enabled behavior remains unchanged.

### Regression commands

Run focused tests:

```bash
go test ./relay/channel/deepseek ./service/openaicompat ./relay
```

Run the full backend suite:

```bash
go test ./...
```

The baseline full suite currently has unrelated failures: the root package
requires a built `web/dist`, three Claude file-content conversion tests fail,
and one relay helper stream-status test fails. The focused packages above pass
at baseline. Final verification must distinguish new failures from these
recorded baseline failures.

## Rollout Checks

Before production rollout, exercise a configured DeepSeek channel with:

1. A non-streaming text Responses request.
2. A streaming text Responses request through the normal client path.
3. A tool-call request and follow-up tool output.
4. A request using a DeepSeek V4 reasoning suffix.
5. A request that reports cached input tokens, verifying quota settlement.

## Acceptance Criteria

- A DeepSeek `/v1/responses` request uses the native `/responses` upstream
  endpoint by default.
- DeepSeek no longer enters the Chat fallback solely because of its channel
  type.
- Explicit policy configuration can still select the Chat fallback.
- Streaming, non-streaming, model mapping, reasoning metadata, cached tokens,
  and quota settlement continue to use existing shared behavior.
- Focused tests pass without changing unrelated behavior.
