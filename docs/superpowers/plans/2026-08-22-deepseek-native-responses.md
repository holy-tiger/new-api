# DeepSeek Native Responses Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route DeepSeek `/v1/responses` traffic to DeepSeek's native Responses endpoint by default while retaining explicitly configured Responses-to-Chat fallback behavior.

**Architecture:** Extend only the DeepSeek provider adaptor with native Responses URL selection and request normalization, then remove the provider-specific unconditional bridge decision from the generic policy. Continue using the existing shared OpenAI Responses response handlers, billing, and quota settlement without changing the generic compatibility bridge.

**Tech Stack:** Go 1.25.1, Gin, existing relay adaptor interfaces, `common` JSON wrappers, Go `testing`

---

## Preconditions and Baseline

Work in:

```bash
cd /home/wenjx/workspace/new-api/.worktrees/deepseek-native-responses
```

The design is recorded in:

```text
docs/superpowers/specs/2026-08-22-deepseek-native-responses-design.md
```

The focused baseline passes:

```bash
go test ./relay/channel/deepseek ./service/openaicompat ./relay
```

The full baseline has unrelated failures that are outside this plan:

- the root package cannot embed the absent `web/dist` directory;
- three Claude file-content conversion tests fail;
- `TestStreamScannerHandler_StreamStatus_PreInitialized` fails.

Do not change those packages as part of this work.

## File Map

- `relay/channel/deepseek/adaptor.go`: select the native endpoint and normalize native Responses requests.
- `relay/channel/deepseek/adaptor_test.go`: specify native URL, pass-through, explicit-zero, and V4 reasoning behavior.
- `service/openaicompat/policy.go`: make all providers, including DeepSeek, obey the explicit fallback policy.
- `service/openaicompat/policy_test.go`: specify native-by-default DeepSeek routing and explicit fallback behavior.

### Task 1: Route DeepSeek Responses Requests to the Native Endpoint

**Files:**
- Modify: `relay/channel/deepseek/adaptor.go:59-75`
- Test: `relay/channel/deepseek/adaptor_test.go`

- [ ] **Step 1: Write the failing native URL test**

Add the relay constant import and this test to `relay/channel/deepseek/adaptor_test.go`:

```go
import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

func TestGetRequestURL_ResponsesUsesNativeEndpoint(t *testing.T) {
	t.Parallel()

	url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: "https://api.deepseek.com",
		},
	})
	if err != nil {
		t.Fatalf("GetRequestURL returned error: %v", err)
	}
	if url != "https://api.deepseek.com/responses" {
		t.Fatalf("expected native Responses URL, got %q", url)
	}
}
```

- [ ] **Step 2: Format and run the test to verify RED**

Run:

```bash
gofmt -w relay/channel/deepseek/adaptor_test.go
go test ./relay/channel/deepseek -run TestGetRequestURL_ResponsesUsesNativeEndpoint -count=1
```

Expected: FAIL because the adaptor currently returns
`https://api.deepseek.com/v1/chat/completions`.

- [ ] **Step 3: Add the minimal native URL branch**

Change the inner relay-mode switch in `GetRequestURL` to:

```go
		switch info.RelayMode {
		case constant.RelayModeCompletions:
			return fmt.Sprintf("%s/completions", fimBaseUrl), nil
		case constant.RelayModeResponses:
			return fmt.Sprintf("%s/responses", info.ChannelBaseUrl), nil
		default:
			return fmt.Sprintf("%s/v1/chat/completions", info.ChannelBaseUrl), nil
		}
```

- [ ] **Step 4: Format and run the package test to verify GREEN**

Run:

```bash
gofmt -w relay/channel/deepseek/adaptor.go
go test ./relay/channel/deepseek -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the URL behavior**

Run:

```bash
git add relay/channel/deepseek/adaptor.go relay/channel/deepseek/adaptor_test.go
git commit -m "feat(deepseek): route Responses requests natively"
```

### Task 2: Convert Native Responses Requests Without Losing Explicit Values

**Files:**
- Modify: `relay/channel/deepseek/adaptor.go:173-176`
- Test: `relay/channel/deepseek/adaptor_test.go`

- [ ] **Step 1: Write the failing pass-through and explicit-zero test**

Add the `common` import and this test to
`relay/channel/deepseek/adaptor_test.go`:

```go
import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

