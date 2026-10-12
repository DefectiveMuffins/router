package router_test

import (
	"testing"

	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func TestLookup_UnknownModel(t *testing.T) {
	spec := router.Lookup("unknown-model-99")
	assert.False(t, spec.Supports(router.CapAdaptiveThinking))
	assert.False(t, spec.Supports(router.CapExtendedThinking))
	assert.False(t, spec.Supports(router.CapReasoning))
}

func TestLookup_DateSuffixNormalization(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantAdaptive  bool
		wantExtended  bool
		wantReasoning bool
	}{
		{"anthropic haiku dated", "claude-haiku-4-5-20251001", false, true, false},
		{"anthropic haiku 5.5 adaptive", "claude-haiku-5-5", true, false, false},
		{"anthropic opus dated", "claude-opus-4-7-20260301", true, false, false},
		{"openai dated", "gpt-4o-2024-08-06", false, false, false},
		{"openai luna pro registered", "gpt-5.6-luna-pro", false, false, true},
		{"openai sol pro registered", "gpt-5.6-sol-pro", false, false, true},
		{"openai gpt-6 astra registered", "gpt-6-astra", false, false, true},
		{"google flash registered", "gemini-2.5-flash", false, false, false},
		{"google pro registered", "gemini-2.5-pro", false, false, false},
		{"openrouter qwen registered", "qwen/qwen3-coder-next", false, false, false},
		{"unknown with date suffix", "mystery-model-20250101", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := router.Lookup(tc.model)
			assert.Equal(t, tc.wantAdaptive, spec.Supports(router.CapAdaptiveThinking))
			assert.Equal(t, tc.wantExtended, spec.Supports(router.CapExtendedThinking))
			assert.Equal(t, tc.wantReasoning, spec.Supports(router.CapReasoning))
		})
	}
}

func TestLookup_GPT6AstraReasoning(t *testing.T) {
	spec := router.Lookup("gpt-6-astra")
	assert.True(t, spec.Supports(router.CapReasoning))
	assert.True(t, spec.Supports(router.CapXhighEffort))
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, spec.Reasoning().Levels)
	assert.True(t, spec.Reasoning().SupportsBudget)
	assert.True(t, spec.Reasoning().AlwaysOn)
}

func TestLookup_GPT61SolRequiresReasoning(t *testing.T) {
	spec := router.Lookup("gpt-6.1-sol")
	assert.True(t, spec.Supports(router.CapReasoning))
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, spec.Reasoning().Levels)
	assert.True(t, spec.Reasoning().AlwaysOn)
}

func TestOpenCodeSpecs(t *testing.T) {
	cases := []struct {
		model  string
		levels []string
		caps   []router.ModelCapability
	}{
		{"opencode/grok-4.7", []string{"low", "medium", "high", "xhigh"}, []router.ModelCapability{router.CapReasoning, router.CapXhighEffort}},
		{"opencode/gpt-6-luna", []string{"low", "medium", "high", "xhigh", "max"}, []router.ModelCapability{router.CapReasoning, router.CapXhighEffort}},
		{"opencode/claude-haiku-5-5", []string{"low", "medium", "high", "max", "xhigh"}, []router.ModelCapability{router.CapAdaptiveThinking, router.CapXhighEffort, router.CapAutoToolChoiceOnly}},
		{"opencode/glm-5.3", []string{"low", "high", "max"}, []router.ModelCapability{router.CapChatReasoningEffort}},
		{"opencode/glm-5.3-flash", []string{"low", "high", "max"}, []router.ModelCapability{router.CapChatReasoningEffort}},
		{"opencode/kimi-k3", []string{"max"}, []router.ModelCapability{router.CapChatReasoningEffort}},
		{"opencode/step-5-preview-free", []string{"low", "medium", "high"}, []router.ModelCapability{router.CapChatReasoningEffort}},
		{"opencode/deepseek-v4.1-flash", []string{"low", "high", "max"}, []router.ModelCapability{router.CapChatReasoningEffort}},
		{"opencode/qwen3.8-max", nil, []router.ModelCapability{router.CapExtendedThinking}},
		{"opencode/qwen3.8-flash", nil, []router.ModelCapability{router.CapExtendedThinking}},
	}
	for _, tc := range cases {
		spec := router.Lookup(tc.model)
		assert.Equal(t, tc.levels, spec.Reasoning().Levels, tc.model)
		for _, c := range tc.caps {
			assert.Truef(t, spec.Supports(c), "%s should support %s", tc.model, c)
		}
		assert.False(t, spec.Supports(router.CapExtendedContext), tc.model)
		assert.False(t, spec.Supports(router.CapReasoning) && spec.Supports(router.CapChatReasoningEffort), tc.model)
	}
	for _, model := range []string{"opencode/longcat-2.5-preview-free", "opencode/mimo-v2.6-pro", "opencode/mimo-v2.6-flash"} {
		spec := router.Lookup(model)
		assert.Empty(t, spec.Reasoning().Levels, model)
		assert.False(t, spec.Supports(router.CapReasoning), model)
		assert.False(t, spec.Supports(router.CapChatReasoningEffort), model)
	}
	assert.NoError(t, router.ValidateCatalogReasoningCapabilities())
}
