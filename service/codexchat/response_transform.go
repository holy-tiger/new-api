package codexchat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
)

func customToolInputFromArguments(arguments string) json.RawMessage {
	if arguments == "" {
		return nil
	}
	var payload map[string]json.RawMessage
	if err := common.Unmarshal([]byte(arguments), &payload); err != nil {
		return json.RawMessage(`""`)
	}
	if input, ok := payload[customToolInputField]; ok {
		return input
	}
	return nil
}

func toolSearchArgumentsFromString(arguments string) json.RawMessage {
	if strings.TrimSpace(arguments) == "" {
		return json.RawMessage(`{}`)
	}
	var value any
	if err := common.Unmarshal([]byte(arguments), &value); err == nil {
		if payload, ok := value.(map[string]any); ok && payload != nil {
			data, marshalErr := common.Marshal(payload)
			if marshalErr == nil {
				return data
			}
		}
		if query, ok := value.(string); ok {
			arguments = query
		}
	}
	data, err := common.Marshal(map[string]any{
		"query": arguments,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

func responseToolCallItemID(callID string, chatName string, toolCtx *ChatToolContext) string {
	if toolCtx != nil {
		if spec, ok := toolCtx.LookupToolSpec(chatName); ok && spec.Kind == ChatToolKindCustom {
			return "ctc_" + callID
		}
	}
	return "fc_" + callID
}

func marshalJSONString(value string) json.RawMessage {
	data, err := common.Marshal(value)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return data
}

func restoreResponsesToolOutput(tc dto.ToolCallResponse, toolCtx *ChatToolContext) dto.ResponsesOutput {
	spec, ok := toolCtx.LookupToolSpec(tc.Function.Name)
	if !ok && toolCtx != nil {
		switch toolCtx.RestoreToolType(tc.Function.Name, "function_call") {
		case "custom_tool_call":
			spec = ChatToolSpec{
				Kind:     ChatToolKindCustom,
				Name:     tc.Function.Name,
				ChatName: tc.Function.Name,
			}
			ok = true
		case "tool_search_call":
			spec = ChatToolSpec{
				Kind:     ChatToolKindToolSearch,
				Name:     toolSearchProxyName,
				ChatName: tc.Function.Name,
			}
			ok = true
		}
	}

	output := dto.ResponsesOutput{
		Type:   "function_call",
		ID:     tc.ID,
		CallId: tc.ID,
		Status: "completed",
		Name:   tc.Function.Name,
	}

	if !ok {
		output.Arguments = marshalJSONString(tc.Function.Arguments)
		return output
	}

	switch spec.Kind {
	case ChatToolKindCustom:
		output.Type = "custom_tool_call"
		output.Name = spec.Name
		output.Input = customToolInputFromArguments(tc.Function.Arguments)
	case ChatToolKindToolSearch:
		output.Type = "tool_search_call"
		output.Name = spec.Name
		output.Arguments = toolSearchArgumentsFromString(tc.Function.Arguments)
	default:
		output.Type = "function_call"
		output.Name = spec.Name
		output.Namespace = spec.Namespace
		output.Arguments = marshalJSONString(tc.Function.Arguments)
	}

	return output
}

// ChatCompletionsResponseToResponsesResponse converts a Chat Completions JSON
// response into a Codex Responses API JSON response. If toolCtx is provided,
// it is used to restore the original Responses tool type for non-standard tools.
func ChatCompletionsResponseToResponsesResponse(chatResp *dto.OpenAITextResponse, toolCtx *ChatToolContext) *dto.OpenAIResponsesResponse {
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
				resp.Output = append(resp.Output, restoreResponsesToolOutput(tc, toolCtx))
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

func responseIDFromChatID(id string) string {
	if id == "" {
		return generateResponsesID()
	}
	if strings.HasPrefix(id, "resp_") {
		return id
	}
	return "resp_" + id
}
