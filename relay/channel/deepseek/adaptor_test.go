package deepseek

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
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

func TestConvertOpenAIResponsesRequest_InitializesReasoningForDeepSeekV4Suffix(t *testing.T) {
	t.Parallel()

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "deepseek-v4-pro-max",
		},
	}
	request := dto.OpenAIResponsesRequest{Model: "client-model-alias"}

	convertedAny, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
	if err != nil {
		t.Fatalf("ConvertOpenAIResponsesRequest returned error: %v", err)
	}
	converted, ok := convertedAny.(dto.OpenAIResponsesRequest)
	if !ok {
		t.Fatalf("expected dto.OpenAIResponsesRequest, got %T", convertedAny)
	}
	if converted.Reasoning == nil {
		t.Fatal("expected reasoning to be initialized for a V4 thinking suffix")
	}
	if converted.Reasoning.Effort != "max" {
		t.Fatalf("expected reasoning effort max, got %q", converted.Reasoning.Effort)
	}
}

func TestConvertOpenAIRequest_NormalizesDeveloperRoleToSystem(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	adaptor := &Adaptor{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeDeepSeek,
			UpstreamModelName: "deepseek-v4-pro",
		},
	}

	request := &dto.GeneralOpenAIRequest{
		Model: "gpt-5.4",
		Messages: []dto.Message{
			{Role: "system", Content: "top-level instructions"},
			{Role: "developer", Content: "developer instructions"},
			{Role: "user", Content: "hello"},
		},
	}

	converted, err := adaptor.ConvertOpenAIRequest(c, info, request)
	if err != nil {
		t.Fatalf("ConvertOpenAIRequest returned error: %v", err)
	}

	convertedReq, ok := converted.(*dto.GeneralOpenAIRequest)
	if !ok {
		t.Fatalf("expected *dto.GeneralOpenAIRequest, got %T", converted)
	}

	if convertedReq.Messages[0].Role != "system" {
		t.Fatalf("expected system role to remain system, got %q", convertedReq.Messages[0].Role)
	}
	if convertedReq.Messages[1].Role != "system" {
		t.Fatalf("expected developer role to be normalized to system, got %q", convertedReq.Messages[1].Role)
	}
	if convertedReq.Messages[2].Role != "user" {
		t.Fatalf("expected user role to remain user, got %q", convertedReq.Messages[2].Role)
	}
}

func TestConvertOpenAIResponsesRequest_ResponsesLiteActivatesOnlyForMappedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newRequest := func() dto.OpenAIResponsesRequest {
		return dto.OpenAIResponsesRequest{
			Model: "gpt-5.6-luna",
			Input: []byte(`[
				{"type":"additional_tools","tools":[{"type":"custom","name":"exec","description":"Run JavaScript"}]},
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"base instructions"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"run JavaScript"}]}
			]`),
		}
	}

	t.Run("mapped model", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		info := &relaycommon.RelayInfo{
			RelayMode:       relayconstant.RelayModeResponses,
			OriginModelName: "gpt-5.6-luna",
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:       constant.ChannelTypeDeepSeek,
				UpstreamModelName: "deepseek-v4-flash",
				IsModelMapped:     true,
			},
		}

		convertedAny, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, newRequest())
		if err != nil {
			t.Fatalf("convert request: %v", err)
		}
		converted := convertedAny.(dto.OpenAIResponsesRequest)
		if !c.GetBool(responsesLiteBridgeContextKey) {
			t.Fatal("mapped Lite request did not activate response bridge")
		}
		if strings.Contains(string(converted.Input), "additional_tools") || !strings.Contains(string(converted.Tools), `"type":"function"`) {
			t.Fatalf("mapped Lite request was not normalized: %+v", converted)
		}
	})

	t.Run("direct DeepSeek model", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		request := newRequest()
		originalInput := append([]byte(nil), request.Input...)
		info := &relaycommon.RelayInfo{
			RelayMode:       relayconstant.RelayModeResponses,
			OriginModelName: "deepseek-v4-flash",
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:       constant.ChannelTypeDeepSeek,
				UpstreamModelName: "deepseek-v4-flash",
				IsModelMapped:     false,
			},
		}

		convertedAny, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, request)
		if err != nil {
			t.Fatalf("convert request: %v", err)
		}
		converted := convertedAny.(dto.OpenAIResponsesRequest)
		if c.GetBool(responsesLiteBridgeContextKey) {
			t.Fatal("direct DeepSeek request activated bridge")
		}
		if !strings.Contains(string(converted.Input), "additional_tools") || !bytes.Equal(converted.Input, originalInput) {
			t.Fatalf("direct request changed: %s", converted.Input)
		}
	})
}

func TestDoResponse_ResponsesLiteRestoresMappedExecCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(responsesLiteBridgeContextKey, true)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_1","object":"response","status":"completed",
			"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","arguments":"{\"source\":\"text(\\\"OK\\\");\"}","status":"completed"}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)),
	}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		IsStream:  false,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "deepseek-v4-flash",
		},
	}

	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	if apiErr != nil {
		t.Fatalf("DoResponse error: %v", apiErr)
	}
	if usage == nil {
		t.Fatal("expected usage")
	}
	body := w.Body.String()
	if !strings.Contains(body, `"type":"custom_tool_call"`) || !strings.Contains(body, `"input":"text(\"OK\");"`) {
		t.Fatalf("exec call was not restored: %s", body)
	}
	if strings.Contains(body, `"type":"function_call"`) {
		t.Fatalf("function call leaked downstream: %s", body)
	}
}
