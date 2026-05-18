# CodeBuddy Claude Code Support Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enable Claude Code (Anthropic Messages API client) to use the CodeBuddy channel by bridging Claude format requests to CodeBuddy's OpenAI-compatible upstream.

**Architecture:** Reuse the existing Claude→OpenAI conversion pipeline (`ClaudeToOpenAIRequest`, `StreamResponseOpenAI2Claude`, `ResponseOpenAI2Claude`). Fix three gaps: (1) URL routing for Claude-format requests, (2) Claude format output in stream aggregation, (3) RelayInfo initialization for CodeBuddy in ClaudeHelper path.

**Tech Stack:** Go, Gin, existing relay/channel/openai adaptor

---

## Background

When Claude Code sends a request to `/v1/messages`, the router marks it as `RelayFormatClaude` and dispatches to `ClaudeHelper`. ClaudeHelper parses the body as `ClaudeRequest`, then calls the adaptor selected by `ApiType`. CodeBuddy maps to `APITypeOpenAI`, so the OpenAI adaptor is used.

The OpenAI adaptor's `ConvertClaudeRequest` already calls `ClaudeToOpenAIRequest` to convert the request, and `OaiStreamHandler` already handles Claude format streaming output via `HandleStreamFormat`. However, three gaps prevent Claude Code from working with CodeBuddy:

1. **GetRequestURL**: Only matches `RelayModeChatCompletions` for CodeBuddy's `/v2/chat/completions` path. Claude requests arrive with `RelayModeUnknown` (since `/v1/messages` is not in `Path2RelayMode`), so they fall through to the default path.

2. **OaiStreamAggregationHandler**: When a Claude Code client sends `stream=false`, CodeBuddy forces upstream streaming but the aggregation handler only outputs OpenAI format — it never converts to Claude format.

3. **RelayInfo initialization**: When `ClaudeHelper` is used with CodeBuddy, `ForceUpstreamStream` must be set so that `DoResponse` takes the aggregation path. Also, `ClaudeConvertInfo` must be initialized for the stream format conversion to work.

---

## Task 1: Fix GetRequestURL for Claude-format CodeBuddy requests

**Files:**
- Modify: `relay/channel/openai/adaptor.go:165-169`
- Test: `relay/channel/openai/codebuddy_test.go`

**Step 1: Write the failing test**

Add a test case in `codebuddy_test.go` that verifies a Claude-format request (with `RelayFormatClaude`) to CodeBuddy routes to `/v2/chat/completions`:

```go
func TestCodeBuddy_GetRequestURL_ClaudeFormat(t *testing.T) {
	t.Parallel()

	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		RelayMode:   relayconstant.RelayModeUnknown, // /v1/messages doesn't match Path2RelayMode
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeCodeBuddy,
			ChannelBaseUrl: "https://codebuddy.example.com",
		},
	}

	adaptor.Init(info)
	url, err := adaptor.GetRequestURL(info)
	if err != nil {
		t.Fatalf("GetRequestURL returned error: %v", err)
	}

	want := "https://codebuddy.example.com/v2/chat/completions"
	if url != want {
		t.Fatalf("GetRequestURL() = %q, want %q", url, want)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy_GetRequestURL_ClaudeFormat -v`
Expected: FAIL — URL will be something other than `/v2/chat/completions`

**Step 3: Write minimal implementation**

In `relay/channel/openai/adaptor.go`, change the CodeBuddy case in `GetRequestURL` from:

```go
case constant.ChannelTypeCodeBuddy:
    if info.RelayMode == relayconstant.RelayModeChatCompletions {
        return fmt.Sprintf("%s/v2/chat/completions", info.ChannelBaseUrl), nil
    }
    return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, info.RequestURLPath, info.ChannelType), nil
```

to:

```go
case constant.ChannelTypeCodeBuddy:
    if info.RelayMode == relayconstant.RelayModeChatCompletions || info.RelayFormat == types.RelayFormatClaude {
        return fmt.Sprintf("%s/v2/chat/completions", info.ChannelBaseUrl), nil
    }
    return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, info.RequestURLPath, info.ChannelType), nil
```

**Step 4: Run test to verify it passes**

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy_GetRequestURL_ClaudeFormat -v`
Expected: PASS

Also run existing tests to verify no regression:

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add relay/channel/openai/adaptor.go relay/channel/openai/codebuddy_test.go
git commit -m "feat(codebuddy): route Claude-format requests to /v2/chat/completions"
```

---

## Task 2: Add Claude format output to OaiStreamAggregationHandler

**Files:**
- Modify: `relay/channel/openai/stream_aggregation.go:187-198`
- Test: `relay/channel/openai/codebuddy_test.go`

**Step 1: Write the failing test**

Add a test that simulates a Claude-format non-streaming client hitting CodeBuddy (which forces upstream streaming), and verifies the aggregated response is in Claude format:

