package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// OaiStreamAggregationHandler reads an SSE stream from upstream and aggregates
// it into a single non-stream OpenAI chat completion response for the client.
// Used when the upstream requires streaming but the client requested non-stream.
func OaiStreamAggregationHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var usage = &dto.Usage{}
	var responseId string
	var model string
	var finishReason string
	var createAt int64

	// Tool calls aggregation: map from index to accumulated tool call
	type aggregatedToolCall struct {
		ID       string
		Type     any
		Name     string
		Args     strings.Builder
	}
	toolCallMap := make(map[int]*aggregatedToolCall)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and non-data lines
		if len(line) < 6 {
			continue
		}
		if line[:5] != "data:" {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" {
			continue
		}
		if strings.HasPrefix(data, "[DONE]") {
			break
		}

		var streamResp dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResp); err != nil {
			continue
		}

		if streamResp.Id != "" {
			responseId = streamResp.Id
		}
		if streamResp.Model != "" {
			model = streamResp.Model
		}
		if streamResp.SystemFingerprint != nil {
			// systemFingerprint available in streamResp but not needed for non-stream response
		}
		if streamResp.Created > 0 {
			createAt = streamResp.Created
		}

		for _, choice := range streamResp.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
			if choice.Delta.Content != nil {
				contentBuilder.WriteString(*choice.Delta.Content)
			}
			if choice.Delta.ReasoningContent != nil {
				reasoningBuilder.WriteString(*choice.Delta.ReasoningContent)
			}
			if choice.Delta.Reasoning != nil {
				reasoningBuilder.WriteString(*choice.Delta.Reasoning)
			}
			if choice.Delta.ToolCalls != nil {
				for _, tc := range choice.Delta.ToolCalls {
					idx := 0
					if tc.Index != nil {
						idx = *tc.Index
					}
					if existing, ok := toolCallMap[idx]; ok {
						if tc.Function.Arguments != "" {
							existing.Args.WriteString(tc.Function.Arguments)
						}
						if tc.Function.Name != "" {
							existing.Name = tc.Function.Name
						}
						if tc.ID != "" {
							existing.ID = tc.ID
						}
					} else {
						atc := &aggregatedToolCall{
							ID:   tc.ID,
							Type: tc.Type,
							Name: tc.Function.Name,
						}
						atc.Args.WriteString(tc.Function.Arguments)
						toolCallMap[idx] = atc
					}
				}
			}
		}

		if streamResp.Usage != nil && service.ValidUsage(streamResp.Usage) {
			usage = streamResp.Usage
		}
	}

	// Build tool calls list from map, sorted by index
	var toolCalls []dto.ToolCallResponse
	if len(toolCallMap) > 0 {
		indices := make([]int, 0, len(toolCallMap))
		for idx := range toolCallMap {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		toolCalls = make([]dto.ToolCallResponse, 0, len(indices))
		for _, idx := range indices {
			atc := toolCallMap[idx]
			tc := dto.ToolCallResponse{
				ID:   atc.ID,
				Type: atc.Type,
				Function: dto.FunctionResponse{
					Name:      atc.Name,
					Arguments: atc.Args.String(),
				},
			}
			tc.SetIndex(idx)
			toolCalls = append(toolCalls, tc)
		}
	}

	// Marshal tool calls as json.RawMessage for the Message.ToolCalls field
	var toolCallsRaw json.RawMessage
	if len(toolCalls) > 0 {
		var err error
		toolCallsRaw, err = common.Marshal(toolCalls)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
	}

	// Construct final non-stream response
	response := dto.OpenAITextResponse{
		Id:      responseId,
		Object:  "chat.completion",
		Created: createAt,
		Model:   model,
		Choices: []dto.OpenAITextResponseChoice{
			{
				Index: 0,
				Message: dto.Message{
					Role:             "assistant",
					Content:          contentBuilder.String(),
					ReasoningContent: reasoningBuilder.String(),
					ToolCalls:        toolCallsRaw,
				},
				FinishReason: finishReason,
			},
		},
		Usage: *usage,
	}

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
}
