package usage_test

import (
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/proxy/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAnthropicUnified_FableWindow(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.29")
	h.Set("anthropic-ratelimit-unified-7d_oi-utilization", "0.84")
	h.Set("anthropic-ratelimit-unified-7d_oi-reset", "1791540000")
	h.Set("anthropic-ratelimit-unified-7d_oi-status", "allowed_warning")

	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	fable, ok := snap.Scoped[usage.ScopeFable]
	require.True(t, ok)
	assert.InDelta(t, 0.84, fable.UsedPercent, 1e-9)
	assert.Equal(t, time.Unix(1791540000, 0).UTC(), fable.ResetAt)
	assert.Equal(t, "allowed_warning", fable.Status)
	assert.InDelta(t, 0.29, snap.Secondary.UsedPercent, 1e-9, "the account-wide weekly window is unchanged")
}

func TestParseAnthropicUnified_OpusClaimRejected(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_opus")
	h.Set("anthropic-ratelimit-unified-status", "rejected")
	h.Set("anthropic-ratelimit-unified-reset", "1791540000")

	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok, "a claim-only Opus limit is still an observation")
	opus := snap.Scoped[usage.ScopeOpus]
	assert.Equal(t, "rejected", opus.Status)
	assert.InDelta(t, 1.0, opus.UsedPercent, 1e-9)
	assert.Equal(t, time.Unix(1791540000, 0).UTC(), opus.ResetAt)
	assert.False(t, snap.Exhausted(), "an Opus limit must not take the whole credential out of service")
	assert.InDelta(t, 1.0, snap.CostFactor(0.05, 2.0), 1e-9, "scoped limits never feed the account cost factor")
}

func TestParseAnthropicUnified_SonnetWarningHasUnknownUsage(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_sonnet")
	h.Set("anthropic-ratelimit-unified-status", "allowed_warning")

	snap, ok := usage.ParseAnthropicUnifiedHeaders(h)
	require.True(t, ok)
	sonnet := snap.Scoped[usage.ScopeSonnet]
	assert.Equal(t, "allowed_warning", sonnet.Status)
	assert.Zero(t, sonnet.UsedPercent)
}

func TestObserver_ScopedLimitSurvivesLaterClaims(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, clock)
	key := o.Key([]byte("tok"))

	opus := http.Header{}
	opus.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_opus")
	opus.Set("anthropic-ratelimit-unified-status", "rejected")
	opus.Set("anthropic-ratelimit-unified-reset", now.Add(48*time.Hour).Format(time.RFC3339))
	snap, _ := usage.ParseAnthropicUnifiedHeaders(opus)
	o.Record(key, snap)

	now = now.Add(time.Hour)
	later := http.Header{}
	later.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
	later.Set("anthropic-ratelimit-unified-5h-utilization", "0.2")
	snap, _ = usage.ParseAnthropicUnifiedHeaders(later)
	o.Record(key, snap)

	got, ok := o.Snapshot(key)
	require.True(t, ok)
	assert.Equal(t, "rejected", got.Scoped[usage.ScopeOpus].Status)

	now = now.Add(48 * time.Hour)
	o.Record(key, snap)
	got, ok = o.Snapshot(key)
	require.True(t, ok)
	assert.NotContains(t, got.Scoped, usage.ScopeOpus, "a limit whose reset passed has refilled")
}

func TestObserver_NearCapScopedWindowExtendsRetention(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, clock)
	key := o.Key([]byte("tok"))

	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-7d_oi-utilization", "0.95")
	h.Set("anthropic-ratelimit-unified-7d_oi-reset", now.Add(72*time.Hour).Format(time.RFC3339))
	snap, _ := usage.ParseAnthropicUnifiedHeaders(h)
	o.Record(key, snap)

	now = now.Add(24 * time.Hour)
	got, ok := o.Snapshot(key)
	require.True(t, ok, "a near-cap Fable reading must not age out before its reset")
	assert.InDelta(t, 0.95, got.Scoped[usage.ScopeFable].UsedPercent, 1e-9)
}