```go
func TestCodeBuddy_StreamAggregation_ClaudeFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	server := mockCodeBuddyServer(t)
	defer server.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:           types.RelayFormatClaude,
		RelayMode:            relayconstant.RelayModeChatCompletions,
		IsStream:             true,
		OriginalClientStream: false,
		ClaudeConvertInfo:    &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:         constant.ChannelTypeCodeBuddy,
			ChannelBaseUrl:      server.URL,
			ApiKey:              "test-key",
			ForceUpstreamStream: true,
			SupportStreamOptions: true,
		},
	}

	req, _ := http.NewRequest("POST", server.URL+"/v2/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")
	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("failed to make request to mock server: %v", err)
	}
	defer resp.Body.Close()

	usage, apiErr := OaiStreamAggregationHandler(c, info, resp)
	if apiErr != nil {
		t.Fatalf("OaiStreamAggregationHandler returned error: %v", apiErr)
	}
	if usage == nil {
		t.Fatal("usage is nil")
	}

	// Verify response is Claude format
	body := w.Body.String()
	if !strings.Contains(body, `"type":"message"`) {
		t.Errorf("response should be Claude format (type:message), got: %s", body)
	}
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Errorf("response should contain role:assistant, got: %s", body)
	}
	if !strings.Contains(body, "Hello world") {
		t.Errorf("response body missing aggregated content 'Hello world': %s", body)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy_StreamAggregation_ClaudeFormat -v`
Expected: FAIL — response will be OpenAI format (`"object":"chat.completion"`) instead of Claude format (`"type":"message"`)

**Step 3: Write minimal implementation**

In `relay/channel/openai/stream_aggregation.go`, replace the final output section (lines ~187-198):

From:
```go
	applyUsagePostProcessing(info, usage, nil)

	// Write as JSON
	respBody, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(respBody)

	return usage, nil
```

To:
```go
	applyUsagePostProcessing(info, usage, nil)

	// Write response — convert to Claude format if the client used Claude API
	var respBody []byte
	if info.RelayFormat == types.RelayFormatClaude {
		claudeResp := service.ResponseOpenAI2Claude(&response, info)
		var marshalErr error
		respBody, marshalErr = common.Marshal(claudeResp)
		if marshalErr != nil {
			return nil, types.NewOpenAIError(marshalErr, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
	} else {
		var marshalErr error
		respBody, marshalErr = common.Marshal(response)
		if marshalErr != nil {
			return nil, types.NewOpenAIError(marshalErr, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(respBody)

	return usage, nil
```

**Step 4: Run test to verify it passes**

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy_StreamAggregation_ClaudeFormat -v`
Expected: PASS

Also run existing aggregation test:

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy_StreamAggregation_NonStreamClient -v`
Expected: PASS (existing OpenAI format test should still work)

**Step 5: Commit**

```bash
git add relay/channel/openai/stream_aggregation.go relay/channel/openai/codebuddy_test.go
git commit -m "feat(codebuddy): output Claude format when aggregating stream for Claude clients"
```

---

## Task 3: Ensure RelayInfo initialization for Claude+CodeBuddy path

**Files:**
- Modify: `relay/common/relay_info.go`
- Test: `relay/common/relay_info_test.go`

**Step 1: Write the failing test**

Add a test that verifies `ForceUpstreamStream` and `ClaudeConvertInfo` are properly set when a Claude-format request targets a CodeBuddy channel:

```go
func TestGenRelayInfoClaude_CodeBuddyForceStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	// Simulate channel context set by middleware
	c.Set("channel_type", constant.ChannelTypeCodeBuddy)
	c.Set("channel_id", 1)

	claudeReq := &dto.ClaudeRequest{
		Model:    "gpt-4o",
		MaxTokens: common.GetPointer[uint](1024),
	}
	claudeReq.Messages = []dto.ClaudeMessage{{Role: "user"}}
	claudeReq.SetContent("hello")

	info := GenRelayInfoClaude(c, claudeReq)

	// ForceUpstreamStream is set during InitChannelMeta, which reads channel_type from context
	// We need to call InitChannelMeta to verify
	info.InitChannelMeta(c)

	if info.ChannelMeta == nil {
		t.Fatal("ChannelMeta is nil after InitChannelMeta")
	}
	if !info.ChannelMeta.ForceUpstreamStream {
		t.Error("ForceUpstreamStream should be true for CodeBuddy channel")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./relay/common/ -run TestGenRelayInfoClaude_CodeBuddyForceStream -v`
Expected: May FAIL or PASS — need to verify that `ForceUpstreamStream` is already set correctly in the `InitChannelMeta` path.

**Step 3: Analyze and fix if needed**

The `InitChannelMeta` function at `relay/common/relay_info.go:229` already sets `ForceUpstreamStream = true` for `ChannelTypeCodeBuddy`. The key question is whether `ClaudeHelper` calls `InitChannelMeta` before the adaptor processes the request.

Looking at `ClaudeHelper` (line 27): `info.InitChannelMeta(c)` is the first call. So `ForceUpstreamStream` should already be set correctly.

If the test passes, this task needs no code change — it's a verification test only.

