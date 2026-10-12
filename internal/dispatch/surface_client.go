package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

// ErrSurfaceMismatch reports a prepared request whose endpoint the model's
// wire surface cannot serve; it is a router defect, never an upstream answer.
var ErrSurfaceMismatch = errors.New("prepared request does not match the model's wire surface")

// SurfaceClients are the inner clients a multi-surface provider dispatches to.
type SurfaceClients struct {
	// OpenAI serves chat/completions and Responses.
	OpenAI providers.Client
	// Anthropic serves Messages; nil when the provider has no key.
	Anthropic providers.Client
}

// SurfaceClient is one provider's client over several wire surfaces: it picks
// the inner client from the routed model's catalog surface.
type SurfaceClient struct {
	clients    SurfaceClients
	surfaceFor func(model string) providers.WireSurface
}

// NewSurfaceClient builds a SurfaceClient.
func NewSurfaceClient(clients SurfaceClients, surfaceFor func(model string) providers.WireSurface) *SurfaceClient {
	return &SurfaceClient{clients: clients, surfaceFor: surfaceFor}
}

// Proxy implements providers.Client.
func (c *SurfaceClient) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	inner, err := c.clientFor(decision.Model, prep.Endpoint)
	if err != nil {
		return err
	}
	return inner.Proxy(ctx, decision, prep, w, r)
}

// Passthrough implements providers.Client; a multi-surface provider has no
// single path to mirror.
func (c *SurfaceClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// DeploymentPrincipal forwards the OpenAI-wire client's account fingerprint.
func (c *SurfaceClient) DeploymentPrincipal() string {
	if identified, ok := c.clients.OpenAI.(providers.DeploymentPrincipal); ok {
		return identified.DeploymentPrincipal()
	}
	return ""
}

// ListModels forwards to the OpenAI-wire client's model listing.
func (c *SurfaceClient) ListModels(ctx context.Context) ([]string, error) {
	if lister, ok := c.clients.OpenAI.(providers.ModelLister); ok {
		return lister.ListModels(ctx)
	}
	return nil, fmt.Errorf("%w: model listing", providers.ErrNotImplemented)
}

func (c *SurfaceClient) clientFor(model string, endpoint providers.Endpoint) (providers.Client, error) {
	surface := c.surfaceFor(model)
	switch surface {
	case providers.SurfaceMessages:
		if endpoint == providers.EndpointResponses {
			return nil, fmt.Errorf("%w: %s on %s", ErrSurfaceMismatch, model, surface)
		}
		if c.clients.Anthropic == nil {
			return nil, fmt.Errorf("%w: %s on %s", ErrProviderNotConfigured, model, surface)
		}
		return c.clients.Anthropic, nil
	case providers.SurfaceResponses:
		if endpoint != providers.EndpointResponses {
			return nil, fmt.Errorf("%w: %s on %s", ErrSurfaceMismatch, model, surface)
		}
	default:
		if endpoint == providers.EndpointResponses {
			return nil, fmt.Errorf("%w: %s on %s", ErrSurfaceMismatch, model, surface)
		}
	}
	if c.clients.OpenAI == nil {
		return nil, fmt.Errorf("%w: %s on %s", ErrProviderNotConfigured, model, surface)
	}
	return c.clients.OpenAI, nil
}

var (
	_ providers.Client              = (*SurfaceClient)(nil)
	_ providers.DeploymentPrincipal = (*SurfaceClient)(nil)
	_ providers.ModelLister         = (*SurfaceClient)(nil)
)
