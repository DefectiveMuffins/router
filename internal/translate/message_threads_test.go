package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMessageThreadType(t *testing.T) {
	assert.Equal(t, translate.MessageThreadCreate, translate.MessageThreadType([]byte(`{"thread":{"type":"create"}}`)))
	assert.Equal(t, translate.MessageThreadContinue, translate.MessageThreadType([]byte(`{"thread":{"type":"continue","previous_message_id":"msg_1"}}`)))
	assert.Empty(t, translate.MessageThreadType([]byte(`{"messages":[]}`)))
}

func TestStripMessageThread(t *testing.T) {
	out, err := translate.StripMessageThread([]byte(`{"model":"m","messages":[],"thread":{"type":"create"}}`))
	require.NoError(t, err)
	assert.False(t, gjson.GetBytes(out, "thread").Exists())
	assert.Equal(t, "m", gjson.GetBytes(out, "model").String())
}

func TestStripMessageThreadsBeta(t *testing.T) {
	h := http.Header{}
	h.Add("anthropic-beta", "claude-code-20250219, message-threads-2026-08-12")
	h.Add("anthropic-beta", "fast-mode-2026-02-01")
	translate.StripMessageThreadsBeta(h)
	assert.Equal(t, []string{"claude-code-20250219,fast-mode-2026-02-01"}, h.Values("anthropic-beta"))

	only := http.Header{"Anthropic-Beta": {"message-threads-2026-08-12"}}
	translate.StripMessageThreadsBeta(only)
	assert.Empty(t, only.Values("anthropic-beta"))
}
