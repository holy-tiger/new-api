# CodeBuddy OpenAI Chat Completions Support Design

## Overview

This design adds **CodeBuddy** as a first-class upstream channel in `new-api` for **OpenAI-compatible clients** that use:

- `POST /v1/chat/completions`
- `GET /v1/models`

The goal is to let clients such as OpenClaw use CodeBuddy through the existing `new-api` gateway without introducing a separate relay stack or a standalone adapter service.

The supported upstream capability in phase 1 is:

- `POST {CODEBUDDY_BASE_URL}/v2/chat/completions`

## Scope

### In Scope

- Add a new channel type: `CodeBuddy`
- Support OpenAI-compatible chat clients through `/v1/chat/completions`
- Reuse the existing OpenAI relay pipeline in `new-api`
- Force upstream requests to use streaming mode when calling CodeBuddy
- Support both client streaming and client non-streaming requests
- Add built-in default model aliases for common OpenAI-style model names
- Inject CodeBuddy-required upstream headers automatically
- Keep existing billing, retry, logging, and usage extraction behavior wherever compatible

### Out of Scope

- `POST /v1/messages`
- `POST /v1/responses`
- Images, audio, embeddings, rerank
- CodeBuddy OAuth flows
- Multi-account rotation
- CodeBuddy usage or credit dashboard pages
- Dynamic model sync from CodeBuddy `/v3/config`
- Declaring tool calling as fully supported

## Background

Analysis of `/home/wenjx/openai_v2` shows that the CodeBuddy backend already accepts an OpenAI-style chat payload at:

- `POST {base}/v2/chat/completions`

The main protocol differences versus a normal OpenAI-compatible upstream are:

- the upstream path is `/v2/chat/completions`, not `/v1/chat/completions`
- a fixed set of CodeBuddy headers is required
- the upstream is expected to run in streaming mode
- common OpenAI model aliases must be mapped to CodeBuddy model IDs

This makes CodeBuddy a good fit for a **special OpenAI-compatible channel type**, not for a new relay architecture.

## Options Considered

### Option 1: New CodeBuddy Channel Type Reusing OpenAI Relay

Add a distinct `ChannelTypeCodeBuddy`, but map it to `APITypeOpenAI` and handle CodeBuddy-specific behavior inside the existing OpenAI adaptor path.

Pros:

- explicit protocol semantics in the admin and backend
- low implementation risk
- avoids duplicating relay and response handling logic
- easy to extend later if CodeBuddy-specific behavior grows

Cons:

- requires a small amount of channel-type plumbing and UI support

### Option 2: Reuse Generic OpenAI Channel with Manual Overrides

Have users configure CodeBuddy through the normal OpenAI channel plus base URL, header override, model mapping, and parameter override.

Pros:

- minimal code change

Cons:

- operationally fragile
- protocol requirements become hidden in manual configuration
- higher support and debugging cost

### Option 3: Build a Dedicated CodeBuddy Relay Stack

Implement a new adaptor or relay path modeled after `openai_v2`.

Pros:

- maximal behavior isolation

Cons:

- duplicates existing `new-api` functionality
- increases maintenance burden
- unnecessary for the current scope

## Decision

Choose **Option 1**.

CodeBuddy will be modeled as a dedicated channel type, while still reusing the existing OpenAI relay stack. This keeps protocol differences explicit without forking the architecture.

## User-Facing Behavior

### Supported Client Surface

Phase 1 supports OpenAI-compatible clients that call:

- `POST /v1/chat/completions`
- `GET /v1/models`

OpenClaw is the primary target. Its configuration in `openai_v2` uses the OpenAI-compatible chat completions surface, which matches this design.

### Streaming Behavior

If the client sends `stream=true`, `new-api` will proxy CodeBuddy as a streaming upstream request and return SSE to the client through the existing streaming path.

### Non-Streaming Behavior