If the test fails, investigate why `channel_type` is not being propagated from context to `ChannelMeta` in the Claude path.

**Step 4: Run test to verify it passes**

Run: `go test ./relay/common/ -run TestGenRelayInfoClaude_CodeBuddyForceStream -v`
Expected: PASS

**Step 5: Commit (if code changes were needed)**

```bash
git add relay/common/relay_info.go relay/common/relay_info_test.go
git commit -m "fix(codebuddy): ensure ForceUpstreamStream is set for Claude format requests"
```

---

## Task 4: Run full test suite and verify no regressions

**Files:** None

**Step 1: Run all CodeBuddy-related tests**

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy -v`
Expected: All PASS

**Step 2: Run relay common tests**

Run: `go test ./relay/common/ -v`
Expected: All PASS

**Step 3: Run full project build**

Run: `go build ./...`
Expected: No compilation errors

**Step 4: Commit final state if any test fixes needed**

---

## Task 5: Integration test with mock Claude Code client

**Files:**
- Create: `relay/channel/openai/codebuddy_claude_test.go`

**Step 1: Write integration test**

Create a test that simulates a full Claude Code request flow through CodeBuddy:

```go
func TestCodeBuddy_ClaudeCodeIntegration(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	server := mockCodeBuddyServer(t)
	defer server.Close()

	// Test 1: Streaming Claude request
	t.Run("streaming", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

		info := &relaycommon.RelayInfo{
			RelayFormat:        types.RelayFormatClaude,
			RelayMode:          relayconstant.RelayModeChatCompletions,
			IsStream:           true,
			OriginalClientStream: true,
			ClaudeConvertInfo:  &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone},
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:          constant.ChannelTypeCodeBuddy,
				ChannelBaseUrl:       server.URL,
				ApiKey:               "test-key",
				ForceUpstreamStream:  true,
				SupportStreamOptions: true,
			},
		}

		adaptor := &Adaptor{}
		adaptor.Init(info)

		// Verify URL
		url, err := adaptor.GetRequestURL(info)
		if err != nil {
			t.Fatalf("GetRequestURL error: %v", err)
		}
		wantURL := server.URL + "/v2/chat/completions"
		if url != wantURL {
			t.Errorf("URL = %q, want %q", url, wantURL)
		}

		// Verify headers
		header := http.Header{}
		if err := adaptor.SetupRequestHeader(c, &header, info); err != nil {
			t.Fatalf("SetupRequestHeader error: %v", err)
		}
		if header.Get("X-Api-Key") != "test-key" {
			t.Errorf("X-Api-Key = %q, want test-key", header.Get("X-Api-Key"))
		}
		if header.Get("X-Product") != "SaaS" {
			t.Errorf("X-Product = %q, want SaaS", header.Get("X-Product"))
		}
	})

	// Test 2: Non-streaming Claude request (aggregation)
	t.Run("non_streaming_aggregation", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

		info := &relaycommon.RelayInfo{
			RelayFormat:           types.RelayFormatClaude,
			RelayMode:             relayconstant.RelayModeChatCompletions,
			IsStream:              true,
			OriginalClientStream:  false,
			ClaudeConvertInfo:     &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone},
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:          constant.ChannelTypeCodeBuddy,
				ChannelBaseUrl:       server.URL,
				ApiKey:               "test-key",
				ForceUpstreamStream:  true,
				SupportStreamOptions: true,
			},
		}

		req, _ := http.NewRequest("POST", server.URL+"/v2/chat/completions", nil)
		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		usage, apiErr := OaiStreamAggregationHandler(c, info, resp)
		if apiErr != nil {
			t.Fatalf("aggregation error: %v", apiErr)
		}
		if usage == nil || usage.PromptTokens != 10 {
			t.Errorf("usage unexpected: %+v", usage)
		}

		body := w.Body.String()
		if !strings.Contains(body, `"type":"message"`) {
			t.Errorf("expected Claude format response, got: %s", body[:min(len(body), 200)])
		}
	})
}
```

**Step 2: Run integration test**

Run: `go test ./relay/channel/openai/ -run TestCodeBuddy_ClaudeCodeIntegration -v`
Expected: All PASS

**Step 3: Commit**

```bash
git add relay/channel/openai/codebuddy_claude_test.go
git commit -m "test(codebuddy): add Claude Code integration tests"
```

---

## Summary of Changes

| File | Change | Lines |
|------|--------|-------|
| `relay/channel/openai/adaptor.go` | Add `RelayFormatClaude` check in GetRequestURL | ~1 |
| `relay/channel/openai/stream_aggregation.go` | Add Claude format conversion in aggregated output | ~12 |
| `relay/channel/openai/codebuddy_test.go` | Add tests for Claude URL routing | ~20 |
| `relay/channel/openai/codebuddy_claude_test.go` | Integration tests for Claude Code flow | ~90 |
| `relay/common/relay_info_test.go` | Verification test for ForceUpstreamStream | ~25 |

**Total: ~148 lines added/changed, 0 architecture changes**
