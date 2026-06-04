# DeepSeek Codex Responses Auto-Bridge Design

## Goal

When a Codex client sends `POST /v1/responses` to a DeepSeek channel, `new-api` should automatically use the existing `Responses -> Chat Completions -> Responses` bridge instead of calling the DeepSeek adaptor's unimplemented native Responses path.

## Scope

In scope:

- DeepSeek channel type only
- Existing `/v1/responses` bridge path only
- Existing `codexchat` request/response transforms only
- Focused tests for routing behavior

Out of scope:

- New native DeepSeek Responses support
- Changes to non-DeepSeek providers
- Expansion of bridge feature coverage beyond current `codexchat` limits
- `/v1/responses/compact` support changes

## Design

The bridge decision currently depends on the global `responses_to_chat_completions_policy`. The new behavior adds a provider-specific fallback: if the selected channel type is DeepSeek, `/v1/responses` should automatically take the existing bridge path even when the global policy is disabled.

The global policy remains valid and should keep working for every provider. DeepSeek auto-bridging is a narrow compatibility default layered under the current policy system, not a replacement for it.

## Constraints

This change does not mean DeepSeek gains full native OpenAI Responses support. It only means DeepSeek channels reuse the current compatibility bridge. Any request shape the bridge already rejects must continue to reject explicitly, such as unsupported Responses-native item types.

## Files

- Modify `service/openaicompat/policy.go` or the thin service wrapper that decides whether `/v1/responses` should bridge to chat completions
- Keep `relay/responses_handler.go` unchanged unless a small call-site adjustment is clearly better
- Add focused tests near the bridge policy logic

## Test Strategy

Add tests for:

1. DeepSeek channel returns `true` for Responses-to-Chat bridging when the global policy is disabled
2. Non-DeepSeek channels still return `false` when the global policy is disabled
3. Existing explicit global policy behavior still works for non-DeepSeek providers

## Risks

- If the DeepSeek fallback is added too broadly, other providers could start bridging unexpectedly
- If the fallback bypasses the existing policy matcher incorrectly, model-pattern behavior for non-DeepSeek channels could regress

## Acceptance Criteria

- Codex `POST /v1/responses` requests routed to DeepSeek no longer fail with local `not implemented`
- Non-DeepSeek channel behavior is unchanged unless already covered by explicit bridge policy
- Focused tests cover the new fallback and the unchanged legacy behavior
