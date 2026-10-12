package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func heldCredentialContext() context.Context {
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, "sk-ant-oat01-held")
	// The primary attempt's own credential is what turns ordinary failover off.
	return context.WithValue(ctx, CredentialsContextKey{}, subscriptionCredsFromToken("sk-ant-oat01-held"))
}

func heldCredentialSiblings() []router.Decision {
	return []router.Decision{
		{Provider: providers.ProviderGoogle, Model: "gemini-3.5-pro"},
		{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-5-5"},
	}
}

func TestFailoverSiblingsOwnCredentialsStayOffByDefault(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderGoogle)

	_, permitted := s.failoverSiblings(heldCredentialContext(), http.Header{}, heldCredentialSiblings())

	assert.False(t, permitted)
}

func TestFailoverSiblingsHeldCredentialModeKeepsOnlyHeldProviders(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderGoogle).WithHeldCredentialFailover(true)

	siblings, permitted := s.failoverSiblings(heldCredentialContext(), http.Header{}, heldCredentialSiblings())

	assert.True(t, permitted)
	assert.Equal(t, []string{"claude-sonnet-5-5"}, siblingModels(siblings),
		"a sibling only the deployment key could serve must not be walked")
}

func TestFailoverSiblingsHeldCredentialModeRefusesWhenNothingHeld(t *testing.T) {
	s := siblingService(providers.ProviderGoogle).WithHeldCredentialFailover(true)
	ctx := context.WithValue(context.Background(), CredentialsContextKey{}, subscriptionCredsFromToken("sk-ant-oat01-held"))

	_, permitted := s.failoverSiblings(ctx, http.Header{}, heldCredentialSiblings()[:1])

	assert.False(t, permitted)
}
