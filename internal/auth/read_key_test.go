package auth_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scopedKeyRow(rawToken string, scope auth.APIKeyScope, installation *auth.Installation) fakeKeyRow {
	hash, prefix, suffix := auth.APITokenFingerprint(rawToken)
	return fakeKeyRow{
		apiKey: &auth.APIKey{
			ID: "key-" + rawToken, InstallationID: installation.ID,
			KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: scope,
		},
		installation: installation,
	}
}

func TestService_VerifyReadAPIKey(t *testing.T) {
	installation := &auth.Installation{ID: "inst-read", ExternalID: "org-read"}
	svc, _ := makeService(t,
		scopedKeyRow("rk_routing", auth.ScopeRouting, installation),
		scopedKeyRow("rk_legacy", "", installation),
		scopedKeyRow("ra_analytics", auth.ScopeAnalyticsRead, installation),
		scopedKeyRow("rk_mislabeled", auth.ScopeAnalyticsRead, installation),
		scopedKeyRow("ra_mislabeled", auth.ScopeRouting, installation),
	)

	for _, test := range []struct {
		name, token string
		want        error
	}{
		{"routing key", "rk_routing", nil},
		{"pre-scope routing key", "rk_legacy", nil},
		{"analytics key", "ra_analytics", nil},
		{"analytics scope behind routing prefix", "rk_mislabeled", auth.ErrWrongKeyScope},
		{"routing scope behind analytics prefix", "ra_mislabeled", auth.ErrWrongKeyScope},
		{"unknown or revoked", "rk_unknown", auth.ErrInvalidToken},
		{"foreign prefix", "sk-ant-upstream", auth.ErrInvalidPrefix},
		{"missing", "", auth.ErrInvalidPrefix},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotInstallation, gotKey, err := svc.VerifyReadAPIKey(context.Background(), test.token)
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, installation, gotInstallation)
			assert.Equal(t, "key-"+test.token, gotKey.ID)
		})
	}
}

// A routing record cached without its BYOK keys would strip them from the next
// inference request for the positive TTL.
func TestService_VerifyReadAPIKey_DoesNotCacheSlimRoutingRecord(t *testing.T) {
	installation := &auth.Installation{ID: "inst-read", ExternalID: "org-read"}
	routing := scopedKeyRow("rk_routing", auth.ScopeRouting, installation)
	analytics := scopedKeyRow("ra_analytics", auth.ScopeAnalyticsRead, installation)
	cache := auth.NewLRUAPIKeyCache(16, 16, time.Minute, time.Minute)
	svc, _ := makeServiceWithCacheAndCounter(t, cache, routing, analytics)

	_, _, err := svc.VerifyReadAPIKey(context.Background(), "rk_routing")
	require.NoError(t, err)
	_, _, err = svc.VerifyReadAPIKey(context.Background(), "ra_analytics")
	require.NoError(t, err)

	_, routingCached := cache.Get(routing.apiKey.KeyHash)
	assert.False(t, routingCached)
	_, analyticsCached := cache.Get(analytics.apiKey.KeyHash)
	assert.True(t, analyticsCached)
}

func TestGatewayReadCredentialAcceptsRoutingOrAnalyticsKeys(t *testing.T) {
	for _, test := range []struct {
		name, token     string
		scope           auth.APIKeyScope
		lookupErr, want error
	}{
		{"routing", "rk_test", auth.ScopeRouting, nil, nil},
		{"pre-scope routing", "rk_test", "", nil, nil},
		{"analytics", "ra_test", auth.ScopeAnalyticsRead, nil, nil},
		{"analytics scope behind routing prefix", "rk_test", auth.ScopeAnalyticsRead, nil, auth.ErrWrongKeyScope},
		{"routing scope behind analytics prefix", "ra_test", auth.ScopeRouting, nil, auth.ErrWrongKeyScope},
		{"foreign prefix", "sk-test", auth.ScopeRouting, nil, auth.ErrInvalidPrefix},
		{"revoked", "rk_test", auth.ScopeRouting, sql.ErrNoRows, auth.ErrInvalidToken},
		{"storage unavailable", "ra_test", auth.ScopeAnalyticsRead, context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := auth.RoutingCredentialVerifier{Keys: gatewayCredentialRepo{key: &auth.APIKey{Scope: test.scope}, failure: test.lookupErr}}
			err := verifier.VerifyReadCredential(context.Background(), test.token)
			if test.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.want)
			}
		})
	}
}
