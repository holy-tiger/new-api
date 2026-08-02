package model

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rankingUser(t *testing.T, username, displayName string) User {
	t.Helper()
	user := User{Username: username, DisplayName: displayName, Password: "test-password", AffCode: "aff-" + username}
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
