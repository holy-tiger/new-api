package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/codexchat"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// responsesViaChatCompletions bridges /v1/responses to /v1/chat/completions.
// It converts a Responses API request to Chat Completions format, sends it to the
// upstream provider, and converts the response back to Responses format.
func responsesViaChatCompletions(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, responsesReq *dto.OpenAIResponsesRequest) (*dto.Usage, *types.NewAPIError) {
	// 1. Determine cache scopes — compute session scope once and reuse everywhere
	ownerScope := codexchat.DetermineOwnerScope(info.TokenId, info.UserId)
	sessionScope := codexchat.ResolveResponsesSessionScope(
		responsesReq,
		c.GetHeader("session_id"),
		c.GetHeader("x-session-id"),
	)

	// 2. Enrich request with history for continuation recovery
	if err := codexchat.EnrichRequestWithHistory(codexchat.GlobalHistoryStore, ownerScope, info.ChannelId, sessionScope, responsesReq); err != nil {
		logger.LogWarn(c, fmt.Sprintf("responsesViaChatCompletions: enrich history failed: %v", err))
	}

	// 3. Convert Responses -> Chat Completions
	chatReq, toolCtx, err := codexchat.ResponsesRequestToChatCompletionsRequest(responsesReq)
	if err != nil {
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	// 4. Marshal, RemoveDisabledFields, ApplyParamOverride
	chatJSON, err := common.Marshal(chatReq)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	chatJSON, err = relaycommon.RemoveDisabledFields(chatJSON, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	if len(info.ParamOverride) > 0 {
		chatJSON, err = relaycommon.ApplyParamOverrideWithRelayInfo(chatJSON, info)
		if err != nil {
			return nil, newAPIErrorFromParamOverride(err)
		}
	}

	// 5. Temporarily switch relay mode to Chat Completions
	savedRelayMode := info.RelayMode
	savedRequestURLPath := info.RequestURLPath
	defer func() {
		info.RelayMode = savedRelayMode
		info.RequestURLPath = savedRequestURLPath
	}()
	info.RelayMode = relayconstant.RelayModeChatCompletions
	info.RequestURLPath = "/v1/chat/completions"

	// 6. Unmarshal modified JSON back to GeneralOpenAIRequest and convert
	var finalChatReq dto.GeneralOpenAIRequest
	if err := common.Unmarshal(chatJSON, &finalChatReq); err != nil {
		return nil, types.NewError(err, types.ErrorCodeChannelParamOverrideInvalid, types.ErrOptionWithSkipRetry())
	}

	convertedRequest, err := adaptor.ConvertOpenAIRequest(c, info, &finalChatReq)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

	// 7. Marshal converted request, RemoveDisabledFields again
	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	// 8. Send request to upstream
	requestBody := bytes.NewBuffer(jsonData)
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	if resp == nil {
		return nil, types.NewOpenAIError(nil, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	// 9. Check status code and handle errors
	httpResp := resp.(*http.Response)
	if httpResp.StatusCode != http.StatusOK {
		newApiErr := service.RelayErrorHandler(c.Request.Context(), httpResp, false)
		service.ResetStatusCode(newApiErr, statusCodeMappingStr)
		return nil, newApiErr
	}

	// 10. Determine streaming from Content-Type header
	info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")

	// 11. Route to streaming or non-streaming handler
	if info.IsStream {
		usage, newApiErr := codexchatResponsesStreamHandler(c, info, httpResp, sessionScope, toolCtx)
		if newApiErr != nil {
			service.ResetStatusCode(newApiErr, statusCodeMappingStr)
			return nil, newApiErr
		}
		return usage, nil
	}

	usage, newApiErr := codexchatResponsesHandler(c, info, httpResp, sessionScope, toolCtx)
	if newApiErr != nil {
		service.ResetStatusCode(newApiErr, statusCodeMappingStr)
		return nil, newApiErr
	}
	return usage, nil
}

// codexchatResponsesHandler handles non-streaming Chat Completions response and
// converts it back to Responses format.
func codexchatResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, sessionScope string, toolCtx *codexchat.ChatToolContext) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// 1. Read full response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	// 2. Unmarshal as Chat Completions response
	var chatResp dto.OpenAITextResponse
	if err := common.Unmarshal(body, &chatResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	// 3. Check for OpenAI error
	if oaiErr := chatResp.GetOpenAIError(); oaiErr != nil && oaiErr.Type != "" {
		return nil, types.WithOpenAIError(*oaiErr, resp.StatusCode)
	}

	// 4. Convert Chat Completions -> Responses
	responsesResp := codexchat.ChatCompletionsResponseToResponsesResponse(&chatResp, toolCtx)

	// 5. Cache function calls for continuation recovery
	ownerScope := codexchat.DetermineOwnerScope(info.TokenId, info.UserId)
	calls := extractFunctionCallsFromOutput(responsesResp.Output)
	if len(calls) > 0 {
		codexchat.GlobalHistoryStore.Store(ownerScope, info.ChannelId, responsesResp.ID, sessionScope, calls)
	}

	// 6. Write response to client
	responseBody, err := common.Marshal(responsesResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// 7. Return usage
	return &chatResp.Usage, nil
}

// codexchatResponsesStreamHandler handles streaming Chat Completions SSE response
// and converts it to Responses SSE events.
func codexchatResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, sessionScope string, toolCtx *codexchat.ChatToolContext) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// 1. Create stream transform state
	state := &codexchat.StreamTransformState{
		ResponseID: helper.GetResponseID(c),
		ToolCtx:    toolCtx,
	}

	// 2. Set SSE headers
	helper.SetEventStreamHeaders(c)

	// 3. Stream scan and transform
	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		// Check for [DONE] sentinel
		if strings.TrimSpace(data) == "[DONE]" {
			return
		}

		// Unmarshal as Chat Completions SSE chunk
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.Unmarshal([]byte(data), &chunk); err != nil {
			sr.Error(err)
			return
		}

		// Process the chunk and emit Responses SSE events
		events := state.ProcessChatSSEChunk(&chunk)
		for _, event := range events {
			if err := codexchat.WriteResponsesSSEEvent(c, event); err != nil {
				sr.Error(err)
				return
			}
		}

		// Check for finish reason — signal stream completion
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				sr.Done()
				return
			}
		}
	})

	// 4. After stream ends: cache function calls for continuation recovery
	ownerScope := codexchat.DetermineOwnerScope(info.TokenId, info.UserId)
	if len(state.CompletedFunctionCalls) > 0 {
		codexchat.GlobalHistoryStore.Store(ownerScope, info.ChannelId, state.ResponseID, sessionScope, state.CompletedFunctionCalls)
	}

	// 5. Recover usage from state, fallback to empty usage
	var usage dto.Usage
	if state.LatestUsage != nil {
		usage = *state.LatestUsage
	}

	return &usage, nil
}

// extractFunctionCallsFromOutput iterates over Responses output items and
// extracts function_call items as CachedFunctionCall entries.
func extractFunctionCallsFromOutput(output []dto.ResponsesOutput) []codexchat.CachedFunctionCall {
	var calls []codexchat.CachedFunctionCall
	for _, item := range output {
		if item.Type != "function_call" {
			continue
		}
		calls = append(calls, codexchat.CachedFunctionCall{
			CallID:    item.CallId,
			Name:      item.Name,
			Arguments: item.ArgumentsString(),
		})
	}
	return calls
}