func TestConvertOpenAIResponsesRequest_PreservesExplicitZeroValues(t *testing.T) {
	t.Parallel()

	zeroUint := uint(0)
	zeroFloat := 0.0
	streamFalse := false
	request := dto.OpenAIResponsesRequest{
		Model:           "deepseek-chat",
		Input:           []byte(`"hello"`),
		MaxOutputTokens: &zeroUint,
		Temperature:     &zeroFloat,
		TopP:            &zeroFloat,
		Stream:          &streamFalse,
		Reasoning:       &dto.Reasoning{Effort: "low"},
	}
	info := &relaycommon.RelayInfo{}

	convertedAny, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
	if err != nil {
		t.Fatalf("ConvertOpenAIResponsesRequest returned error: %v", err)
	}
	converted, ok := convertedAny.(dto.OpenAIResponsesRequest)
	if !ok {
		t.Fatalf("expected dto.OpenAIResponsesRequest, got %T", convertedAny)
	}
	if converted.Model != request.Model {
		t.Fatalf("expected model %q, got %q", request.Model, converted.Model)
	}
	if info.ReasoningEffort != "low" {
		t.Fatalf("expected reasoning effort metadata to be low, got %q", info.ReasoningEffort)
	}

	data, err := common.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal converted request: %v", err)
	}
	var payload map[string]any
	if err := common.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal converted request: %v", err)
	}
	for _, key := range []string{"max_output_tokens", "temperature", "top_p", "stream"} {
		if _, exists := payload[key]; !exists {
			t.Fatalf("expected explicit zero field %q to be preserved in %s", key, data)
		}
	}
}
```

- [ ] **Step 2: Format and run the test to verify RED**

Run:

```bash
gofmt -w relay/channel/deepseek/adaptor_test.go
go test ./relay/channel/deepseek -run TestConvertOpenAIResponsesRequest_PreservesExplicitZeroValues -count=1
```

Expected: FAIL with `ConvertOpenAIResponsesRequest returned error: not implemented`.

- [ ] **Step 3: Implement the minimal native pass-through**

Replace `ConvertOpenAIResponsesRequest` with:

```go
func (a *Adaptor) ConvertOpenAIResponsesRequest(_ *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if info != nil && request.Reasoning != nil {
		info.ReasoningEffort = request.Reasoning.Effort
	}
	return request, nil
}
```

- [ ] **Step 4: Format and run the focused test to verify GREEN**

Run:

```bash
gofmt -w relay/channel/deepseek/adaptor.go
go test ./relay/channel/deepseek -run TestConvertOpenAIResponsesRequest_PreservesExplicitZeroValues -count=1
```

Expected: PASS.

- [ ] **Step 5: Write the failing V4 reasoning-suffix test**

Add this table-driven test:

```go
func TestConvertOpenAIResponsesRequest_MapsDeepSeekV4ReasoningSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		model      string
		wantModel  string
		wantEffort string
	}{
		{
			name:       "disabled thinking",
			model:      "deepseek-v4-flash-none",
			wantModel:  "deepseek-v4-flash",
			wantEffort: "none",
		},
		{
			name:       "maximum thinking",
			model:      "deepseek-v4-pro-max",
			wantModel:  "deepseek-v4-pro",
			wantEffort: "max",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: tc.model,
				},
			}
			request := dto.OpenAIResponsesRequest{
				Model:     "client-model-alias",
				Reasoning: &dto.Reasoning{Summary: "concise"},
			}

			convertedAny, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
			if err != nil {
				t.Fatalf("ConvertOpenAIResponsesRequest returned error: %v", err)
			}
			converted, ok := convertedAny.(dto.OpenAIResponsesRequest)
			if !ok {
				t.Fatalf("expected dto.OpenAIResponsesRequest, got %T", convertedAny)
			}
			if converted.Model != tc.wantModel {
				t.Fatalf("expected model %q, got %q", tc.wantModel, converted.Model)
			}
			if converted.Reasoning == nil || converted.Reasoning.Effort != tc.wantEffort {
				t.Fatalf("expected reasoning effort %q, got %+v", tc.wantEffort, converted.Reasoning)
			}
			if converted.Reasoning.Summary != "concise" {
				t.Fatalf("expected existing reasoning summary to be preserved, got %q", converted.Reasoning.Summary)
			}
			if info.UpstreamModelName != tc.wantModel {
				t.Fatalf("expected upstream model %q, got %q", tc.wantModel, info.UpstreamModelName)
			}
			if info.ReasoningEffort != tc.wantEffort {
				t.Fatalf("expected relay reasoning effort %q, got %q", tc.wantEffort, info.ReasoningEffort)
			}
		})
	}
}
```

- [ ] **Step 6: Run the suffix test to verify RED**

Run:

```bash
gofmt -w relay/channel/deepseek/adaptor_test.go
go test ./relay/channel/deepseek -run TestConvertOpenAIResponsesRequest_MapsDeepSeekV4ReasoningSuffix -count=1
```

Expected: FAIL because the converted model remains `client-model-alias`.

- [ ] **Step 7: Implement V4 native Responses reasoning normalization**

Replace the converter from Step 3 and add the helper below it:

```go
func (a *Adaptor) ConvertOpenAIResponsesRequest(_ *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	applyDeepSeekV4ResponsesThinkingSuffix(info, &request)
	return request, nil
}

