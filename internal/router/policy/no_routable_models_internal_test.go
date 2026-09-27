package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
)

func TestEmptyCandidateError_NamesTheGatewayCase(t *testing.T) {
	err := emptyCandidateError(router.Request{}, []Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionGatewayNotServed},
		{CatalogID: "gpt-5.5", Reason: ExclusionGatewayNotServed},
	})

	assert.ErrorIs(t, err, ErrGatewayServesNoDeployedModel)
	assert.ErrorIs(t, err, ErrNoRoutableModels)
}

func TestEmptyCandidateError_MixedReasonsStayGeneric(t *testing.T) {
	// One non-gateway drop means aliases are not the whole story, so pointing
	// the caller at them would send them to fix the wrong setting.
	err := emptyCandidateError(router.Request{}, []Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionGatewayNotServed},
		{CatalogID: "gpt-5.5", Reason: ExclusionRequested},
	})

	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrGatewayServesNoDeployedModel)
}

func TestEmptyCandidateError_NoDiagnostics(t *testing.T) {
	assert.ErrorIs(t, emptyCandidateError(router.Request{}, nil), ErrNoRoutableModels)
}

func TestEmptyCandidateError_SizeIsNotConfiguration(t *testing.T) {
	// A pool emptied partly by size answers with the client's native
	// prompt-too-long, the only error a harness compacts on.
	err := emptyCandidateError(router.Request{}, []Diagnostic{
		{CatalogID: "claude-opus-5", Reason: ExclusionContextWindow},
		{CatalogID: "gpt-5.5", Reason: ExclusionRequested},
	})
	assert.ErrorIs(t, err, ErrContextWindowExceeded)
	assert.NotErrorIs(t, err, ErrNoRoutableModels)

	// Everything else was excluded by the pre-filter's size estimate and the
	// re-admitted widest model still dropped out: still a size problem.
	err = emptyCandidateError(router.Request{OverflowAdmittedModels: map[string]struct{}{"gpt-6-luna": {}}}, []Diagnostic{
		{CatalogID: "gpt-6-luna", Reason: ExclusionUnmappedRoster},
		{CatalogID: "gpt-5.5", Reason: ExclusionRequested},
	})
	assert.ErrorIs(t, err, ErrContextWindowExceeded)

	err = emptyCandidateError(router.Request{OverflowAdmittedModels: map[string]struct{}{"gpt-6-luna": {}}}, []Diagnostic{
		{CatalogID: "gpt-6-luna", Reason: ExclusionGatewayNotServed},
	})
	assert.ErrorIs(t, err, ErrGatewayServesNoDeployedModel, "a gateway serving nothing is empty at any size")
}

func TestCandidateLogFields_GroupsExclusionsByReason(t *testing.T) {
	fields := candidateLogFields(ResolvedCandidates{
		Candidates: []Candidate{{CatalogID: "gpt-4.1-mini"}, {CatalogID: "gemini-2.5-flash"}},
		Diagnostics: []Diagnostic{
			{CatalogID: "claude-opus-5", Reason: ExclusionRequested},
			{CatalogID: "z-ai/glm-5", Reason: ExclusionContextWindow},
			{CatalogID: "gpt-5.6-terra", Reason: ExclusionRequested},
		},
	})

	assert.Equal(t, []any{
		"candidate_count", 2,
		"candidates", "gpt-4.1-mini,gemini-2.5-flash",
		"excluded_" + string(ExclusionContextWindow), "z-ai/glm-5",
		"excluded_" + string(ExclusionRequested), "claude-opus-5,gpt-5.6-terra",
	}, fields)
}
