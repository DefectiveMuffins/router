package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestedEffortReadsExplicitClientLevels(t *testing.T) {
	cases := []struct {
		name  string
		parse func([]byte) (*RequestEnvelope, error)
		body  string
		want  string
	}{
		{"chat reasoning_effort", ParseOpenAI, `{"model":"gpt-6-sol","messages":[],"reasoning_effort":"high"}`, "high"},
		{"responses reasoning.effort", ParseOpenAI, `{"model":"gpt-6-sol","messages":[],"reasoning":{"effort":"max"}}`, "max"},
		{"anthropic adaptive effort", ParseAnthropic, `{"model":"claude-opus-5-5","messages":[],"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, "xhigh"},
		{"minimal canonicalizes to low", ParseOpenAI, `{"model":"gpt-6-sol","messages":[],"reasoning_effort":"minimal"}`, "low"},
		{"thinking budget is not a level", ParseAnthropic, `{"model":"claude-opus-5-5","messages":[],"thinking":{"type":"enabled","budget_tokens":2048}}`, ""},
		{"auto is not a level", ParseOpenAI, `{"model":"gpt-6-sol","messages":[],"reasoning_effort":"auto"}`, ""},
		{"unknown level is dropped", ParseOpenAI, `{"model":"gpt-6-sol","messages":[],"reasoning_effort":"turbo"}`, ""},
		{"absent", ParseOpenAI, `{"model":"gpt-6-sol","messages":[]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := tc.parse([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.RequestedEffort())
		})
	}
}