If the client sends `stream=false` or omits `stream`, `new-api` will still call CodeBuddy upstream with `stream=true`, then aggregate the returned stream into a normal OpenAI chat completion response before returning it to the client.

This behavior is required because the analyzed CodeBuddy upstream expects streaming mode.

## Architecture

### Channel Modeling

Add:

- `constant.ChannelTypeCodeBuddy`

Map it in:

- `common.ChannelType2APIType() -> APITypeOpenAI`

Result:

- relay dispatch still uses the existing OpenAI adaptor
- CodeBuddy-specific logic is expressed as targeted channel branches inside that adaptor

### Files Expected to Change

- `constant/channel.go`
- `common/api_type.go`
- `relay/channel/openai/adaptor.go`
- channel-related frontend/admin files for channel type display and selection
- tests covering adaptor URL, header setup, request conversion, and response compatibility

No new relay mode is required.

## Request Routing Design

### Request URL

For `ChannelTypeCodeBuddy`, `relay/channel/openai/adaptor.go` must override the normal upstream path construction and always send requests to:

- `{ChannelBaseURL}/v2/chat/completions`

It must not forward the external client path directly as `/v1/chat/completions`.

### Headers

For `ChannelTypeCodeBuddy`, upstream requests must automatically include:

- `Authorization: Bearer {api_key}`
- `X-Api-Key: {api_key}`
- `Content-Type: application/json`
- `X-Product: SaaS`
- `X-IDE-Type: CLI`
- `X-IDE-Name: CLI`
- `X-IDE-Version: 2.83.1`
- `X-Conversation-ID: {uuid}`
- `X-Conversation-Request-ID: {uuid}`

The two `X-Conversation-*` values should be generated per request inside the adaptor. They should not require admin configuration.

These headers are part of the channel’s built-in protocol behavior and should not depend on manual header override setup.

## Request Conversion Rules

### Shared Request DTO

Continue to use the existing OpenAI request DTOs and conversion path already used by the OpenAI relay flow. No new request DTO is needed.

This preserves existing `new-api` semantics for optional fields and explicit zero values.

### CodeBuddy Request Normalization

For `ChannelTypeCodeBuddy`, `ConvertOpenAIRequest()` should apply the following changes:

- force upstream `stream=true`
- ensure `stream_options.include_usage=true`
- if client provided `stream_options`, override it with `include_usage=true`
- keep regular OpenAI chat fields such as `temperature`, `top_p`, `max_tokens`, `tools`, `tool_choice`, and `user` as pass-through fields for phase 1

The external API remains OpenAI-compatible. Only the upstream request is normalized for CodeBuddy requirements.

## Model Mapping Design

### Default Alias Mapping

Phase 1 should provide built-in default mappings:

- `gpt-4o -> glm-5.0-turbo`
- `gpt-4 -> glm-5.1`
- `gpt-3.5-turbo -> glm-5.0-turbo`
- `deepseek-chat -> deepseek-v3.1`

### Fallback Rule

If the requested model does not match a built-in alias, pass the model name through unchanged.

This allows administrators and clients to use native CodeBuddy model IDs directly, such as:

- `glm-5.0-turbo`
- `glm-5.1`
- `deepseek-v3.1`

### Configuration Principle

The default mapping should be integrated into the existing project model-mapping path rather than hardcoded as an isolated one-off behavior with no override path.

## Response Handling Design

Phase 1 will reuse the current OpenAI chat response handlers.

This includes:

- streaming SSE relay
- non-stream aggregation
- existing usage extraction
- existing billing settlement path
- existing retry and logging path

No dedicated CodeBuddy response handler should be introduced in phase 1 unless validation reveals a hard incompatibility.

### Usage Field Compatibility

CodeBuddy may include an additional `credit` field in `usage`.

Phase 1 requirement:

- parsing must not fail because of extra usage fields

