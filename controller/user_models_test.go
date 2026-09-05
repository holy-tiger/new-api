package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetUserModelsFiltersUserWhitelist(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.Create(&model.User{
		Id:       1003,
		Username: "user-models-whitelist",
		Password: "password",
		Group:    "default",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: "zz-user-open", ChannelId: 1, Enabled: true},
		{Group: "default", Model: "zz-user-private", ChannelId: 1, Enabled: true},
	}).Error)
	require.NoError(t, db.Create(&[]model.Model{
		{ModelName: "zz-user-open", NameRule: model.NameRuleExact},
		{ModelName: "zz-user-private", NameRule: model.NameRuleExact, UserWhitelist: []int{12}},
	}).Error)
	model.InvalidateModelAccessCache()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/self/models", nil)
	ctx.Set("id", 1003)
	ctx.Set("role", common.RoleCommonUser)
	GetUserModels(ctx)

	var response struct {
		Success bool     `json:"success"`
		Data    []string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, []string{"zz-user-open"}, response.Data)
}
