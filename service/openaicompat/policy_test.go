package openaicompat

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"
)

func TestShouldResponsesUseChatCompletionsPolicy_DeepSeekUsesNativeWhenDisabled(t *testing.T) {
	policy := model_setting.ResponsesToChatCompletionsPolicy{
		Enabled:       false,
		AllChannels:   false,
		ModelPatterns: []string{"^deepseek-v4-.*$"},
	}

	if ShouldResponsesUseChatCompletionsPolicy(policy, 12, constant.ChannelTypeDeepSeek, "deepseek-v4-pro") {
		t.Fatal("expected DeepSeek to use native Responses when fallback policy is disabled")
	}
}

func TestShouldResponsesUseChatCompletionsPolicy_ExplicitDeepSeekPolicyHonorsModelPattern(t *testing.T) {
	policy := model_setting.ResponsesToChatCompletionsPolicy{
		Enabled:       true,
		AllChannels:   false,
		ChannelTypes:  []int{constant.ChannelTypeDeepSeek},
		ModelPatterns: []string{"^deepseek-v4-.*$"},
	}

	if !ShouldResponsesUseChatCompletionsPolicy(policy, 12, constant.ChannelTypeDeepSeek, "deepseek-v4-pro") {
		t.Fatal("expected explicit DeepSeek fallback policy to match deepseek-v4-pro")
	}
	if ShouldResponsesUseChatCompletionsPolicy(policy, 12, constant.ChannelTypeDeepSeek, "deepseek-chat") {
		t.Fatal("expected explicit DeepSeek fallback policy to reject a non-matching model")
	}
}

func TestShouldResponsesUseChatCompletionsPolicy_NonDeepSeekStillNeedsPolicy(t *testing.T) {
	policy := model_setting.ResponsesToChatCompletionsPolicy{
		Enabled:       false,
		AllChannels:   false,
		ModelPatterns: []string{"^gpt-5.*$"},
	}

	if ShouldResponsesUseChatCompletionsPolicy(policy, 13, constant.ChannelTypeOpenAI, "gpt-5.4") {
		t.Fatal("expected non-DeepSeek channels to remain disabled without explicit policy")
	}
}

func TestShouldResponsesUseChatCompletionsPolicy_ExplicitPolicyStillWorks(t *testing.T) {
	policy := model_setting.ResponsesToChatCompletionsPolicy{
		Enabled:       true,
		AllChannels:   false,
		ChannelTypes:  []int{constant.ChannelTypeOpenAI},
		ModelPatterns: []string{"^gpt-5.*$"},
	}

	if !ShouldResponsesUseChatCompletionsPolicy(policy, 13, constant.ChannelTypeOpenAI, "gpt-5.4") {
		t.Fatal("expected explicit policy to keep working for non-DeepSeek channels")
	}
}
