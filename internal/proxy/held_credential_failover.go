package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/router"
)

// WithHeldCredentialFailover lets sibling failover run on requests that carry
// their own credentials (ROUTER_HELD_CREDENTIAL_FAILOVER, default off), limited
// to siblings those credentials can serve.
func (s *Service) WithHeldCredentialFailover(enabled bool) *Service {
	s.heldCredentialFailover = enabled
	return s
}

// failoverSiblings returns the siblings a failed turn may walk and whether
// failover is permitted at all. Own-credential requests normally never fail
// over because a sibling could fall through to the deployment key; the
// held-credential mode keeps only siblings the caller's own subscription or
// BYOK keys serve, so no rescue can spend on a key the caller did not bring.
func (s *Service) failoverSiblings(ctx context.Context, headers http.Header, siblings []router.Decision) ([]router.Decision, bool) {
	if len(siblings) == 0 {
		return nil, false
	}
	if s.shouldFailover(ctx) || s.gatewaySiblingAllowed(ctx, siblings[0]) {
		return siblings, true
	}
	if !s.heldCredentialFailover {
		return siblings, false
	}
	held := make([]router.Decision, 0, len(siblings))
	for _, sibling := range siblings {
		// Resolve from a credential-free ctx: the primary attempt's credential
		// may still be attached and would make any provider look held.
		resolved := s.resolveCredentials(clearCredentials(ctx), sibling.Provider, sibling.Model, headers)
		if CredentialsFromContext(resolved) != nil {
			held = append(held, sibling)
		}
	}
	return held, len(held) > 0
}
