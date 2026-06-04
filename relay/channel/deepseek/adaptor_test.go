package deepseek

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

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
