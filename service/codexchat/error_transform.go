package codexchat

import (
	"github.com/QuantumNous/new-api/common"
)

// NormalizeErrorResponse converts a Chat upstream error into a Responses-compatible error envelope.
// Returns the normalized body bytes, HTTP status code, and content type.
func NormalizeErrorResponse(chatBody []byte, statusCode int) ([]byte, int, string) {
	if len(chatBody) == 0 {
		resp := map[string]any{
			"error": map[string]any{
				"message": "empty upstream response",
				"type":    "upstream_error",
				"code":    statusCode,
			},
		}
		body, _ := common.Marshal(resp)
		return body, statusCode, "application/json"
	}

	// Try standard OpenAI error format: {"error": {"message": "...", "type": "...", ...}}
	var oaiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Param   any    `json:"param"`
		} `json:"error"`
	}

	if err := common.Unmarshal(chatBody, &oaiErr); err == nil && oaiErr.Error.Message != "" {
		resp := map[string]any{
			"error": map[string]any{
				"message": oaiErr.Error.Message,
				"type":    oaiErr.Error.Type,
				"code":    oaiErr.Error.Code,
				"param":   oaiErr.Error.Param,
			},
		}
		body, _ := common.Marshal(resp)
		return body, statusCode, "application/json"
	}

	// Try generic JSON object with known error fields
	var unknownErr map[string]any
	if err := common.Unmarshal(chatBody, &unknownErr); err == nil {
		msg := extractFirstString(unknownErr, "message", "msg", "detail")
		if msg == "" {
			msg = "upstream error"
		}
		resp := map[string]any{
			"error": map[string]any{
				"message": msg,
				"type":    "upstream_error",
				"code":    statusCode,
			},
		}
		body, _ := common.Marshal(resp)
		return body, statusCode, "application/json"
	}

	// Plain text, HTML, or unparseable body
	msg := string(chatBody)
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	if msg == "" {
		msg = "upstream error"
	}
	resp := map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "upstream_error",
			"code":    statusCode,
		},
	}
	body, _ := common.Marshal(resp)
	return body, statusCode, "application/json"
}

// extractFirstString returns the first non-empty string value for any of the given keys.
func extractFirstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
