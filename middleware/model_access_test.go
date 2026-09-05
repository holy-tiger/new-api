package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnforceModelAccessAbortsBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	db, err := gorm.Open(sqlite.Open("file:model_access_middleware?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Channel{}))
	require.NoError(t, db.Create(&model.Model{
		ModelName:     "private-model",
		NameRule:      model.NameRuleExact,
		UserWhitelist: []int{12},
	}).Error)
	model.InvalidateModelAccessCache()
	t.Cleanup(func() {
		model.InvalidateModelAccessCache()
		model.DB = previousDB
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set("id", 99)
	common.SetContextKey(c, constant.ContextKeyUserRole, common.RoleCommonUser)

	require.False(t, enforceModelAccess(c, "private-model"))
	require.True(t, c.IsAborted())
	require.Equal(t, 403, c.Writer.Status())
	require.Contains(t, recorder.Body.String(), "model_access_denied")
}
