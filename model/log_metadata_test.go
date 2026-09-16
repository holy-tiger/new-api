package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExtractClientMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("originator", "opencode")
	c.Request.Header.Set("User-Agent", "opencode/1.2.3")
	c.Request.Header.Set("session-id", "session-123")
	c.Request.Header.Set("x-codex-turn-metadata", `{"installation_id":"installation-123","sandbox":"seatbelt","workspaces":{"/workspace/project":{"associated_remote_urls":{"origin":"git@github.com:org/repo.git"}}}}`)

	metadata := extractClientMetadata(c)
	require.Equal(t, "installation-123", metadata["installation_id"])
	require.Equal(t, "opencode", metadata["originator"])
	require.Equal(t, "opencode/1.2.3", metadata["user_agent"])
	require.Equal(t, "session-123", metadata["session_id"])
	require.Equal(t, "seatbelt", metadata["sandbox"])
	workspaces, ok := metadata["workspaces"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, map[string]interface{}{"remote_urls": map[string]interface{}{"origin": "git@github.com:org/repo.git"}}, workspaces["/workspace/project"])
}

func TestExtractClientMetadataMissingFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

	metadata := extractClientMetadata(c)
	require.Equal(t, "", metadata["installation_id"])
	require.Equal(t, "", metadata["originator"])
	require.Equal(t, "", metadata["user_agent"])
	require.Equal(t, "", metadata["session_id"])
	require.Equal(t, "", metadata["sandbox"])
	require.Equal(t, map[string]interface{}{}, metadata["workspaces"])

	encoded := common.MapToJsonStr(map[string]interface{}{"client_metadata": metadata})
	var decoded map[string]interface{}
	require.NoError(t, common.UnmarshalJsonStr(encoded, &decoded))
	require.Contains(t, decoded, "client_metadata")
}
