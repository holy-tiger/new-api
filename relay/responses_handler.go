package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/relay/channel"
)

func ResponsesHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		switch info.ApiType {
		case appconstant.APITypeOpenAI, appconstant.APITypeCodex:
		default:
			return types.NewErrorWithStatusCode(
				fmt.Errorf("unsupported endpoint %q for api type %d", "/v1/responses/compact", info.ApiType),
				types.ErrorCodeInvalidRequest,
				http.StatusBadRequest,
				types.ErrOptionWithSkipRetry(),
			)
		}
	}

	var responsesReq *dto.OpenAIResponsesRequest
	switch req := info.Request.(type) {
	case *dto.OpenAIResponsesRequest:
		responsesReq = req
	case *dto.OpenAIResponsesCompactionRequest:
		responsesReq = &dto.OpenAIResponsesRequest{
			Model:              req.Model,
			Input:              req.Input,
			Instructions:       req.Instructions,
			PreviousResponseID: req.PreviousResponseID,
		}
	default:
		return types.NewErrorWithStatusCode(
			fmt.Errorf("invalid request type, expected dto.OpenAIResponsesRequest or dto.OpenAIResponsesCompactionRequest, got %T", info.Request),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	request, err := common.DeepCopy(responsesReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to GeneralOpenAIRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	// Bridge: Responses -> Chat Completions (for Codex clients talking to Chat-only upstreams)
	if service.ShouldResponsesUseChatCompletionsGlobal(info.ChannelId, info.ChannelType, info.UpstreamModelName) {
		usage, err := responsesViaChatCompletions(c, info, adaptor, request)
		if err != nil {
			return err
		}
		service.PostTextConsumeQuota(c, info, usage, nil)
		return nil
	}

	var requestBody io.Reader
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
		}

		bodyBytes, _ := storage.Bytes()
		if len(bodyBytes) > 0 {
			// Apply adapter-specific body fixes even in passthrough mode.
			// For Codex channels, the backend requires certain body fields (e.g., store=false)
			// that must be enforced regardless of passthrough setting.
			bodyBytes, err = applyAdapterPassthroughBodyFixes(bodyBytes, info)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}
			requestBody = bytes.NewBuffer(bodyBytes)
		} else {
			requestBody = common.ReaderOnly(storage)
		}

		// Even with body passthrough, apply param override for header operations
		// (e.g., affinity rule's pass_headers for Originator, Session_id, etc.)
		// The body modifications are discarded since we use the raw body above.
		if len(info.ParamOverride) > 0 {
			if len(bodyBytes) > 0 {
				_, err = relaycommon.ApplyParamOverrideWithRelayInfo(bodyBytes, info)
				if err != nil {
					return newAPIErrorFromParamOverride(err)
				}
			}
		}
	} else {
		convertedRequest, err := adaptor.ConvertOpenAIResponsesRequest(c, info, *request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// remove disabled fields for OpenAI Responses API
		jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// apply param override
		if len(info.ParamOverride) > 0 {
			jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
			if err != nil {
				return newAPIErrorFromParamOverride(err)
			}
		}

		if common.DebugEnabled {
			println("requestBody: ", string(jsonData))
		}
		requestBody = bytes.NewBuffer(jsonData)
	}

	// [CACHE-DIAG] Log cache-relevant request body fields (not the full body) for diagnosis.
	// These fields are critical for prompt cache hit rate:
	// - prompt_cache_key: the cache key sent by client (used for affinity routing)
	// - store: must be consistent (Codex adapter forces false)
	// - previous_response_id: linked conversation for server-side cache
	// - model: the resolved upstream model name
	logCacheDiagFields(c, info, requestBody)

	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	if resp != nil {
		httpResp = resp.(*http.Response)

		if httpResp.StatusCode != http.StatusOK {
			newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return newAPIError
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	usageDto := usage.(*dto.Usage)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact {
		originModelName := info.OriginModelName
		originPriceData := info.PriceData

		_, err := helper.ModelPriceHelper(c, info, info.GetEstimatePromptTokens(), &types.TokenCountMeta{})
		if err != nil {
			info.OriginModelName = originModelName
			info.PriceData = originPriceData
			return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithSkipRetry(), types.ErrOptionWithStatusCode(http.StatusBadRequest))
		}
		service.PostTextConsumeQuota(c, info, usageDto, nil)

		info.OriginModelName = originModelName
		info.PriceData = originPriceData
		return nil
	}

	if strings.HasPrefix(info.OriginModelName, "gpt-4o-audio") {
		service.PostAudioConsumeQuota(c, info, usageDto, "")
	} else {
		service.PostTextConsumeQuota(c, info, usageDto, nil)
	}
	return nil
}

// responsesViaChatCompletions is a stub for bridging /v1/responses to /v1/chat/completions.
// Full implementation will be added in a future task.
func responsesViaChatCompletions(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, responsesReq *dto.OpenAIResponsesRequest) (*dto.Usage, *types.NewAPIError) {
	return nil, types.NewErrorWithStatusCode(
		fmt.Errorf("responses via chat completions bridge not yet implemented"),
		types.ErrorCodeInvalidRequest,
		http.StatusInternalServerError,
		types.ErrOptionWithSkipRetry(),
	)
}

// applyAdapterPassthroughBodyFixes applies adapter-specific body transformations that
// are necessary even when body passthrough is enabled. When passthrough mode skips the
// adapter's ConvertOpenAIResponsesRequest, certain backend-required fields may be missing
// or incorrect (e.g., Codex backend requires store=false). This function applies minimal
// JSON fixes using gjson/sjson to avoid a full unmarshal/marshal cycle.
func applyAdapterPassthroughBodyFixes(bodyBytes []byte, info *relaycommon.RelayInfo) ([]byte, error) {
	if len(bodyBytes) == 0 || info == nil {
		return bodyBytes, nil
	}

	// Only apply fixes for Codex channel type (57)
	if info.ChannelType != appconstant.ChannelTypeCodex {
		return bodyBytes, nil
	}

	isCompact := info.RelayMode == relayconstant.RelayModeResponsesCompact
	var err error

	// Codex backend requires the "instructions" field to be present.
	// If missing, default to empty string (matching Codex CLI behavior).
	if !gjson.GetBytes(bodyBytes, "instructions").Exists() {
		bodyBytes, err = sjson.SetBytes(bodyBytes, "instructions", "")
		if err != nil {
			return bodyBytes, err
		}
	}

	if !isCompact {
		// codex: store must be false
		bodyBytes, err = sjson.SetBytes(bodyBytes, "store", false)
		if err != nil {
			return bodyBytes, err
		}
		// Remove max_output_tokens (Codex backend doesn't accept it)
		if gjson.GetBytes(bodyBytes, "max_output_tokens").Exists() {
			bodyBytes, err = sjson.DeleteBytes(bodyBytes, "max_output_tokens")
			if err != nil {
				return bodyBytes, err
			}
		}
		// Remove temperature (Codex backend doesn't accept it)
		if gjson.GetBytes(bodyBytes, "temperature").Exists() {
			bodyBytes, err = sjson.DeleteBytes(bodyBytes, "temperature")
			if err != nil {
				return bodyBytes, err
			}
		}
	}

	return bodyBytes, nil
}

// logCacheDiagFields logs cache-relevant fields from the request body for prompt cache diagnosis.
// It extracts only key fields (prompt_cache_key, store, previous_response_id, model, service_tier,
// safety_identifier) without printing the full body, which could be very large.
func logCacheDiagFields(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) {
	if !common.DebugEnabled {
		return
	}

	// Try to extract body bytes for field extraction.
	var bodyBytes []byte
	switch v := requestBody.(type) {
	case *bytes.Buffer:
		bodyBytes = v.Bytes()
	default:
		// For passthrough mode, requestBody is a wrapped *bytes.Buffer that we can't easily read.
		// Fall back to extracting from the body storage instead.
		if storage, err := common.GetBodyStorage(c); err == nil {
			if bs, bsErr := storage.Bytes(); bsErr == nil {
				bodyBytes = bs
			}
		}
	}

	if len(bodyBytes) == 0 {
		logger.LogDebug(c, "[CACHE-DIAG] could not extract body bytes for diagnosis")
		return
	}

	var fields []string
	fields = append(fields, "model="+gjson.GetBytes(bodyBytes, "model").String())

	if pck := gjson.GetBytes(bodyBytes, "prompt_cache_key"); pck.Exists() {
		fields = append(fields, "prompt_cache_key="+pck.String())
	}
	if store := gjson.GetBytes(bodyBytes, "store"); store.Exists() {
		fields = append(fields, "store="+store.String())
	}
	if prevID := gjson.GetBytes(bodyBytes, "previous_response_id"); prevID.Exists() {
		fields = append(fields, "previous_response_id="+prevID.String())
	}
	if st := gjson.GetBytes(bodyBytes, "service_tier"); st.Exists() {
		fields = append(fields, "service_tier="+st.String())
	}
	if si := gjson.GetBytes(bodyBytes, "safety_identifier"); si.Exists() {
		fields = append(fields, "safety_identifier="+si.String())
	}

	// Body size indicates whether the request is large enough for meaningful caching
	fields = append(fields, fmt.Sprintf("body_len=%d", len(bodyBytes)))

	// Channel info for affinity diagnosis
	fields = append(fields, fmt.Sprintf("channel_id=%d channel_type=%d passthrough_body=%v param_override=%d",
		info.ChannelId, info.ChannelType, info.ChannelSetting.PassThroughBodyEnabled, len(info.ParamOverride)))

	if info.UseRuntimeHeadersOverride {
		keys := make([]string, 0, len(info.RuntimeHeadersOverride))
		for k := range info.RuntimeHeadersOverride {
			keys = append(keys, k)
		}
		fields = append(fields, "runtime_header_override_keys="+strings.Join(keys, ","))
	}

	logger.LogDebug(c, "[CACHE-DIAG] %s", strings.Join(fields, " | "))
}
