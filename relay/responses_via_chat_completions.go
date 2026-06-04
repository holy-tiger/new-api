package relay

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

type codexResponsesTrace struct {
	mu      sync.Mutex
	path    string
	builder strings.Builder
}

func newCodexResponsesTrace(c *gin.Context, info *relaycommon.RelayInfo) *codexResponsesTrace {
	defer func() {
		if recover() != nil {
			// Best-effort tracing only; never let trace collection affect relay flow.
		}
	}()
	requestID := ""
	requestPath := ""
	remoteAddr := ""
	channelID := 0
	if c != nil {
		requestID = c.GetString(common.RequestIdKey)
		if c.Request != nil && c.Request.URL != nil {
			requestPath = c.Request.URL.Path
		}
		if c.Request != nil {
			remoteAddr = c.Request.RemoteAddr
		}
	}
	if info != nil {
		channelID = info.ChannelId
	}
	if requestID == "" {
		requestID = fmt.Sprintf("trace-%d", time.Now().UnixNano())
	}
	traceDir := "/tmp/new-api-codex-traces"
	path := filepath.Join(traceDir, requestID+".jsonl")
	trace := &codexResponsesTrace{path: path}
	trace.builder.WriteString(fmt.Sprintf("{\"meta\":{\"request_id\":%q,\"request_path\":%q,\"channel_id\":%d,\"remote_addr\":%q}}\n",
		requestID,
		requestPath,
		channelID,
		remoteAddr,
	))
	return trace
}

func (t *codexResponsesTrace) record(event map[string]any) {
	if t == nil || event == nil {
		return
	}
	data, err := common.Marshal(event)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.builder.Write(data)
	t.builder.WriteByte('\n')
}

func (t *codexResponsesTrace) recordDone() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.builder.WriteString("{\"done\":true}\n")
}

func (t *codexResponsesTrace) flush(c *gin.Context) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		logger.LogWarn(c, fmt.Sprintf("codex responses trace mkdir failed: %v", err))
		return
	}
	if err := os.WriteFile(t.path, []byte(t.builder.String()), 0o644); err != nil {
		logger.LogWarn(c, fmt.Sprintf("codex responses trace write failed: %v", err))
		return
	}
	logger.LogInfo(c, fmt.Sprintf("codex responses trace written: %s", t.path))
}

