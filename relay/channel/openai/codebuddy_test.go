package openai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func TestCodeBuddy_GetRequestURL(t *testing.T) {
	t.Parallel()

	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
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

func TestCodeBuddy_GetRequestURL_Embeddings(t *testing.T) {
	t.Parallel()

	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeEmbeddings,
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

	if url == "" {
		t.Fatal("GetRequestURL() returned empty string for embeddings mode")
	}
	// Embeddings should NOT use the /v2/chat/completions override
	if strings.Contains(url, "/v2/chat/completions") {
		t.Errorf("embeddings URL should not be /v2/chat/completions, got %q", url)
	}
}

func TestCodeBuddy_SetupRequestHeader(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	adaptor := &Adaptor{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeCodeBuddy,
			ApiKey:      "test-api-key",
		},
	}

	adaptor.Init(info)
	header := http.Header{}
	err := adaptor.SetupRequestHeader(c, &header, info)
	if err != nil {
		t.Fatalf("SetupRequestHeader returned error: %v", err)
	}

	if got := header.Get("Authorization"); got != "Bearer test-api-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-api-key")
	}
	if got := header.Get("X-Api-Key"); got != "test-api-key" {
		t.Errorf("X-Api-Key = %q, want %q", got, "test-api-key")
	}
	if got := header.Get("X-Product"); got != "SaaS" {
		t.Errorf("X-Product = %q, want %q", got, "SaaS")
	}
	if got := header.Get("X-IDE-Type"); got != "CLI" {
		t.Errorf("X-IDE-Type = %q, want %q", got, "CLI")
	}
	if got := header.Get("X-IDE-Name"); got != "CLI" {
		t.Errorf("X-IDE-Name = %q, want %q", got, "CLI")
	}
	if got := header.Get("X-IDE-Version"); got != "2.83.1" {
		t.Errorf("X-IDE-Version = %q, want %q", got, "2.83.1")
	}
	if convID := header.Get("X-Conversation-ID"); convID == "" {
		t.Error("X-Conversation-ID is empty, expected a UUID")
	}
	if convReqID := header.Get("X-Conversation-Request-ID"); convReqID == "" {
		t.Error("X-Conversation-Request-ID is empty, expected a UUID")
	}
}

func TestCodeBuddy_ConvertOpenAIRequest_ForcesStream(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	adaptor := &Adaptor{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	streamFalse := false
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeCodeBuddy,
			SupportStreamOptions: true,
		},
	}

	adaptor.Init(info)

	request := &dto.GeneralOpenAIRequest{
		Model:  "gpt-4o",
		Stream: &streamFalse,
	}

	converted, err := adaptor.ConvertOpenAIRequest(c, info, request)
	if err != nil {
		t.Fatalf("ConvertOpenAIRequest returned error: %v", err)
	}

	convertedReq, ok := converted.(*dto.GeneralOpenAIRequest)
	if !ok {
		t.Fatalf("expected *dto.GeneralOpenAIRequest, got %T", converted)
	}

	if !lo.FromPtrOr(convertedReq.Stream, false) {
		t.Error("expected stream=true in converted request, got stream=false")
	}

	if convertedReq.StreamOptions == nil || !convertedReq.StreamOptions.IncludeUsage {
		t.Error("expected stream_options.include_usage=true in converted request")
	}
}

// mockCodeBuddyServer returns an httptest.Server that mimics CodeBuddy's SSE response
func mockCodeBuddyServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify path
		if r.URL.Path != "/v2/chat/completions" {
			t.Errorf("upstream path = %q, want /v2/chat/completions", r.URL.Path)
		}

		// Return SSE stream
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []string{
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"glm-5.0-turbo","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"glm-5.0-turbo","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"glm-5.0-turbo","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			`data: [DONE]`,
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "%s\n\n", chunk)
			flusher.Flush()
		}
	}))
}

func TestCodeBuddy_StreamResponse(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	server := mockCodeBuddyServer(t)
	defer server.Close()

	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
		IsStream:  true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeCodeBuddy,
			ChannelBaseUrl: server.URL,
			ApiKey:         "test-key",
		},
	}
	adaptor.Init(info)

	// Verify URL points to /v2/chat/completions
	url, err := adaptor.GetRequestURL(info)
	if err != nil {
		t.Fatalf("GetRequestURL error: %v", err)
	}
	if url != server.URL+"/v2/chat/completions" {
		t.Errorf("URL = %q, want %q", url, server.URL+"/v2/chat/completions")
	}
}

func TestCodeBuddy_StreamAggregation_NonStreamClient(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	server := mockCodeBuddyServer(t)
	defer server.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	// Simulate a non-stream client hitting CodeBuddy channel
	info := &relaycommon.RelayInfo{
		RelayMode:            relayconstant.RelayModeChatCompletions,
		IsStream:             true, // will be set to true by the forced streaming logic
		OriginalClientStream: false, // client originally requested non-stream
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:        constant.ChannelTypeCodeBuddy,
			ChannelBaseUrl:     server.URL,
			ApiKey:             "test-key",
			ForceUpstreamStream: true,
			SupportStreamOptions: true,
		},
	}

	// Make HTTP request to mock upstream
	req, _ := http.NewRequest("POST", server.URL+"/v2/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")
	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("failed to make request to mock server: %v", err)
	}
	defer resp.Body.Close()

	// Run aggregation handler
	usage, apiErr := OaiStreamAggregationHandler(c, info, resp)
	if apiErr != nil {
		t.Fatalf("OaiStreamAggregationHandler returned error: %v", apiErr)
	}

	// Verify usage
	if usage == nil {
		t.Fatal("usage is nil")
	}
	if usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", usage.PromptTokens)
	}
	if usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d, want 5", usage.CompletionTokens)
	}

	// Verify response is JSON (not SSE)
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	// Verify response body contains expected content
	body := w.Body.String()
	if !strings.Contains(body, `"object":"chat.completion"`) {
		t.Errorf("response body missing chat.completion object: %s", body)
	}
	if !strings.Contains(body, "Hello world") {
		t.Errorf("response body missing aggregated content 'Hello world': %s", body)
	}

	// Parse response as OpenAITextResponse
	var response dto.OpenAITextResponse
	if err := common.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(response.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(response.Choices))
	}
	if response.Choices[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", response.Choices[0].FinishReason)
	}
}

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
