package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

type routingPolicyFixture struct {
	policy             auth.RoutingPolicy
	assigned           bool
	policyErr          error
	userErr            error
	policyReads        int
	userReads          int
	beforePolicyReturn func()
	beforeUserReturn   func()
}

func (fixture *routingPolicyFixture) GetPolicy(context.Context, string) (auth.RoutingPolicy, error) {
	fixture.policyReads++
	policy := fixture.policy
	if fixture.beforePolicyReturn != nil {
		fixture.beforePolicyReturn()
	}
	return policy, fixture.policyErr
}

func (fixture *routingPolicyFixture) HasAssignment(context.Context, string, string, int64) (bool, error) {
	fixture.userReads++
	assigned := fixture.assigned
	if fixture.beforeUserReturn != nil {
		fixture.beforeUserReturn()
	}
	return assigned, fixture.userErr
}

func TestRoutingPolicyInvalidationDuringInFlightReads(t *testing.T) {
	cache := auth.NewRoutingPolicyCache(time.Minute)
	fixture := &routingPolicyFixture{policy: auth.RoutingPolicy{Mode: auth.RoutingPolicyAssigned, Revision: 1}, assigned: true}
	service := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithRoutingPolicies(fixture, cache)
	fixture.beforePolicyReturn = func() {
		fixture.beforePolicyReturn = nil
		fixture.policy = auth.RoutingPolicy{Mode: auth.RoutingPolicyAssigned, Revision: 2}
		cache.InvalidateInstallation("installation")
	}
	ctx, err := service.WithRoutingPolicy(context.Background(), "installation")
	require.NoError(t, err)
	assert.Equal(t, int64(2), auth.RoutingPolicyFrom(ctx).Revision)
	assert.Equal(t, 2, fixture.policyReads)

	fixture.beforeUserReturn = func() {
		fixture.beforeUserReturn = nil
		fixture.assigned = false
		cache.InvalidateInstallation("installation")
	}
	ctx, err = service.WithRoutingAssignment(ctx, "installation", "user")
	require.NoError(t, err)
	assert.True(t, auth.RoutingPassthroughFrom(ctx))
	assert.Equal(t, 2, fixture.userReads)
}

func TestRoutingPolicyDefaultsAndAssignments(t *testing.T) {
	for _, tc := range []struct {
		mode            auth.RoutingPolicyMode
		assigned        bool
		withoutIdentity bool
		wantPassthrough bool
	}{
		{mode: auth.RoutingPolicyInherit},
		{mode: auth.RoutingPolicyPassthrough, wantPassthrough: true},
		{mode: auth.RoutingPolicyAssigned, wantPassthrough: true},
		{mode: auth.RoutingPolicyAssigned, assigned: true},
		{mode: auth.RoutingPolicyAssigned, assigned: true, withoutIdentity: true, wantPassthrough: true},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			fixture := &routingPolicyFixture{policy: auth.RoutingPolicy{Mode: tc.mode, Revision: 1}, assigned: tc.assigned}
			service := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(fixture, nil)
			ctx, err := service.WithRoutingPolicy(context.Background(), "installation")
			require.NoError(t, err)
			userID := "user"
			if tc.withoutIdentity {
				userID = ""
			}
			ctx, err = service.WithRoutingAssignment(ctx, "installation", userID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantPassthrough, auth.RoutingPassthroughFrom(ctx))
		})
	}
}

func TestRoutingPolicyLookupErrorsAndInvalidation(t *testing.T) {
	fixture := &routingPolicyFixture{policyErr: errors.New("database offline")}
	cache := auth.NewRoutingPolicyCache(time.Minute)
	service := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithRoutingPolicies(fixture, cache)
	_, err := service.WithRoutingPolicy(context.Background(), "installation")
	require.ErrorIs(t, err, auth.ErrRoutingPolicyUnavailable)

	fixture.policyErr = nil
	fixture.policy = auth.RoutingPolicy{Mode: auth.RoutingPolicyAssigned, Revision: 1}
	ctx, err := service.WithRoutingPolicy(context.Background(), "installation")
	require.NoError(t, err)
	fixture.userErr = errors.New("assignment lookup failed")
	_, err = service.WithRoutingAssignment(ctx, "installation", "user")
	require.ErrorIs(t, err, auth.ErrRoutingPolicyUnavailable)

	fixture.userErr = nil
	fixture.assigned = true
	ctx, err = service.WithRoutingAssignment(ctx, "installation", "user")
	require.NoError(t, err)
	assert.False(t, auth.RoutingPassthroughFrom(ctx))

	fixture.assigned = false
	service.InvalidateInstallation("installation")
	ctx, err = service.WithRoutingPolicy(context.Background(), "installation")
	require.NoError(t, err)
	ctx, err = service.WithRoutingAssignment(ctx, "installation", "user")
	require.NoError(t, err)
	assert.True(t, auth.RoutingPassthroughFrom(ctx))
	assert.Equal(t, 3, fixture.policyReads)
	assert.Equal(t, 3, fixture.userReads)
}