func writeCodexChatRequestTrace(c *gin.Context, payload []byte) {
	if c == nil || len(payload) == 0 {
		return
	}
	requestID := c.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = fmt.Sprintf("trace-%d", time.Now().UnixNano())
	}
	traceDir := "/tmp/new-api-codex-traces"
	path := filepath.Join(traceDir, requestID+".chat-request.json")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		logger.LogWarn(c, fmt.Sprintf("codex chat request trace mkdir failed: %v", err))
		return
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		logger.LogWarn(c, fmt.Sprintf("codex chat request trace write failed: %v", err))
		return
	}
	logger.LogInfo(c, fmt.Sprintf("codex chat request trace written: %s", path))
}

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

	writeCodexChatRequestTrace(c, jsonData)

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
	trace := newCodexResponsesTrace(c, info)
	defer trace.flush(c)

	// 1. Create stream transform state
	state := &codexchat.StreamTransformState{
		ResponseID:              helper.GetResponseID(c),
		ToolCtx:                 toolCtx,
		SuppressReasoningOutput: true,
	}

	// 2. Set SSE headers
	helper.SetEventStreamHeaders(c)

	info.StreamStatus = relaycommon.NewStreamStatus()
	reader := bufio.NewReader(resp.Body)
	streamFailed := false

	for {
		select {
		case <-c.Request.Context().Done():
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
			streamFailed = true
		default:
		}
		if streamFailed {
			break
		}

		block, err := readSSEBlock(reader)
		if err != nil {
			if err == io.EOF {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
				break
			}
			info.StreamStatus.RecordError(err.Error())
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			failedEvent := state.BuildFailedEvent(fmt.Sprintf("Stream error: %v", err), "stream_error")
			trace.record(failedEvent)
			if writeErr := codexchat.WriteResponsesSSEEvent(c, failedEvent); writeErr != nil {
				logger.LogWarn(c, fmt.Sprintf("codexchatResponsesStreamHandler: failed to write stream_error event: %v", writeErr))
			}
			streamFailed = true
			break
		}
		if block.Data == "" {
			continue
		}
		if strings.TrimSpace(block.Data) == "[DONE]" {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
			break
		}

		info.SetFirstResponseTime()
		info.ReceivedResponseCount++

		payload := make(map[string]any)
		if err := common.Unmarshal([]byte(block.Data), &payload); err != nil {
			info.StreamStatus.RecordError(err.Error())
			continue
		}

		if block.Event == "error" || payload["error"] != nil {
			message, errorType := extractSSEError(payload)
			if message == "" {
				message = "upstream stream returned an error event"
			}
			failedEvent := state.BuildFailedEvent(message, errorType)
			trace.record(failedEvent)
			if writeErr := codexchat.WriteResponsesSSEEvent(c, failedEvent); writeErr != nil {
				logger.LogWarn(c, fmt.Sprintf("codexchatResponsesStreamHandler: failed to write response.failed event: %v", writeErr))
			}
			info.StreamStatus.RecordError(message)
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonHandlerStop, nil)
			streamFailed = true
			break
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.Unmarshal([]byte(block.Data), &chunk); err != nil {
			info.StreamStatus.RecordError(err.Error())
			continue
		}

		events := state.ProcessChatSSEChunk(&chunk)
		for _, event := range events {
			trace.record(event)
			if err := codexchat.WriteResponsesSSEEvent(c, event); err != nil {
				info.StreamStatus.RecordError(err.Error())
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
				streamFailed = true
				break
			}
		}
		if streamFailed {
			break
		}
	}

	if !streamFailed {
		finalEvents := state.Finalize()
		for _, event := range finalEvents {
			trace.record(event)
			if err := codexchat.WriteResponsesSSEEvent(c, event); err != nil {
				// Best-effort write; stream is already ending
				logger.LogWarn(c, fmt.Sprintf("codexchatResponsesStreamHandler: failed to write final event: %v", err))
			}
		}
		helper.Done(c)
	}
	trace.recordDone()

	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, fmt.Sprintf("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}

	// 5. After stream ends: cache function calls for continuation recovery
	ownerScope := codexchat.DetermineOwnerScope(info.TokenId, info.UserId)
	if len(state.CompletedFunctionCalls) > 0 {
		codexchat.GlobalHistoryStore.Store(ownerScope, info.ChannelId, state.ResponseID, sessionScope, state.CompletedFunctionCalls)
	}

	if streamFailed {
		return nil, nil
	}

	// 6. Recover usage from state, fallback to empty usage
	var usage dto.Usage
	if state.LatestUsage != nil {
		usage = *state.LatestUsage
	}

	return &usage, nil
}

type sseBlock struct {
	Event string
	Data  string
}

func readSSEBlock(reader *bufio.Reader) (sseBlock, error) {
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return sseBlock{}, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(lines) == 0 {
				if err == io.EOF {
					return sseBlock{}, io.EOF
				}
				continue
			}
			return parseSSEBlock(lines), nil
		}
		lines = append(lines, line)

		if err == io.EOF {
			if len(lines) == 0 {
				return sseBlock{}, io.EOF
			}
			return parseSSEBlock(lines), nil
		}
	}
}

func parseSSEBlock(lines []string) sseBlock {
	block := sseBlock{}
	dataLines := make([]string, 0, len(lines))
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "event:"):
			block.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	block.Data = strings.Join(dataLines, "\n")
	return block
}

func extractSSEError(payload map[string]any) (string, string) {
	if payload == nil {
		return "", ""
	}
	errValue := payload["error"]
	if errValue == nil {
		errValue = payload
	}
	errMap, ok := errValue.(map[string]any)
	if !ok {
		if text := common.Interface2String(errValue); text != "" {
			return text, ""
		}
		return fmt.Sprintf("%v", errValue), ""
	}

	message := common.Interface2String(errMap["message"])
	if message == "" {
		message = common.Interface2String(errMap["detail"])
	}
	if message == "" {
		message = fmt.Sprintf("%v", errMap)
	}

	errorType := common.Interface2String(errMap["type"])
	if errorType == "" {
		errorType = common.Interface2String(errMap["code"])
	}
	return message, errorType
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
