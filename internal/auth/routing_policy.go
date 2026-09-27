package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// RoutingPolicyMode is the installation-level serving policy.
type RoutingPolicyMode string

const (
	RoutingPolicyInherit     RoutingPolicyMode = "inherit"
	RoutingPolicyPassthrough RoutingPolicyMode = "passthrough"
	RoutingPolicyAssigned    RoutingPolicyMode = "assigned"
)

// ErrRoutingPolicyUnavailable prevents uncertain policy state from becoming automatic routing.
var ErrRoutingPolicyUnavailable = errors.New("routing policy unavailable")

// RoutingPolicy is an immutable, revisioned snapshot for one installation.
type RoutingPolicy struct {
	Mode     RoutingPolicyMode
	Revision int64
}

// RoutingPolicyRepository reads generic assignments without knowing their control-plane origin.
type RoutingPolicyRepository interface {
	GetPolicy(context.Context, string) (RoutingPolicy, error)
	HasAssignment(context.Context, string, string, int64) (bool, error)
}

type routingPolicyContextKey struct{}
type routingPolicyGenerationContextKey struct{}
type routingDecisionContextKey struct{}

// RoutingPolicyFrom returns the policy read at admission, including identity-less requests.
func RoutingPolicyFrom(ctx context.Context) RoutingPolicy {
	policy, _ := ctx.Value(routingPolicyContextKey{}).(RoutingPolicy)
	return policy
}

// RoutingPassthroughFrom reports whether this request must keep its requested model.
func RoutingPassthroughFrom(ctx context.Context) bool {
	policy := RoutingPolicyFrom(ctx)
	if policy.Mode == RoutingPolicyPassthrough {
		return true
	}
	if policy.Mode != RoutingPolicyAssigned {
		return false
	}
	routerOn, _ := ctx.Value(routingDecisionContextKey{}).(bool)
	return !routerOn
}

type policyCacheEntry struct {
	policy     RoutingPolicy
	expiresAt  time.Time
	generation uint64
}

type assignmentCacheEntry struct {
	routerOn   bool
	expiresAt  time.Time
	generation uint64
}

// RoutingPolicyCache is bounded by installation invalidations and a short TTL.
// The revision in the assignment key prevents an old user decision from crossing a policy update.
type RoutingPolicyCache struct {
	mu          sync.Mutex
	policies    *expirable.LRU[string, policyCacheEntry]
	assignments *expirable.LRU[string, assignmentCacheEntry]
	generations map[string]uint64
	ttl         time.Duration
}

// NewRoutingPolicyCache constructs the installation-scoped cache.
func NewRoutingPolicyCache(ttl time.Duration) *RoutingPolicyCache {
	return &RoutingPolicyCache{
		policies:    expirable.NewLRU[string, policyCacheEntry](10000, nil, ttl),
		assignments: expirable.NewLRU[string, assignmentCacheEntry](50000, nil, ttl),
		generations: make(map[string]uint64), ttl: ttl,
	}
}

func (cache *RoutingPolicyCache) generation(installationID string) uint64 {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.generations[installationID]
}

func (cache *RoutingPolicyCache) policy(installationID string, generation uint64) (RoutingPolicy, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.policies.Get(installationID)
	return entry.policy, found && entry.generation == generation && cache.generations[installationID] == generation && time.Now().Before(entry.expiresAt)
}

func (cache *RoutingPolicyCache) setPolicy(installationID string, generation uint64, policy RoutingPolicy) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.generations[installationID] != generation {
		return false
	}
	cache.policies.Add(installationID, policyCacheEntry{policy: policy, generation: generation, expiresAt: time.Now().Add(cache.ttl)})
	return true
}

func assignmentKey(installationID, routerUserID string, revision int64) string {
	return fmt.Sprintf("%s\x00%s\x00%d", installationID, routerUserID, revision)
}

func (cache *RoutingPolicyCache) assignment(installationID, routerUserID string, revision int64, generation uint64) (bool, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.assignments.Get(assignmentKey(installationID, routerUserID, revision))
	return entry.routerOn, found && entry.generation == generation && cache.generations[installationID] == generation && time.Now().Before(entry.expiresAt)
}

func (cache *RoutingPolicyCache) setAssignment(installationID, routerUserID string, revision int64, generation uint64, routerOn bool) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.generations[installationID] != generation {
		return false
	}
	cache.assignments.Add(assignmentKey(installationID, routerUserID, revision), assignmentCacheEntry{routerOn: routerOn, generation: generation, expiresAt: time.Now().Add(cache.ttl)})
	return true
}

// InvalidateInstallation prevents in-flight stale reads from populating a newer generation.
func (cache *RoutingPolicyCache) InvalidateInstallation(installationID string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.generations[installationID]++
	cache.policies.Remove(installationID)
}
