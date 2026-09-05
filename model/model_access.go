package model

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var modelAccessCache struct {
	sync.RWMutex
	loaded bool
	models []Model
}

func InvalidateModelAccessCache() {
	modelAccessCache.Lock()
	modelAccessCache.loaded = false
	modelAccessCache.models = nil
	modelAccessCache.Unlock()
}

func getModelAccessModels() ([]Model, error) {
	modelAccessCache.RLock()
	if modelAccessCache.loaded {
		models := append([]Model(nil), modelAccessCache.models...)
		modelAccessCache.RUnlock()
		return models, nil
	}
	modelAccessCache.RUnlock()

	modelAccessCache.Lock()
	defer modelAccessCache.Unlock()
	if modelAccessCache.loaded {
		return append([]Model(nil), modelAccessCache.models...), nil
	}
	var models []Model
	if err := DB.Find(&models).Error; err != nil {
		return nil, err
	}
	for i := range models {
		models[i].UserWhitelist = NormalizeUserWhitelist(models[i].UserWhitelist)
	}
	modelAccessCache.models = models
	modelAccessCache.loaded = true
	return append([]Model(nil), models...), nil
}

func normalizedAccessModelName(modelName string) string {
	return strings.TrimSuffix(modelName, ratio_setting.CompactModelSuffix)
}

func modelMatchesAccessRule(meta Model, modelName string) bool {
	switch meta.NameRule {
	case NameRuleExact:
		return meta.ModelName == modelName
	case NameRulePrefix:
		return strings.HasPrefix(modelName, meta.ModelName)
	case NameRuleSuffix:
		return strings.HasSuffix(modelName, meta.ModelName)
	case NameRuleContains:
		return strings.Contains(modelName, meta.ModelName)
	default:
		return false
	}
}

func findModelAccessPolicy(models []Model, modelName string) *Model {
	for _, rule := range []int{NameRuleExact, NameRulePrefix, NameRuleSuffix, NameRuleContains} {
		for i := range models {
			if models[i].NameRule == rule && modelMatchesAccessRule(models[i], modelName) {
				return &models[i]
			}
		}
	}
	return nil
}

func IsModelAccessible(modelName string, userID int, role int) (bool, error) {
	if role == common.RoleRootUser {
		return true, nil
	}
	models, err := getModelAccessModels()
	if err != nil {
		return false, err
	}
	modelName = normalizedAccessModelName(modelName)
	policy := findModelAccessPolicy(models, modelName)
	if policy == nil || len(policy.UserWhitelist) == 0 {
		return true, nil
	}
	for _, allowedUserID := range policy.UserWhitelist {
		if allowedUserID == userID {
			return true, nil
		}
	}
	return false, nil
}

func FilterModelsForUser(modelNames []string, userID int, role int) ([]string, error) {
	if role == common.RoleRootUser || len(modelNames) == 0 {
		return append([]string(nil), modelNames...), nil
	}
	models, err := getModelAccessModels()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		policy := findModelAccessPolicy(models, normalizedAccessModelName(modelName))
		if policy == nil || len(policy.UserWhitelist) == 0 {
			result = append(result, modelName)
			continue
		}
		for _, allowedUserID := range policy.UserWhitelist {
			if allowedUserID == userID {
				result = append(result, modelName)
				break
			}
		}
	}
	return result, nil
}
