package policyclient

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestRouteRequestCarriesSubscriptionHeadroom(t *testing.T) {
	reset := time.Date(2026, 10, 2, 22, 0, 0, 0, time.FixedZone("EDT", -4*3600))
	observed := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	body, err := marshalRouteRequest(policy.Query{
		SchemaVersion: policy.SchemaVersionV1,
		Strategy:      "hybrid",
		SubscriptionHeadroom: []router.SubscriptionHeadroom{{
			Provider: "anthropic",
			Windows: []router.QuotaWindow{
				{Name: "primary", UsedFraction: 0.31, WindowMinutes: 300, ResetAt: reset},
				{Name: "secondary", UsedFraction: 0.62},
			},
			ObservedAt: observed,
		}},
	})
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))
	assert.JSONEq(t, `[{
		"provider": "anthropic",
		"windows": [
			{"name": "primary", "used_fraction": 0.31, "window_minutes": 300, "reset_at": "2026-10-03T02:00:00Z"},
			{"name": "secondary", "used_fraction": 0.62}
		],
		"overage_in_use": false,
		"exhausted": false,
		"observed_at": "2026-10-02T19:00:00Z"
	}]`, string(wire["subscription_headroom"]))
}

func TestRouteRequestCarriesScopedWindows(t *testing.T) {
	body, err := marshalRouteRequest(policy.Query{
		SchemaVersion: policy.SchemaVersionV1,
		Strategy:      "hybrid",
		SubscriptionHeadroom: []router.SubscriptionHeadroom{{
			Provider: "anthropic",
			Windows: []router.QuotaWindow{
				{Name: "secondary", UsedFraction: 0.84, WindowMinutes: 10080, Scope: "fable", Status: "allowed_warning"},
			},
		}},
	})
	require.NoError(t, err)
	var wire struct {
		Headroom []struct {
			Windows []map[string]any `json:"windows"`
		} `json:"subscription_headroom"`
	}
	require.NoError(t, json.Unmarshal(body, &wire))
	require.Len(t, wire.Headroom, 1)
	assert.Equal(t, "fable", wire.Headroom[0].Windows[0]["scope"])
	assert.Equal(t, "allowed_warning", wire.Headroom[0].Windows[0]["status"])
}

func TestRouteRequestOmitsHeadroomWhenUnobserved(t *testing.T) {
	body, err := marshalRouteRequest(policy.Query{SchemaVersion: policy.SchemaVersionV1, Strategy: "hybrid"})
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))
	assert.NotContains(t, wire, "subscription_headroom")
}

func TestClassifierV4RequestNeverCarriesHeadroom(t *testing.T) {
	body, err := marshalRouteRequest(policy.Query{
		SchemaVersion:        policy.SchemaVersionV4,
		Strategy:             router.StrategyHMM,
		SubscriptionHeadroom: []router.SubscriptionHeadroom{{Provider: "anthropic"}},
	})
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))
	assert.NotContains(t, wire, "subscription_headroom", "v4 is a fixture-locked classifier contract")
}

func TestRouteRequestCarriesRequestedEffortOnlyWhenSet(t *testing.T) {
	body, err := marshalRouteRequest(policy.Query{SchemaVersion: policy.SchemaVersionV1, Strategy: "hybrid", RequestedEffort: "max"})
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))
	assert.JSONEq(t, `"max"`, string(wire["requested_effort"]))

	body, err = marshalRouteRequest(policy.Query{SchemaVersion: policy.SchemaVersionV1, Strategy: "hybrid"})
	require.NoError(t, err)
	wire = nil
	require.NoError(t, json.Unmarshal(body, &wire))
	assert.NotContains(t, wire, "requested_effort")
}
