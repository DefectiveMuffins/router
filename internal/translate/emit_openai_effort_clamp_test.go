package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

func openCodeChatEmit(t *testing.T, source string, body []byte, id catalog.ModelID, forceEffort, forceReasoningEffort string) map[string]any {
	t.Helper()
	var env *translate.RequestEnvelope
	var err error
	if source == "anthropic" {
		env, err = translate.ParseAnthropic(body)
	} else {
		env, err = translate.ParseOpenAI(body)
	}
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:          id.String(),
		TargetProvider:       providers.ProviderOpenCode,
		Capabilities:         router.Lookup(id.String()),
		ForceEffort:          forceEffort,
		ForceReasoningEffort: forceReasoningEffort,
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	return out
}

func TestChatEmit_ClampsForcedEffortOntoTargetMenu(t *testing.T) {
	sources := map[string][]byte{
		"anthropic": []byte(`{"model":"claude-x","max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`),
		"openai":    []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`),
	}
	cases := []struct {
		name                 string
		id                   catalog.ModelID
		forceEffort          string
		forceReasoningEffort string
		want                 string
	}{
		{"medium falls to the lower neighbour", catalog.ModelIDOpenCodeGLM53, "medium", "", "low"},
		{"xhigh tops out at max", catalog.ModelIDOpenCodeGLM53, "xhigh", "", "max"},
		{"user reasoning effort is clamped too", catalog.ModelIDOpenCodeGLM53, "", "medium", "low"},
		{"single-level menu", catalog.ModelIDOpenCodeKimiK3, "low", "", "max"},
		{"max above a high ceiling", catalog.ModelIDOpenCodeStep5PreviewFree, "max", "", "high"},
		{"no menu writes nothing", catalog.ModelIDOpenCodeMiMoV26Pro, "high", "", ""},
	}
	for source, body := range sources {
		for _, tc := range cases {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				out := openCodeChatEmit(t, source, body, tc.id, tc.forceEffort, tc.forceReasoningEffort)
				if tc.want == "" {
					assert.NotContains(t, out, "reasoning_effort")
					return
				}
				assert.Equal(t, tc.want, out["reasoning_effort"])
			})
		}
	}
}

func TestChatEmit_ClampsCallerReasoningEffortOntoTargetMenu(t *testing.T) {
	cases := []struct {
		name   string
		id     catalog.ModelID
		caller string
		force  string
		want   string
	}{
		{"on-menu level is kept", catalog.ModelIDOpenCodeGLM53, "high", "", "high"},
		{"off-menu level is clamped", catalog.ModelIDOpenCodeGLM53, "medium", "", "low"},
		{"alias is canonicalized first", catalog.ModelIDOpenCodeGLM53, "minimal", "", "low"},
		{"single-level menu", catalog.ModelIDOpenCodeKimiK3, "high", "", "max"},
		{"above the menu's ceiling", catalog.ModelIDOpenCodeStep5PreviewFree, "xhigh", "", "high"},
		{"a routed level outranks the caller's", catalog.ModelIDOpenCodeGLM53, "low", "max", "max"},
		{"off the ladder is dropped, not raised", catalog.ModelIDOpenCodeGLM53, "none", "", ""},
		{"no menu writes nothing", catalog.ModelIDOpenCodeMiMoV26Pro, "high", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"` + tc.caller + `"}`)
			out := openCodeChatEmit(t, "openai", body, tc.id, tc.force, "")
			if tc.want == "" {
				assert.NotContains(t, out, "reasoning_effort")
				return
			}
			assert.Equal(t, tc.want, out["reasoning_effort"])
		})
	}
}

func TestChatEmit_ChatReasoningEffortKeepsPlainChatParameters(t *testing.T) {
	body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],"max_tokens":4000,"stop":["x"]}`)
	out := openCodeChatEmit(t, "openai", body, catalog.ModelIDOpenCodeGLM53, "high", "")
	assert.Equal(t, "high", out["reasoning_effort"])
	assert.Equal(t, float64(4000), out["max_tokens"])
	assert.NotContains(t, out, "max_completion_tokens")
	assert.Equal(t, []any{"x"}, out["stop"])
}