If the extra field is not yet surfaced in `new-api` accounting or response payloads, that is acceptable for phase 1 as long as the request succeeds and standard usage parsing remains intact.

## Error Handling

Phase 1 prioritizes **OpenAI-compatible client behavior** over CodeBuddy-specific error fidelity.

Known upstream error cases from `openai_v2` analysis include:

- `11101`: upstream non-stream request not supported
- `11102`: model not found

### Handling Policy

- keep the standard OpenAI error response structure exposed by `new-api`
- do not introduce a full separate CodeBuddy error taxonomy in phase 1
- avoid surfacing `11101` to clients by always forcing upstream streaming
- if practical in the adaptor or shared error path, map model-not-found to the existing OpenAI-style model error semantics

The main phase 1 requirement is that clients receive stable, standard OpenAI-style errors rather than raw CodeBuddy protocol leakage.

## `/v1/models` Design

Phase 1 supports the external `/v1/models` surface, but it does **not** depend on dynamic upstream model discovery.

Reason:

- prior analysis showed `GET {base}/v3/config` may return `models: null` in some environments

### Phase 1 Policy

- do not integrate CodeBuddy `/v3/config` into model sync or dynamic model discovery
- rely on static recommended models and direct native model pass-through

This avoids coupling the feature to an unreliable upstream capability.

## Tool Calling Position

Tool-related request fields may be passed through during phase 1, but tool calling must **not** be declared fully supported without dedicated validation.

Documentation and testing priority should remain:

- first-class support for text chat completions
- tool calling compatibility treated as unverified or best-effort until tested

## Risks

### Risk 1: Forced Upstream Streaming vs Local Non-Stream Aggregation

The most important behavioral risk is whether the existing non-stream aggregation path works cleanly with CodeBuddy’s streaming-only upstream behavior.

Mitigation:

- add explicit non-stream compatibility tests for CodeBuddy

### Risk 2: SSE Chunk Shape Differences

The second major risk is whether CodeBuddy’s streamed response and final usage chunk exactly match the assumptions in the current OpenAI chat handlers.

Mitigation:

- add targeted stream parsing and usage extraction tests

### Risk 3: Extra Usage Fields

Additional fields such as `credit` may be dropped or ignored.

Mitigation:

- ensure unknown fields do not break parsing
- defer exposing `credit` as a formal phase 1 feature

## Test Plan

Minimum required test coverage:

### Adaptor URL Tests

- CodeBuddy channel routes to `/v2/chat/completions`

### Header Tests

- `Authorization` is set correctly
- `X-Api-Key` is set correctly
- `X-Product` and `X-IDE-*` headers are set
- `X-Conversation-ID` and `X-Conversation-Request-ID` are generated

### Request Conversion Tests

- client `stream=false` still becomes upstream `stream=true`
- `stream_options.include_usage=true` is enforced
- default model aliases map to expected CodeBuddy model IDs
- unknown models pass through unchanged

### Response Compatibility Tests

- streaming responses pass through the existing OpenAI SSE path
- non-stream requests aggregate correctly into a standard OpenAI response
- final usage chunk with extra fields does not break parsing

## Rollout Criteria

Phase 1 is considered successful when all of the following are true:

- OpenClaw can use `new-api` against a CodeBuddy channel through `/v1/chat/completions`
- plain text chat works
- both streaming and non-streaming client modes work
- common alias models work
- invalid or missing models produce a stable OpenAI-style error response

The following are explicitly not required for phase 1 completion:

- guaranteed tool calling support
- dynamic `/v1/models` sync from upstream
- Claude-compatible client support
- Responses API compatibility
- OAuth or account rotation

## Implementation Summary

The implementation should treat CodeBuddy as:

- a **new channel type**
- a **specialized OpenAI-compatible upstream**
- **not** a new relay subsystem

This preserves architectural simplicity while making the CodeBuddy protocol requirements explicit, testable, and maintainable inside `new-api`.
