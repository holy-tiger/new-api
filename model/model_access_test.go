package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupModelAccessTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	previousDB := DB
	DB = db
	require.NoError(t, DB.AutoMigrate(&Model{}))
	InvalidateModelAccessCache()
	t.Cleanup(func() {
		InvalidateModelAccessCache()
		DB = previousDB
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
}

func TestIsModelAccessibleUsesRulePrecedence(t *testing.T) {
	setupModelAccessTestDB(t)
	require.NoError(t, DB.Create(&[]Model{
		{ModelName: "model-x", NameRule: NameRuleExact, UserWhitelist: []int{2}},
		{ModelName: "model-", NameRule: NameRulePrefix, UserWhitelist: []int{3}},
		{ModelName: "-x", NameRule: NameRuleSuffix, UserWhitelist: []int{4}},
		{ModelName: "odel", NameRule: NameRuleContains, UserWhitelist: []int{5}},
	}).Error)

	allowed, err := IsModelAccessible("model-x", 2, common.RoleCommonUser)
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = IsModelAccessible("model-x", 3, common.RoleCommonUser)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestIsModelAccessibleRootAndAdminSemantics(t *testing.T) {
	setupModelAccessTestDB(t)
	require.NoError(t, DB.Create(&Model{
		ModelName:     "restricted-model",
		NameRule:      NameRuleExact,
		UserWhitelist: []int{12},
	}).Error)

	allowed, err := IsModelAccessible("restricted-model", 99, common.RoleRootUser)
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = IsModelAccessible("restricted-model", 99, common.RoleAdminUser)
	require.NoError(t, err)
	require.False(t, allowed)

	allowed, err = IsModelAccessible("restricted-model", 12, common.RoleCommonUser)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestFilterModelsForUserRemovesRestrictedModels(t *testing.T) {
	setupModelAccessTestDB(t)
	require.NoError(t, DB.Create(&[]Model{
		{ModelName: "open-model", NameRule: NameRuleExact},
		{ModelName: "private-model", NameRule: NameRuleExact, UserWhitelist: []int{12}},
	}).Error)

	models, err := FilterModelsForUser([]string{"open-model", "private-model"}, 99, common.RoleCommonUser)
	require.NoError(t, err)
	require.Equal(t, []string{"open-model"}, models)

	models, err = FilterModelsForUser([]string{"open-model", "private-model"}, 12, common.RoleCommonUser)
	require.NoError(t, err)
	require.Equal(t, []string{"open-model", "private-model"}, models)
}
