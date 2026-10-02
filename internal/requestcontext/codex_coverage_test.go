package requestcontext_test

import (
	"testing"

	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
)

func TestCodexLunaSubscriptionCoverage(t *testing.T) {
	const lunaModel = "gpt-6-luna"
	assert.True(t, requestcontext.CodexSubscriptionCoversModel(lunaModel))
	assert.Contains(t, requestcontext.CodexCoveredModels(), lunaModel)
	for _, model := range []string{"gpt-5.4-nano", "gpt-future-model", "openai/" + lunaModel, lunaModel + ":high"} {
		assert.False(t, requestcontext.CodexSubscriptionCoversModel(model), "only covered canonical model IDs may receive OAuth")
	}
}
