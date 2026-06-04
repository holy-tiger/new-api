package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func TestCodexchatResponsesStreamHandler_AppendsDoneSentinel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "req-test")

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			``,
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))),
	}

	usage, relayErr := codexchatResponsesStreamHandler(c, &relaycommon.RelayInfo{}, resp, "", nil)
	if relayErr != nil {
		t.Fatalf("codexchatResponsesStreamHandler returned error: %+v", relayErr)
	}
	if usage == nil || usage.TotalTokens != 2 {
		t.Fatalf("expected usage total_tokens=2, got %+v", usage)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.completed\n") {
		t.Fatalf("expected response.completed event, got body:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]\n\n") {
		t.Fatalf("expected final [DONE] sentinel, got body:\n%s", body)
	}
}

func TestCodexchatResponsesStreamHandler_ParsesMultiLineSSEBlocks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "req-test-multiline")

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message`,
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,`,
			`data: "model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			``,
			`event: message`,
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))),
	}

	usage, relayErr := codexchatResponsesStreamHandler(c, &relaycommon.RelayInfo{}, resp, "", nil)
	if relayErr != nil {
		t.Fatalf("codexchatResponsesStreamHandler returned error: %+v", relayErr)
	}
	if usage == nil || usage.TotalTokens != 2 {
		t.Fatalf("expected usage total_tokens=2, got %+v", usage)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `"type":"response.output_text.delta"`) {
		t.Fatalf("expected output_text delta from multi-line SSE block, got body:\n%s", body)
	}
	if !strings.Contains(body, `"delta":"Hello"`) {
		t.Fatalf("expected Hello delta from multi-line SSE block, got body:\n%s", body)
	}
}

func TestCodexchatResponsesStreamHandler_ConvertsUpstreamErrorToResponseFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "req-test-error")

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: error`,
			`data: {"error":{"message":"upstream exploded","type":"server_error"}}`,
			``,
		}, "\n"))),
	}

	usage, relayErr := codexchatResponsesStreamHandler(c, &relaycommon.RelayInfo{}, resp, "", nil)
	if relayErr != nil {
		t.Fatalf("codexchatResponsesStreamHandler returned error: %+v", relayErr)
	}
	if usage != nil {
		t.Fatalf("expected nil usage for upstream error stream, got %+v", usage)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed\n") {
		t.Fatalf("expected response.failed event, got body:\n%s", body)
	}
	if !strings.Contains(body, `"message":"upstream exploded"`) {
		t.Fatalf("expected upstream error message in response.failed, got body:\n%s", body)
	}
	if strings.Contains(body, "event: response.completed\n") {
		t.Fatalf("did not expect response.completed after upstream error, got body:\n%s", body)
	}
}
