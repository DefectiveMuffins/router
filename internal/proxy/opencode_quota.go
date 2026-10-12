package proxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const (
	openCodeUsageLimitMarker       = "GoUsageLimitError"
	openCodeUsageLimitDefaultReset = time.Hour
	openCodeUsageLimitMaxReset     = 31 * 24 * time.Hour
)

// openCodeUsageLimitReset reports whether err is the plan's per-model usage
// limit and when that model may be offered again.
func openCodeUsageLimitReset(err error, now time.Time) (time.Time, bool) {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status != http.StatusTooManyRequests ||
		!bytes.Contains(buffered.Body, []byte(openCodeUsageLimitMarker)) {
		return time.Time{}, false
	}
	wait := openCodeUsageLimitDefaultReset
	if delay, ok := providers.RetryAfter(err, now); ok && delay > 0 {
		wait = min(delay, openCodeUsageLimitMaxReset)
	}
	return now.Add(wait), true
}

// recordOpenCodeUsageLimit records a refused model as a rejected scoped
// window on the plan's lane, which withholds it from automatic routing and
// tells policy sidecars until reset.
func (s *Service) recordOpenCodeUsageLimit(ctx context.Context, target inference.Target, err error) {
	if s.usageObserver == nil || target.Provider != providers.ProviderOpenCode {
		return
	}
	now := s.clockNow()
	resetAt, limited := openCodeUsageLimitReset(err, now)
	if !limited {
		return
	}
	scope := openCodeScope(target.CatalogID, target.UpstreamID)
	s.usageObserver.Record(usage.ProviderLaneKey(providers.ProviderOpenCode), usage.Snapshot{
		Scoped: map[string]usage.ScopedWindow{scope: {
			Window: usage.Window{UsedPercent: 1, WindowMinutes: int(resetAt.Sub(now)/time.Minute) + 1, ResetAt: resetAt},
			Status: usage.StatusRejected,
		}},
	})
	observability.FromContext(ctx).Info("Plan usage limit reached; model ruled out until reset",
		"provider", providers.ProviderOpenCode, "scope", scope, "reset_at", resetAt)
}

// openCodeScope is the plan's per-model limit key: the upstream model id.
func openCodeScope(catalogID, upstreamID string) string {
	return strings.TrimPrefix(catalog.UpstreamIDFor(catalogID, upstreamID), providers.ProviderOpenCode+"/")
}

// openCodeLimitedModels lists the catalog models served only by the plan
// whose usage limit is in force. They join the soft automatic exclusions, so
// the router stops choosing them until reset whatever strategy routes the
// turn, while an explicit /force-model still reaches the upstream.
func (s *Service) openCodeLimitedModels() map[string]struct{} {
	if s.usageObserver == nil {
		return nil
	}
	snap, observed := s.usageObserver.Snapshot(usage.ProviderLaneKey(providers.ProviderOpenCode))
	if !observed || len(snap.Scoped) == 0 {
		return nil
	}
	now := s.clockNow()
	var out map[string]struct{}
	for _, m := range catalog.Models {
		limited := false
		for _, b := range m.Providers {
			if b.Provider != providers.ProviderOpenCode {
				limited = false
				break
			}
			w, ok := snap.Scoped[openCodeScope(m.ID, b.UpstreamID)]
			limited = ok && w.Status == usage.StatusRejected && w.ResetAt.After(now)
		}
		if limited {
			if out == nil {
				out = make(map[string]struct{})
			}
			out[m.ID] = struct{}{}
		}
	}
	return out
}

// openCodeLaneHeadroom reports the plan lane's observed scoped limits for
// the policy, or nil when the lane is not enabled or nothing is in force.
func (s *Service) openCodeLaneHeadroom(enabled map[string]struct{}) []router.SubscriptionHeadroom {
	if s.usageObserver == nil {
		return nil
	}
	if enabled != nil {
		if _, ok := enabled[providers.ProviderOpenCode]; !ok {
			return nil
		}
	}
	snap, observed := s.usageObserver.Snapshot(usage.ProviderLaneKey(providers.ProviderOpenCode))
	if !observed {
		return nil
	}
	lane := headroomFromSnapshot(providers.ProviderOpenCode, snap, s.clockNow())
	if len(lane.Windows) == 0 {
		return nil
	}
	return []router.SubscriptionHeadroom{lane}
}
