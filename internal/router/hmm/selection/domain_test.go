package selection_test

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
)

func domainEvidenceForTest(roster *rosterdata.Roster) *selection.DomainEvidence {
	return &selection.DomainEvidence{Arms: map[string]selection.DomainArmEvidence{
		"vendor-a/quality": {GlobalWII: 90, WPI: 10, TerminalQuality: 0},
		"vendor-b/cheap":   {GlobalWII: 55, WPI: 0, TerminalQuality: 100},
	}}
}

func TestSparseDomainScoresPreserveBaselineForAllMasks(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(roster)
	groups := []selection.Group{{Label: "low"}}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	for mask := 0; mask < 32; mask++ {
		profile := selection.DomainProfile{}
		for bit, domain := range []selection.Domain{selection.DomainUI, selection.DomainLogic, selection.DomainData, selection.DomainInfra, selection.DomainDocs} {
			profile[domain] = mask&(1<<bit) != 0
		}
		pick, scores, _, order, ok := selection.SelectGroupsWithDomainPreferences(roster, groups, "", candidates, nil, nil, nil, nil, evidence, profile)
		require.True(t, ok)
		assert.ElementsMatch(t, roster.Clusters["low"].Arms, order["low"])
		beta := (0.15*boolFloat(profile[selection.DomainLogic]) + 0.25*boolFloat(profile[selection.DomainInfra])) / math.Max(1, float64(popcount(mask)))
		assert.InDelta(t, beta, selection.TerminalInfluence(profile), 1e-12)
		assert.InDelta(t, 30+0.4*beta*(0-90), scores["low"]["vendor-a/quality"], 1e-5)
		assert.InDelta(t, 25+0.4*beta*(100-55), scores["low"]["vendor-b/cheap"], 1e-5)
		if beta == 0 {
			assert.Equal(t, "vendor-a/quality", pick.Arm)
			assert.Equal(t, roster.Clusters["low"].Arms, order["low"])
		}
	}
	assert.Zero(t, selection.TerminalInfluence(nil))
	assert.Zero(t, selection.TerminalInfluence(selection.DomainProfile{selection.DomainInfra: true}))
	assert.Zero(t, selection.TerminalInfluence(selection.DomainProfile{
		selection.DomainUI: false, selection.DomainLogic: false, selection.DomainData: false,
		selection.DomainInfra: true, selection.Domain("unexpected"): false,
	}))
}

func TestSparseDomainRetainsPinVendorAndEligibility(t *testing.T) {
	roster := dynamicRoster()
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-a/quality"}}
	cluster.PreferredVendorsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessCodex: {"vendor-a"}}
	roster.Clusters["low"] = cluster
	profile := fullProfile(selection.DomainInfra)
	groups := []selection.Group{{Label: "low"}}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	pick, _, _, _, ok := selection.SelectGroupsWithDomainPreferences(roster, groups, "pi", candidates, nil, nil, nil, nil, domainEvidenceForTest(roster), profile)
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm)
	pick, _, _, _, ok = selection.SelectGroupsWithDomainPreferences(roster, groups, "codex", candidates, nil, nil, nil, nil, domainEvidenceForTest(roster), profile)
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm)
	pick, _, _, _, ok = selection.SelectGroupsWithDomainPreferences(roster, groups, "", candidateSet("vendor-b/cheap"), nil, nil, nil, nil, domainEvidenceForTest(roster), profile)
	require.True(t, ok)
	assert.Equal(t, "vendor-b/cheap", pick.Arm)
}

func TestSparseDomainZeroCorrectionKeepsNeutralOrder(t *testing.T) {
	roster := dynamicRoster()
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-b/cheap"}}
	roster.Clusters["low"] = cluster
	evidence := domainEvidenceForTest(roster)
	for arm, cell := range evidence.Arms {
		cell.TerminalQuality = cell.GlobalWII
		evidence.Arms[arm] = cell
	}
	pick, scores, _, orders, ok := selection.SelectGroupsWithDomainPreferences(
		roster, []selection.Group{{Label: "low"}}, "pi", candidateSet("vendor-a/quality", "vendor-b/cheap"),
		nil, nil, nil, nil, evidence, fullProfile(selection.DomainInfra),
	)
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm)
	assert.Equal(t, cluster.Arms, orders["low"])
	assert.Equal(t, float32(30), scores["low"]["vendor-a/quality"])
}

