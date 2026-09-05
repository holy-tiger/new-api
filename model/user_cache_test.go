package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUserCachePreservesRoleInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	base := (&User{Id: 7, Username: "root", Role: common.RoleRootUser}).ToBaseUser()
	base.WriteContext(ctx)

	role, exists := ctx.Get(string(constant.ContextKeyUserRole))
	require.True(t, exists)
	require.Equal(t, common.RoleRootUser, role)
}
