package catalog_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

var openCodeRows = []struct {
	id      catalog.ModelID
	surface providers.WireSurface
	in, out float64
}{
	{catalog.ModelIDOpenCodeGrok47, providers.SurfaceResponses, 2.00, 6.00},
	{catalog.ModelIDOpenCodeGPT6Luna, providers.SurfaceResponses, 0.10, 0.50},
	{catalog.ModelIDOpenCodeClaudeHaiku55, providers.SurfaceMessages, 0.10, 0.50},
	{catalog.ModelIDOpenCodeGLM53, providers.SurfaceChatCompletions, 1.40, 4.40},
	{catalog.ModelIDOpenCodeGLM53Flash, providers.SurfaceChatCompletions, 0.15, 0.50},
	{catalog.ModelIDOpenCodeKimiK3, providers.SurfaceChatCompletions, 3.00, 15.00},
	{catalog.ModelIDOpenCodeStep5PreviewFree, providers.SurfaceChatCompletions, 0, 0},
	{catalog.ModelIDOpenCodeLongCat25PreviewFree, providers.SurfaceChatCompletions, 0, 0},
	{catalog.ModelIDOpenCodeMiMoV26Pro, providers.SurfaceChatCompletions, 0.435, 0.87},
	{catalog.ModelIDOpenCodeMiMoV26Flash, providers.SurfaceChatCompletions, 0.14, 0.28},
	{catalog.ModelIDOpenCodeQwen38Max, providers.SurfaceMessages, 2.00, 6.00},
	{catalog.ModelIDOpenCodeQwen38Flash, providers.SurfaceMessages, 0.15, 0.47},
	{catalog.ModelIDOpenCodeDeepSeekV41Flash, providers.SurfaceChatCompletions, 0.15, 0.60},
}

func TestCatalog_OpenCodeRows(t *testing.T) {
	require.Len(t, openCodeRows, 13)
	for _, row := range openCodeRows {
		m, ok := catalog.ByID(row.id.String())
		require.Truef(t, ok, "%s missing from the catalog", row.id)
		require.Lenf(t, m.Providers, 1, "%s must have exactly one binding", row.id)
		b := m.Providers[0]
		assert.Equal(t, providers.ProviderOpenCode, b.Provider, row.id)
		assert.Equal(t, strings.TrimPrefix(row.id.String(), providers.ProviderOpenCode+"/"), b.UpstreamID, row.id)
		assert.Equal(t, row.surface, b.Surface, row.id)
		assert.InDelta(t, row.in, b.Price.InputUSDPer1M, 1e-9, row.id)
		assert.InDelta(t, row.out, b.Price.OutputUSDPer1M, 1e-9, row.id)
		assert.Nil(t, b.Price.LongContext, row.id)
		assert.Zero(t, b.FastPrice, row.id)
		assert.NotEqual(t, catalog.TierUnknown, m.Tier, row.id)
	}
}

func TestCatalog_FreeOnlyOnOpenCodePreviews(t *testing.T) {
	free := map[string]struct{}{}
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Free {
				free[m.ID] = struct{}{}
			}
		}
	}
	assert.Equal(t, map[string]struct{}{
		catalog.ModelIDOpenCodeStep5PreviewFree.String():     {},
		catalog.ModelIDOpenCodeLongCat25PreviewFree.String(): {},
	}, free)
}

func TestSurfaceFor(t *testing.T) {
	cases := []struct {
		provider, model string
		surface         providers.WireSurface
		family          providers.TranslationFamily
	}{
		{providers.ProviderOpenCode, catalog.ModelIDOpenCodeClaudeHaiku55.String(), providers.SurfaceMessages, providers.FamilyAnthropic},
		{providers.ProviderOpenCode, catalog.ModelIDOpenCodeGrok47.String(), providers.SurfaceResponses, providers.FamilyOpenAICompat},
		{providers.ProviderOpenCode, catalog.ModelIDOpenCodeGLM53.String(), providers.SurfaceChatCompletions, providers.FamilyOpenAICompat},
		{providers.ProviderFireworks, "z-ai/glm-5.3", providers.SurfaceProviderDefault, providers.FamilyOpenAICompat},
		{providers.ProviderOpenCode, "no-such-model", providers.SurfaceProviderDefault, providers.FamilyOpenAICompat},
		{providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), providers.SurfaceProviderDefault, providers.FamilyAnthropic},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.surface, catalog.SurfaceFor(tc.provider, tc.model), "surface for (%s, %s)", tc.provider, tc.model)
		assert.Equalf(t, tc.family, catalog.TranslationFamilyFor(tc.provider, tc.model), "family for (%s, %s)", tc.provider, tc.model)
	}
}

func TestRoutingTargetSet_IncludesOpenCodeWhenEnabled(t *testing.T) {
	targets := catalog.RoutingTargetSet(map[string]struct{}{providers.ProviderOpenCode: {}})
	for _, row := range openCodeRows {
		assert.Containsf(t, targets, row.id.String(), "%s is not a routing target with opencode enabled", row.id)
	}
	assert.Len(t, targets, len(openCodeRows))
}
