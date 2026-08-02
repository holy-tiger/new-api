package controller

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const maxUserUsageRankingRangeSeconds int64 = 366 * 24 * 60 * 60

func parseRankingTimestamp(c *gin.Context, name string) (int64, error) {
	raw := c.Query(name)
	if raw == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Unix timestamp", name)
	}
	return value, nil
}

func parseUserUsageRankingQuery(c *gin.Context) (model.UserUsageRankingQuery, error) {
	start, err := parseRankingTimestamp(c, "start_timestamp")
	if err != nil {
		return model.UserUsageRankingQuery{}, err
	}
	end, err := parseRankingTimestamp(c, "end_timestamp")
	if err != nil {
		return model.UserUsageRankingQuery{}, err
	}
	if end < start {
		return model.UserUsageRankingQuery{}, fmt.Errorf("end_timestamp must not be earlier than start_timestamp")
	}
	if end-start > maxUserUsageRankingRangeSeconds {
		return model.UserUsageRankingQuery{}, fmt.Errorf("time range must not exceed 366 days")
	}

	page := 1
	if raw := c.Query("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 {
			return model.UserUsageRankingQuery{}, fmt.Errorf("page must be a positive integer")
		}
	}

	pageSize := 20
	if raw := c.Query("page_size"); raw != "" {
		pageSize, err = strconv.Atoi(raw)
		if err != nil || (pageSize != 20 && pageSize != 50 && pageSize != 100) {
			return model.UserUsageRankingQuery{}, fmt.Errorf("page_size must be one of 20, 50, or 100")
		}
	}

	sortBy := model.UserUsageRankingSort(c.DefaultQuery("sort_by", "token_used"))
	if sortBy != model.UserUsageRankingSortTokenUsed &&
		sortBy != model.UserUsageRankingSortQuota &&
		sortBy != model.UserUsageRankingSortCount {
		return model.UserUsageRankingQuery{}, fmt.Errorf("invalid sort_by")
	}
	sortOrder := model.UserUsageRankingOrder(c.DefaultQuery("sort_order", "desc"))
	if sortOrder != model.UserUsageRankingOrderAsc && sortOrder != model.UserUsageRankingOrderDesc {
		return model.UserUsageRankingQuery{}, fmt.Errorf("invalid sort_order")
	}

	return model.UserUsageRankingQuery{
		StartTime: start,
		EndTime:   end,
		Page:      page,
		PageSize:  pageSize,
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}, nil
}

func GetAllQuotaDates(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	dates, err := model.GetAllQuotaDates(startTimestamp, endTimestamp, username)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    dates,
	})
	return
}

func GetQuotaDatesByUser(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	dates, err := model.GetQuotaDataGroupByUser(startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    dates,
	})
}

func GetUserUsageRanking(c *gin.Context) {
	query, err := parseUserUsageRankingQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	items, total, err := model.GetUserUsageRanking(query)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"items":     items,
			"total":     total,
			"page":      query.Page,
			"page_size": query.PageSize,
		},
	})
}

func GetUserQuotaDates(c *gin.Context) {
	userId := c.GetInt("id")
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	// 判断时间跨度是否超过 1 个月
	if endTimestamp-startTimestamp > 2592000 {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "时间跨度不能超过 1 个月",
		})
		return
	}
	dates, err := model.GetQuotaDataByUserId(userId, startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    dates,
	})
	return
}
