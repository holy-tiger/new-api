package openaicompat

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"
)

func TestShouldResponsesUseChatCompletionsPolicy_DeepSeekFallback(t *testing.T) {
	policy := model_setting.ResponsesToChatCompletionsPolicy{
		Enabled:       false,
		AllChannels:   false,
		ModelPatterns: []string{"^gpt-5.*$"},
	}

	if !ShouldResponsesUseChatCompletionsPolicy(policy, 12, constant.ChannelTypeDeepSeek, "gpt-5.4") {
		t.Fatal("expected DeepSeek channels to auto-enable responses-to-chat bridging")
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