func applyDeepSeekV4ResponsesThinkingSuffix(info *relaycommon.RelayInfo, request *dto.OpenAIResponsesRequest) {
	modelName := request.Model
	if info != nil && info.ChannelMeta != nil && info.UpstreamModelName != "" {
		modelName = info.UpstreamModelName
	}
	baseModel, thinkingType, effort, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(modelName)
	if ok {
		if thinkingType == "disabled" {
			effort = "none"
		}
		request.Model = baseModel
		if request.Reasoning == nil {
			request.Reasoning = &dto.Reasoning{}
		}
		request.Reasoning.Effort = effort
		if info != nil && info.ChannelMeta != nil {
			info.UpstreamModelName = baseModel
		}
	}
	if info != nil && request.Reasoning != nil {
		info.ReasoningEffort = request.Reasoning.Effort
	}
}
```

- [ ] **Step 8: Format and run all DeepSeek adaptor tests to verify GREEN**

Run:

```bash
gofmt -w relay/channel/deepseek/adaptor.go relay/channel/deepseek/adaptor_test.go
go test ./relay/channel/deepseek -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit native request conversion**

Run:

```bash
git add relay/channel/deepseek/adaptor.go relay/channel/deepseek/adaptor_test.go
git commit -m "feat(deepseek): convert native Responses requests"
```

### Task 3: Remove DeepSeek's Unconditional Chat Fallback

**Files:**
- Modify: `service/openaicompat/policy.go:3-6,24-33`
- Test: `service/openaicompat/policy_test.go`

- [ ] **Step 1: Replace the forced-fallback test with native-default coverage**

Replace `TestShouldResponsesUseChatCompletionsPolicy_DeepSeekFallback` with:

```go
func TestShouldResponsesUseChatCompletionsPolicy_DeepSeekUsesNativeWhenDisabled(t *testing.T) {
	policy := model_setting.ResponsesToChatCompletionsPolicy{
		Enabled:       false,
		AllChannels:   false,
		ModelPatterns: []string{"^deepseek-v4-.*$"},
	}

	if ShouldResponsesUseChatCompletionsPolicy(policy, 12, constant.ChannelTypeDeepSeek, "deepseek-v4-pro") {
		t.Fatal("expected DeepSeek to use native Responses when fallback policy is disabled")
	}
}
```

Add explicit DeepSeek fallback coverage:

```go
func TestShouldResponsesUseChatCompletionsPolicy_ExplicitDeepSeekPolicyHonorsModelPattern(t *testing.T) {
	policy := model_setting.ResponsesToChatCompletionsPolicy{
		Enabled:       true,
		AllChannels:   false,
		ChannelTypes:  []int{constant.ChannelTypeDeepSeek},
		ModelPatterns: []string{"^deepseek-v4-.*$"},
	}

	if !ShouldResponsesUseChatCompletionsPolicy(policy, 12, constant.ChannelTypeDeepSeek, "deepseek-v4-pro") {
		t.Fatal("expected explicit DeepSeek fallback policy to match deepseek-v4-pro")
	}
	if ShouldResponsesUseChatCompletionsPolicy(policy, 12, constant.ChannelTypeDeepSeek, "deepseek-chat") {
		t.Fatal("expected explicit DeepSeek fallback policy to reject a non-matching model")
	}
}
```

