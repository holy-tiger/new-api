package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestEditUserPreservesTokenGroupsWhenGroupUnchanged(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("failed to migrate user table: %v", err)
	}

	user := &model.User{Username: "unchanged", Password: "hashed", Group: "vip", AffCode: "unchanged-aff"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	token := &model.Token{UserId: user.Id, Name: "explicit-token", Key: "explicit-key", Group: "auto"}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	user.Group = "vip"
	if err := model.UpdateUserAndClearTokenGroups(user, false, false); err != nil {
		t.Fatalf("failed to update user: %v", err)
	}

	var stored model.Token
	if err := db.First(&stored, token.Id).Error; err != nil {
		t.Fatalf("failed to reload token: %v", err)
	}
	if stored.Group != "auto" {
		t.Fatalf("expected explicit token group to remain auto, got %q", stored.Group)
	}
}
