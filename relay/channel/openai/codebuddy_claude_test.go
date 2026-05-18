package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

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
			RelayFormat:          types.RelayFormatClaude,
			RelayMode:            relayconstant.RelayModeChatCompletions,
			IsStream:             true,
			OriginalClientStream: true,
			ClaudeConvertInfo:    &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone},
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:          constant.ChannelTypeCodeBuddy,
				ChannelBaseUrl:       server.URL,
				ApiKey:                "test-key",
				ForceUpstreamStream:   true,
				SupportStreamOptions:  true,
			},
		}

		adaptor := &Adaptor{}
		adaptor.Init(info)

		// Verify URL routes to /v2/chat/completions
		url, err := adaptor.GetRequestURL(info)
		if err != nil {
			t.Fatalf("GetRequestURL error: %v", err)
		}
		wantURL := server.URL + "/v2/chat/completions"
		if url != wantURL {
			t.Errorf("URL = %q, want %q", url, wantURL)
		}

		// Verify CodeBuddy headers are set
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
		if header.Get("X-IDE-Type") != "CLI" {
			t.Errorf("X-IDE-Type = %q, want CLI", header.Get("X-IDE-Type"))
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
				ApiKey:                "test-key",
				ForceUpstreamStream:   true,
				SupportStreamOptions:  true,
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
		if !strings.Contains(body, `"role":"assistant"`) {
			t.Errorf("expected role:assistant in response, got: %s", body[:min(len(body), 200)])
		}
		if !strings.Contains(body, "Hello world") {
			t.Errorf("expected aggregated content 'Hello world', got: %s", body[:min(len(body), 200)])
		}
	})
}