Keep the two existing non-DeepSeek tests unchanged.

- [ ] **Step 2: Format and run policy tests to verify RED**

Run:

```bash
gofmt -w service/openaicompat/policy_test.go
go test ./service/openaicompat -run TestShouldResponsesUseChatCompletionsPolicy -count=1
```

Expected: FAIL because the current DeepSeek channel-type shortcut always
returns `true`.

- [ ] **Step 3: Make DeepSeek obey the generic explicit policy**

Replace the imports and function in `service/openaicompat/policy.go` with:

```go
import "github.com/QuantumNous/new-api/setting/model_setting"

// ShouldResponsesUseChatCompletionsPolicy returns true if the channel is enabled and the model matches the bridge policy patterns
func ShouldResponsesUseChatCompletionsPolicy(policy model_setting.ResponsesToChatCompletionsPolicy, channelID int, channelType int, model string) bool {
	if !policy.IsChannelEnabled(channelID, channelType) {
		return false
	}
	return matchAnyRegex(policy.ModelPatterns, model)
}
```

Do not change `ShouldChatCompletionsUseResponsesPolicy`, either global wrapper,
or the policy structs in `setting/model_setting/global.go`.

- [ ] **Step 4: Format and run all policy tests to verify GREEN**

Run:

```bash
gofmt -w service/openaicompat/policy.go service/openaicompat/policy_test.go
go test ./service/openaicompat -count=1
```

Expected: PASS.

- [ ] **Step 5: Run the native adaptor and relay regression set**

Run:

```bash
go test ./relay/channel/deepseek ./service/openaicompat ./relay -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the policy change**

Run:

```bash
git add service/openaicompat/policy.go service/openaicompat/policy_test.go
git commit -m "fix(deepseek): prefer native Responses API"
```

### Task 4: Final Verification and Scope Audit

**Files:**
- Verify: `relay/channel/deepseek/adaptor.go`
- Verify: `relay/channel/deepseek/adaptor_test.go`
- Verify: `service/openaicompat/policy.go`
- Verify: `service/openaicompat/policy_test.go`

- [ ] **Step 1: Run formatting and static diff checks**

Run:

```bash
gofmt -w relay/channel/deepseek/adaptor.go relay/channel/deepseek/adaptor_test.go service/openaicompat/policy.go service/openaicompat/policy_test.go
git diff --check HEAD~3..HEAD
```

Expected: both commands exit 0 with no formatting errors.

- [ ] **Step 2: Run focused tests without the Go test cache**

Run:

```bash
go test ./relay/channel/deepseek ./service/openaicompat ./relay -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the full backend suite and compare with the recorded baseline**

Run:

```bash
go test ./... -count=1
```

Expected baseline failures only:

- missing `web/dist` in the root package;
- the same three `relay/channel/claude` file-content tests;
- `TestStreamScannerHandler_StreamStatus_PreInitialized` in `relay/helper`.

Any failure in `relay/channel/deepseek`, `service/openaicompat`, or `relay`, or
any new failing package, is a regression and must be fixed before completion.

- [ ] **Step 4: Audit the committed scope**

Run:

```bash
git diff --stat ef2bc25e..HEAD
git diff --name-status ef2bc25e..HEAD
git status --short --branch
```

Expected implementation files:

```text
relay/channel/deepseek/adaptor.go
relay/channel/deepseek/adaptor_test.go
service/openaicompat/policy.go
service/openaicompat/policy_test.go
```

Documentation files and the earlier `.gitignore` safety commit are also
expected. No deployment, frontend, billing, database, or unrelated relay file
may be modified. The worktree must be clean.

- [ ] **Step 5: Inspect the final commit sequence**

Run:

```bash
git log --oneline --decorate -6
```

Expected implementation commits:

```text
fix(deepseek): prefer native Responses API
feat(deepseek): convert native Responses requests
feat(deepseek): route Responses requests natively
```
