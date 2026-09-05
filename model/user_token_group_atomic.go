package model

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// UpdateUserAndClearTokenGroups updates a user and optionally clears all of
// that user's Token group overrides atomically.
func UpdateUserAndClearTokenGroups(user *User, updatePassword bool, clearTokenGroups bool) error {
	var err error
	if updatePassword {
		user.Password, err = common.Password2Hash(user.Password)
		if err != nil {
			return err
		}
	}

	newUser := *user
	updates := map[string]interface{}{
		"username":     newUser.Username,
		"display_name": newUser.DisplayName,
		"group":        newUser.Group,
		"remark":       newUser.Remark,
	}
	if updatePassword {
		updates["password"] = newUser.Password
	}

	tx := DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if err = tx.Model(&User{}).Where("id = ?", user.Id).Updates(updates).Error; err != nil {
		tx.Rollback()
		return err
	}
	if clearTokenGroups {
		if err = tx.Model(&Token{}).Where("user_id = ?", user.Id).Update("group", "").Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	if err = tx.Commit().Error; err != nil {
		return err
	}
	if err = DB.Where("id = ?", user.Id).First(user).Error; err != nil {
		return err
	}
	if err = updateUserCache(*user); err != nil {
		return err
	}
	if clearTokenGroups {
		if err = InvalidateUserTokensCache(user.Id); err != nil {
			common.SysLog(fmt.Sprintf("failed to invalidate tokens cache for user %d: %s", user.Id, err.Error()))
		}
	}
	return nil
}
