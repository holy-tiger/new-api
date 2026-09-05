package model

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var modelAccessCache struct {
	sync.RWMutex
	loaded   bool
	models   []Model
	mappings map[string][]string
}

func InvalidateModelAccessCache() {
	modelAccessCache.Lock()
	modelAccessCache.loaded = false
	modelAccessCache.models = nil
	modelAccessCache.mappings = nil
	modelAccessCache.Unlock()
}

func getModelAccessModels() ([]Model, map[string][]string, error) {
	modelAccessCache.RLock()
	if modelAccessCache.loaded {
		models := append([]Model(nil), modelAccessCache.models...)
		mappings := cloneModelMappings(modelAccessCache.mappings)
		modelAccessCache.RUnlock()
		return models, mappings, nil
	}
	modelAccessCache.RUnlock()

	modelAccessCache.Lock()
	defer modelAccessCache.Unlock()
	if modelAccessCache.loaded {
		return append([]Model(nil), modelAccessCache.models...), cloneModelMappings(modelAccessCache.mappings), nil
	}
	var models []Model
	if err := DB.Find(&models).Error; err != nil {
		return nil, nil, err
	}
	mappings, err := loadModelMappings()
	if err != nil {
		return nil, nil, err
	}
	for i := range models {
		models[i].UserWhitelist = NormalizeUserWhitelist(models[i].UserWhitelist)
	}
	modelAccessCache.models = models
	modelAccessCache.mappings = mappings
	modelAccessCache.loaded = true
	return append([]Model(nil), models...), cloneModelMappings(mappings), nil
}

func cloneModelMappings(mappings map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(mappings))
	for source, targets := range mappings {
		clone[source] = append([]string(nil), targets...)
	}
	return clone
}

func loadModelMappings() (map[string][]string, error) {
	var channels []struct {
		ModelMapping *string `gorm:"column:model_mapping"`
	}
	if err := DB.Table("channels").Select("model_mapping").Where("model_mapping IS NOT NULL AND model_mapping <> ''").Find(&channels).Error; err != nil {
		return nil, err
	}
	mappings := make(map[string][]string)
	for _, channel := range channels {
		if channel.ModelMapping == nil {
			continue
		}
		var mapping map[string]string
		if err := common.UnmarshalJsonStr(*channel.ModelMapping, &mapping); err != nil {
			return nil, err
		}
		for source, target := range mapping {
			if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
				continue
			}
			mappings[source] = append(mappings[source], target)
		}
	}
	return mappings, nil
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

func findModelAccessPolicyForName(models []Model, modelName string) *Model {
	normalizedName := normalizedAccessModelName(modelName)
	if policy := findModelAccessPolicy(models, normalizedName); policy != nil {
		return policy
	}
	if normalizedName != modelName {
		return findModelAccessPolicy(models, modelName)
	}
	return nil
}

func mappedModelNames(modelName string, mappings map[string][]string) []string {
	result := make([]string, 0)
	visited := map[string]bool{modelName: true}
	queue := []string{modelName}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, target := range mappings[current] {
			if visited[target] {
				continue
			}
			visited[target] = true
			result = append(result, target)
			queue = append(queue, target)
		}
	}
	return result
}

func isModelAccessible(models []Model, mappings map[string][]string, modelName string, userID int) bool {
	names := append([]string{modelName}, mappedModelNames(modelName, mappings)...)
	for _, candidate := range names {
		policy := findModelAccessPolicyForName(models, candidate)
		if policy == nil || len(policy.UserWhitelist) == 0 {
			continue
		}
		allowed := false
		for _, allowedUserID := range policy.UserWhitelist {
			if allowedUserID == userID {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func IsModelAccessible(modelName string, userID int, role int) (bool, error) {
	if role == common.RoleRootUser {
		return true, nil
	}
	models, mappings, err := getModelAccessModels()
	if err != nil {
		return false, err
	}
	return isModelAccessible(models, mappings, modelName, userID), nil
}

func FilterModelsForUser(modelNames []string, userID int, role int) ([]string, error) {
	if role == common.RoleRootUser || len(modelNames) == 0 {
		return append([]string(nil), modelNames...), nil
	}
	models, mappings, err := getModelAccessModels()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		if isModelAccessible(models, mappings, modelName, userID) {
			result = append(result, modelName)
		}
	}
	return result, nil
}
