package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupDistributorAffinityTestDB(t *testing.T) {
	t.Helper()

	originalIsMasterNode := common.IsMasterNode
	originalSQLitePath := common.SQLitePath
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	t.Cleanup(func() {
		common.IsMasterNode = originalIsMasterNode
		common.SQLitePath = originalSQLitePath
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
		if model.DB != nil {
			sqlDB, err := model.DB.DB()
			if err == nil {
				_ = sqlDB.Close()
			}
		}
	})

	gin.SetMode(gin.TestMode)
	common.IsMasterNode = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.UsingSQLite = false
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, os.Setenv("SQL_DSN", "local"))

	require.NoError(t, model.InitDB())
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Model{}))
}

func TestDistribute_ClearsStaleAffinityAndFallsBackToAvailableChannel(t *testing.T) {
	setupDistributorAffinityTestDB(t)
	require.True(t, operation_setting.GetChannelAffinitySetting().Enabled)

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     101,
		Name:   "disabled-affinity",
		Key:    "sk-disabled",
		Status: common.ChannelStatusAutoDisabled,
		Group:  "default",
		Models: "gpt-5",
	}).Error)

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     202,
		Name:   "healthy-fallback",
		Key:    "sk-healthy",
		Status: common.ChannelStatusEnabled,
		Group:  "default",
		Models: "gpt-5",
	}).Error)

	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "default",
		Model:     "gpt-5",
		ChannelId: 202,
		Enabled:   true,
	}).Error)

	service.ClearChannelAffinityCacheAll()

	affinityValue := fmt.Sprintf("stale-%d", time.Now().UnixNano())
	seedBody := fmt.Sprintf(`{"model":"gpt-5","prompt_cache_key":"%s"}`, affinityValue)
	seedRec := httptest.NewRecorder()
	seedCtx, _ := gin.CreateTestContext(seedRec)
	seedCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(seedBody))
	seedCtx.Request.Header.Set("Content-Type", "application/json")
	_, found := service.GetPreferredChannelByAffinity(seedCtx, "gpt-5", "default")
	require.False(t, found)
	service.RecordChannelAffinity(seedCtx, 101)
	t.Cleanup(func() {
		service.ClearChannelAffinityCacheAll()
	})

	router := gin.New()
	router.Use(func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		c.Next()
	})
	router.Use(Distribute())
	router.POST("/v1/responses", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"channel_id": c.GetInt("channel_id"),
		})
	})

	body := seedBody
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		ChannelID int `json:"channel_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 202, resp.ChannelID)

	checkRec := httptest.NewRecorder()
	checkCtx, _ := gin.CreateTestContext(checkRec)
	checkCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	checkCtx.Request.Header.Set("Content-Type", "application/json")

	channelID, found := service.GetPreferredChannelByAffinity(checkCtx, "gpt-5", "default")
	require.True(t, found)
	require.Equal(t, 202, channelID)
}