func TestSparseDomainKeepsUserQualityPreference(t *testing.T) {
	roster := dynamicRoster()
	profile := fullProfile(selection.DomainLogic)
	groups := []selection.Group{{Label: "low"}}
	priceHeavy := 0.0
	_, scores, _, _, ok := selection.SelectGroupsWithDomainPreferences(
		roster, groups, "", candidateSet("vendor-a/quality", "vendor-b/cheap"),
		&priceHeavy, nil, nil, nil, domainEvidenceForTest(roster), profile,
	)
	require.True(t, ok)
	alpha := roster.Ranking.AlphaMin["low"]
	assert.InDelta(t, alpha*90-(1-alpha)*10+alpha*0.15*(0-90), scores["low"]["vendor-a/quality"], 1e-5)
	assert.InDelta(t, alpha*55+alpha*0.15*(100-55), scores["low"]["vendor-b/cheap"], 1e-5)
}

func TestSparseEvidenceRejectsRecipeDriftAndMissingArm(t *testing.T) {
	roster := dynamicRoster()
	roster.SHA256 = strings.Repeat("a", 64)
	roster.Ranking.WIIScoreVersion = "authored-wii"
	roster.Ranking.WIINormalizationSHA256 = strings.Repeat("b", 64)
	roster.Ranking.WPIScoreVersion = "authored-wpi"
	roster.Ranking.WPINormalizationSHA256 = strings.Repeat("c", 64)
	encode := func(arms map[string]selection.DomainArmEvidence, logicWeight float64) []byte {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"schema_version": "domain_wmi_evidence_v2", "recipe_version": "domain_wmi_terminal_sparse_v1",
			"source_snapshot_sha256": strings.Repeat("d", 64), "source_ingest_date": "2026-09-28", "roster_sha256": roster.SHA256,
			"wii_score_version": "authored-wii", "wii_normalization_sha256": roster.Ranking.WIINormalizationSHA256,
			"wpi_score_version": "authored-wpi", "wpi_normalization_sha256": roster.Ranking.WPINormalizationSHA256,
			"recipes": map[string]any{
				"ui":    map[string]any{"influence": 0, "weights": map[string]float64{}},
				"logic": map[string]any{"influence": logicWeight, "weights": map[string]float64{"terminalbench_v2_1": 1}},
				"data":  map[string]any{"influence": 0, "weights": map[string]float64{}},
				"infra": map[string]any{"influence": 0.25, "weights": map[string]float64{"terminalbench_v2_1": 1}},
				"docs":  map[string]any{"influence": 0, "weights": map[string]float64{}},
			}, "arms": arms,
		})
		require.NoError(t, err)
		return payload
	}
	for _, test := range []struct {
		name   string
		arms   map[string]selection.DomainArmEvidence
		weight float64
		valid  bool
	}{
		{"complete", domainEvidenceForTest(roster).Arms, 0.15, true},
		{"changed weight", domainEvidenceForTest(roster).Arms, 0.9, false},
		{"missing effort arm", map[string]selection.DomainArmEvidence{"vendor-a/quality": {GlobalWII: 90, WPI: 10}}, 0.15, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := selection.ParseDomainEvidence(encode(test.arms, test.weight), roster)
			if test.valid {
				require.NoError(t, err)
				assert.Equal(t, 100.0, parsed.Arms["vendor-b/cheap"].TerminalQuality)
			} else {
				require.Error(t, err, fmt.Sprintf("%s should fail closed", test.name))
			}
		})
	}
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func fullProfile(active selection.Domain) selection.DomainProfile {
	return selection.DomainProfile{
		selection.DomainUI:    active == selection.DomainUI,
		selection.DomainLogic: active == selection.DomainLogic,
		selection.DomainData:  active == selection.DomainData,
		selection.DomainInfra: active == selection.DomainInfra,
		selection.DomainDocs:  active == selection.DomainDocs,
	}
}

func popcount(mask int) int {
	count := 0
	for mask != 0 {
		count += mask & 1
		mask >>= 1
	}
	return count
}
