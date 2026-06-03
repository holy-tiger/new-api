package codexchat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
)

// ChatCompletionsResponseToResponsesResponse converts a Chat Completions JSON
// response into a Codex Responses API JSON response.
func ChatCompletionsResponseToResponsesResponse(chatResp *dto.OpenAITextResponse) *dto.OpenAIResponsesResponse {
	if chatResp == nil {
		return nil
	}

	resp := &dto.OpenAIResponsesResponse{
		ID:        generateResponsesID(),
		Object:    "response",
		CreatedAt: int(time.Now().Unix()),
		Model:     chatResp.Model,
		Status:    json.RawMessage(`"completed"`),
	}

	// If there are no choices, return early
	if len(chatResp.Choices) == 0 {
		resp.Usage = &chatResp.Usage
		return resp
	}

	choice := chatResp.Choices[0]

	// Map status and incomplete_details
	if choice.FinishReason == "length" {
		resp.Status = json.RawMessage(`"incomplete"`)
		resp.IncompleteDetails = &dto.IncompleteDetails{
			Reasoning: "max_output_tokens",
		}
	}

	// Map message content
	if choice.Message.Content != nil {
		stringContent := choice.Message.StringContent()
		if stringContent != "" {
			resp.Output = append(resp.Output, dto.ResponsesOutput{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{
						Type: "output_text",
						Text: stringContent,
					},
				},
			})
		}
	}

	// Map tool calls
	if len(choice.Message.ToolCalls) > 0 {
		var toolCalls []dto.ToolCallResponse
		if err := common.Unmarshal(choice.Message.ToolCalls, &toolCalls); err == nil {
			for _, tc := range toolCalls {
				resp.Output = append(resp.Output, dto.ResponsesOutput{
					Type:      "function_call",
					ID:        tc.ID,
					CallId:    tc.ID,
					Name:      tc.Function.Name,
					Arguments: json.RawMessage(tc.Function.Arguments),
					Status:    "completed",
				})
			}
		} else {
			logger.LogWarn(context.Background(), "[codexchat] failed to unmarshal ToolCalls from chat response: "+err.Error())
		}
	}

	// Map reasoning content
	if choice.Message.ReasoningContent != "" {
		resp.Output = append(resp.Output, dto.ResponsesOutput{
			Type: "reasoning",
			Content: []dto.ResponsesOutputContent{
				{
					Type: "summary_text",
					Text: choice.Message.ReasoningContent,
				},
			},
		})
	}

	// Map usage
	resp.Usage = &chatResp.Usage

	return resp
}

// generateResponsesID creates a Responses-style ID ("resp_" + 24 hex chars).
func generateResponsesID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use current time nanoseconds as entropy
		ts := time.Now().UnixNano()
		binary.LittleEndian.PutUint64(b[:8], uint64(ts))
		binary.LittleEndian.PutUint64(b[8:], uint64(ts>>1))
	}
	return "resp_" + hex.EncodeToString(b)[:24]
}
