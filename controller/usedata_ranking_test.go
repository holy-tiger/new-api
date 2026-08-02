package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rankingContext(target string) *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return ctx
}

func TestParseUserUsageRankingQueryDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	query, err := parseUserUsageRankingQuery(rankingContext("/api/data/user-ranking?start_timestamp=100&end_timestamp=200"))
	require.NoError(t, err)
	assert.Equal(t, 1, query.Page)
	assert.Equal(t, 20, query.PageSize)
	assert.Equal(t, "token_used", string(query.SortBy))
	assert.Equal(t, "desc", string(query.SortOrder))
}

func TestParseUserUsageRankingQueryAcceptsSupportedValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	query, err := parseUserUsageRankingQuery(rankingContext("/api/data/user-ranking?start_timestamp=100&end_timestamp=200&page=2&page_size=100&sort_by=count&sort_order=asc"))
	require.NoError(t, err)
	assert.Equal(t, 2, query.Page)
	assert.Equal(t, 100, query.PageSize)
	assert.Equal(t, "count", string(query.SortBy))
	assert.Equal(t, "asc", string(query.SortOrder))
}

func TestParseUserUsageRankingQueryRejectsInvalidValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []string{
		"/api/data/user-ranking?end_timestamp=200",
		"/api/data/user-ranking?start_timestamp=100",
		"/api/data/user-ranking?start_timestamp=200&end_timestamp=100",
		"/api/data/user-ranking?start_timestamp=1&end_timestamp=31622402",
		"/api/data/user-ranking?start_timestamp=100&end_timestamp=200&page=0",
		"/api/data/user-ranking?start_timestamp=100&end_timestamp=200&page_size=10",
		"/api/data/user-ranking?start_timestamp=100&end_timestamp=200&sort_by=username",
		"/api/data/user-ranking?start_timestamp=100&end_timestamp=200&sort_order=sideways",
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			_, err := parseUserUsageRankingQuery(rankingContext(target))
			require.Error(t, err)
		})
	}
}
