package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestEditUserAndClearTokenGroups(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("failed to migrate user table: %v", err)
	}

	targetUser := &model.User{Username: "target", Password: "hashed", Group: "vip", AffCode: "target-aff"}
	otherUser := &model.User{Username: "other", Password: "hashed", Group: "default", AffCode: "other-aff"}
	if err := db.Create(targetUser).Error; err != nil {
		t.Fatalf("failed to create target user: %v", err)
	}
	if err := db.Create(otherUser).Error; err != nil {
		t.Fatalf("failed to create other user: %v", err)
	}
	for _, token := range []*model.Token{
		{UserId: targetUser.Id, Name: "target-auto", Key: "target-key-1", Group: "auto"},
		{UserId: targetUser.Id, Name: "target-vip", Key: "target-key-2", Group: "vip"},
		{UserId: otherUser.Id, Name: "other-token", Key: "other-key", Group: "default"},
	} {
		if err := db.Create(token).Error; err != nil {
			t.Fatalf("failed to create token %s: %v", token.Name, err)
		}
	}

	targetUser.Group = "default"
	if err := model.UpdateUserAndClearTokenGroups(targetUser, false, true); err != nil {
		t.Fatalf("failed to update user and clear token groups: %v", err)
	}

	var targetTokens []model.Token
	if err := db.Where("user_id = ?", targetUser.Id).Order("id").Find(&targetTokens).Error; err != nil {
		t.Fatalf("failed to load target tokens: %v", err)
	}
	if len(targetTokens) != 2 || targetTokens[0].Group != "" || targetTokens[1].Group != "" {
		t.Fatalf("expected all target token groups to be empty, got %#v", targetTokens)
	}

	var otherToken model.Token
	if err := db.Where("user_id = ?", otherUser.Id).First(&otherToken).Error; err != nil {
		t.Fatalf("failed to load other user's token: %v", err)
	}
	if otherToken.Group != "default" {
		t.Fatalf("expected other user's token group to remain default, got %q", otherToken.Group)
	}
}
