package model

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rankingUser(t *testing.T, username, displayName string) User {
	t.Helper()
	user := User{
		Username: username, DisplayName: displayName,
		Password: "test-password", AffCode: "aff-" + username,
		Status: common.UserStatusEnabled,
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func rankingRow(t *testing.T, row QuotaData) {
	t.Helper()
	require.NoError(t, DB.Create(&row).Error)
}

func TestGetUserUsageRankingAggregatesByUserID(t *testing.T) {
	truncateTables(t)
	alice := rankingUser(t, "alice-current", "Alice")
	bob := rankingUser(t, "bob", "Bob")
	rankingRow(t, QuotaData{UserID: alice.Id, Username: "alice-old", ModelName: "a", CreatedAt: 1000, TokenUsed: 100, Quota: 30, Count: 1})
	rankingRow(t, QuotaData{UserID: alice.Id, Username: "alice-current", ModelName: "b", CreatedAt: 2000, TokenUsed: 300, Quota: 70, Count: 2})
	rankingRow(t, QuotaData{UserID: bob.Id, Username: "bob", ModelName: "a", CreatedAt: 2000, TokenUsed: 250, Quota: 90, Count: 4})

	items, total, err := GetUserUsageRanking(UserUsageRankingQuery{
		StartTime: 900, EndTime: 2100, Page: 1, PageSize: 20,
		SortBy: UserUsageRankingSortTokenUsed, SortOrder: UserUsageRankingOrderDesc,
	})
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.EqualValues(t, 2, total)
	assert.Equal(t, alice.Id, items[0].UserID)
	assert.Equal(t, "alice-current", items[0].Username)
	assert.Equal(t, "Alice", items[0].DisplayName)
	assert.EqualValues(t, 400, items[0].TokenUsed)
	assert.EqualValues(t, 100, items[0].Quota)
	assert.EqualValues(t, 3, items[0].Count)
}

func TestGetUserUsageRankingSortsAndPaginatesStableTies(t *testing.T) {
	truncateTables(t)
	first := rankingUser(t, "first", "First")
	second := rankingUser(t, "second", "Second")
	third := rankingUser(t, "third", "Third")
	rankingRow(t, QuotaData{UserID: first.Id, Username: first.Username, ModelName: "m", CreatedAt: 1000, TokenUsed: 10, Quota: 50, Count: 2})
	rankingRow(t, QuotaData{UserID: second.Id, Username: second.Username, ModelName: "m", CreatedAt: 1000, TokenUsed: 20, Quota: 50, Count: 3})
	rankingRow(t, QuotaData{UserID: third.Id, Username: third.Username, ModelName: "m", CreatedAt: 1000, TokenUsed: 30, Quota: 10, Count: 1})

	items, total, err := GetUserUsageRanking(UserUsageRankingQuery{
		StartTime: 900, EndTime: 1100, Page: 2, PageSize: 1,
		SortBy: UserUsageRankingSortQuota, SortOrder: UserUsageRankingOrderDesc,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, items, 1)
	assert.Equal(t, second.Id, items[0].UserID)
}

func TestGetUserUsageRankingUsesInt64(t *testing.T) {
	truncateTables(t)
	user := rankingUser(t, "large", "Large")
	large := math.MaxInt32
	rankingRow(t, QuotaData{UserID: user.Id, Username: user.Username, ModelName: "a", CreatedAt: 1000, TokenUsed: large, Quota: large, Count: large})
	rankingRow(t, QuotaData{UserID: user.Id, Username: user.Username, ModelName: "b", CreatedAt: 1000, TokenUsed: large, Quota: large, Count: large})

	items, _, err := GetUserUsageRanking(UserUsageRankingQuery{
		StartTime: 900, EndTime: 1100, Page: 1, PageSize: 20,
		SortBy: UserUsageRankingSortTokenUsed, SortOrder: UserUsageRankingOrderDesc,
	})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, int64(large)*2, items[0].TokenUsed)
	assert.Equal(t, int64(large)*2, items[0].Quota)
	assert.Equal(t, int64(large)*2, items[0].Count)
}

func TestGetUserUsageRankingIncludesEnabledUsersWithoutRangeUsage(t *testing.T) {
	truncateTables(t)
	used := rankingUser(t, "used", "Used")
	zero := rankingUser(t, "zero", "Zero")
	require.NoError(t, DB.Model(&used).Update("role", common.RoleRootUser).Error)
	require.NoError(t, DB.Model(&zero).Update("role", common.RoleAdminUser).Error)
	disabled := rankingUser(t, "disabled", "Disabled")
	require.NoError(t, DB.Model(&disabled).Update("status", common.UserStatusDisabled).Error)
	deleted := rankingUser(t, "deleted", "Deleted")

	rankingRow(t, QuotaData{UserID: used.Id, Username: used.Username, ModelName: "m", CreatedAt: 1000, TokenUsed: 100, Quota: 50, Count: 2})
	rankingRow(t, QuotaData{UserID: zero.Id, Username: zero.Username, ModelName: "m", CreatedAt: 500, TokenUsed: 200, Quota: 80, Count: 3})
	rankingRow(t, QuotaData{UserID: disabled.Id, Username: disabled.Username, ModelName: "m", CreatedAt: 1000, TokenUsed: 300, Quota: 90, Count: 4})
	rankingRow(t, QuotaData{UserID: deleted.Id, Username: deleted.Username, ModelName: "m", CreatedAt: 1000, TokenUsed: 400, Quota: 100, Count: 5})
	require.NoError(t, DB.Delete(&deleted).Error)

	items, total, err := GetUserUsageRanking(UserUsageRankingQuery{
		StartTime: 900, EndTime: 1100, Page: 1, PageSize: 20,
		SortBy: UserUsageRankingSortTokenUsed, SortOrder: UserUsageRankingOrderDesc,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, items, 2)
	assert.Equal(t, used.Id, items[0].UserID)
	assert.EqualValues(t, 100, items[0].TokenUsed)
	assert.Equal(t, zero.Id, items[1].UserID)
	assert.Equal(t, "zero", items[1].Username)
	assert.Equal(t, "Zero", items[1].DisplayName)
	assert.Zero(t, items[1].TokenUsed)
	assert.Zero(t, items[1].Quota)
	assert.Zero(t, items[1].Count)
}

func TestGetUserUsageRankingSortsZeroUsageUsersStable(t *testing.T) {
	truncateTables(t)
	firstZero := rankingUser(t, "first-zero", "First Zero")
	secondZero := rankingUser(t, "second-zero", "Second Zero")
	used := rankingUser(t, "used", "Used")
	rankingRow(t, QuotaData{UserID: used.Id, Username: used.Username, ModelName: "m", CreatedAt: 1000, TokenUsed: 10, Quota: 20, Count: 1})

	items, total, err := GetUserUsageRanking(UserUsageRankingQuery{
		StartTime: 900, EndTime: 1100, Page: 2, PageSize: 1,
		SortBy: UserUsageRankingSortQuota, SortOrder: UserUsageRankingOrderAsc,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, items, 1)
	assert.Equal(t, secondZero.Id, items[0].UserID)
	assert.Zero(t, items[0].Quota)
	assert.Less(t, firstZero.Id, secondZero.Id)
}
