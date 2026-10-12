package providers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func TestWireSurfaceFamily(t *testing.T) {
	cases := []struct {
		surface  providers.WireSurface
		provider string
		family   providers.TranslationFamily
		name     string
	}{
		{providers.SurfaceChatCompletions, providers.ProviderAnthropic, providers.FamilyOpenAICompat, "chat_completions"},
		{providers.SurfaceResponses, providers.ProviderAnthropic, providers.FamilyOpenAICompat, "responses"},
		{providers.SurfaceMessages, providers.ProviderOpenAI, providers.FamilyAnthropic, "messages"},
		{providers.SurfaceProviderDefault, providers.ProviderAnthropic, providers.FamilyAnthropic, "provider_default"},
		{providers.SurfaceProviderDefault, providers.ProviderOpenCode, providers.FamilyOpenAICompat, "provider_default"},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.family, tc.surface.Family(tc.provider), "family of %s for %s", tc.surface, tc.provider)
		assert.Equal(t, tc.name, tc.surface.String())
	}
}
