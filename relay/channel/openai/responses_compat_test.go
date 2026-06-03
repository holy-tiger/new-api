package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func newResponsesCompatTestContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, recorder
}

func newSSEHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func extractOpenAIStreamChunks(t *testing.T, body string) []dto.ChatCompletionsStreamResponse {
	t.Helper()

	lines := strings.Split(body, "\n")
	chunks := make([]dto.ChatCompletionsStreamResponse, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(payload, &chunk); err != nil {
			t.Fatalf("failed to unmarshal stream chunk %q: %v", payload, err)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func TestOaiResponsesToChatStreamHandler_MapsIncompleteToLength(t *testing.T) {
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	c, recorder := newResponsesCompatTestContext("/v1/chat/completions")
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-4o",
		},
	}

	resp := newSSEHTTPResponse(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","created_at":123,"model":"gpt-4o","status":"in_progress"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"resp_1","object":"response","created_at":123,"model":"gpt-4o","status":"incomplete","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"incomplete_details":{"reason":"max_output_tokens"}}}`,
		``,
	}, "\n"))

	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if usage == nil {
		t.Fatal("expected usage")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Fatalf("unexpected usage: %#v", usage)
	}

	chunks := extractOpenAIStreamChunks(t, recorder.Body.String())
	if len(chunks) == 0 {
		t.Fatalf("expected streamed chat chunks, got body %q", recorder.Body.String())
	}

	foundLength := false
	for _, chunk := range chunks {
		if len(chunk.Choices) == 0 || chunk.Choices[0].FinishReason == nil {
			continue
		}
		if *chunk.Choices[0].FinishReason == "length" {
			foundLength = true
			break
		}
	}
	if !foundLength {
		t.Fatalf("expected finish_reason=length in stream body, got %q", recorder.Body.String())
	}
}

func TestOaiResponsesToChatStreamHandler_AcceptsCustomToolCallItems(t *testing.T) {
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	c, recorder := newResponsesCompatTestContext("/v1/chat/completions")
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-4o",
		},
	}

	resp := newSSEHTTPResponse(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_2","object":"response","created_at":123,"model":"gpt-4o","status":"in_progress"}}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"item_custom","call_id":"call_custom","name":"my_custom_tool","arguments":{"x":1},"status":"completed"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_2","object":"response","created_at":123,"model":"gpt-4o","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`,
		``,
	}, "\n"))

	_, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("expected tool_calls in stream body, got %q", body)
	}
	if !strings.Contains(body, `"my_custom_tool"`) {
		t.Fatalf("expected custom tool name in stream body, got %q", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected finish_reason=tool_calls in stream body, got %q", body)
	}
}

func TestOaiResponsesToChatStreamHandler_PreservesTextAndToolCallsTogether(t *testing.T) {
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	c, recorder := newResponsesCompatTestContext("/v1/chat/completions")
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-4o",
		},
	}

	resp := newSSEHTTPResponse(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_4","object":"response","created_at":123,"model":"gpt-4o","status":"in_progress"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"I will call a tool now."}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"item_custom","call_id":"call_custom","name":"my_custom_tool","arguments":{"x":1},"status":"completed"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_4","object":"response","created_at":123,"model":"gpt-4o","status":"completed","usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}`,
		``,
	}, "\n"))

	_, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"content":"I will call a tool now."`) {
		t.Fatalf("expected text delta in stream body, got %q", body)
	}
	if !strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("expected tool_calls in stream body, got %q", body)
	}
	if !strings.Contains(body, `"my_custom_tool"`) {
		t.Fatalf("expected custom tool name in stream body, got %q", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected finish_reason=tool_calls in stream body, got %q", body)
	}
}

func TestOaiResponsesStreamHandler_UsesIncompleteUsage(t *testing.T) {
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	c, recorder := newResponsesCompatTestContext("/v1/responses")
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-4o",
		},
	}

	resp := newSSEHTTPResponse(strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"resp_3","object":"response","created_at":123,"model":"gpt-4o","status":"incomplete","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"incomplete_details":{"reason":"max_output_tokens"}}}`,
		``,
	}, "\n"))

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if usage == nil {
		t.Fatal("expected usage")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if !strings.Contains(recorder.Body.String(), `"type":"response.incomplete"`) {
		t.Fatalf("expected passthrough response.incomplete event, got %q", recorder.Body.String())
	}
}
