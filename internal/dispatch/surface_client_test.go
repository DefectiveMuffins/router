package dispatch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

const (
	surfaceTestMessagesModel  = "plan/messages-model"
	surfaceTestResponsesModel = "plan/responses-model"
	surfaceTestChatModel      = "plan/chat-model"
)

type recordingSurfaceClient struct {
	calls []string
}

func (c *recordingSurfaceClient) Proxy(_ context.Context, decision router.Decision, _ providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	c.calls = append(c.calls, decision.Model)
	return nil
}

func (c *recordingSurfaceClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

type identifiedSurfaceClient struct {
	recordingSurfaceClient
}

func (c *identifiedSurfaceClient) DeploymentPrincipal() string { return "principal-1" }

func (c *identifiedSurfaceClient) ListModels(context.Context) ([]string, error) {
	return []string{"m1", "m2"}, nil
}

func surfaceTestSurfaces(model string) providers.WireSurface {
	switch model {
	case surfaceTestMessagesModel:
		return providers.SurfaceMessages
	case surfaceTestResponsesModel:
		return providers.SurfaceResponses
	case surfaceTestChatModel:
		return providers.SurfaceChatCompletions
	default:
		return providers.SurfaceProviderDefault
	}
}

func proxySurface(client *dispatch.SurfaceClient, model string, endpoint providers.Endpoint) error {
	return client.Proxy(context.Background(), router.Decision{Provider: providers.ProviderOpenCode, Model: model},
		providers.PreparedRequest{Endpoint: endpoint}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
}

func TestSurfaceClient_RoutesBySurface(t *testing.T) {
	openAIWire, messagesWire := &recordingSurfaceClient{}, &recordingSurfaceClient{}
	client := dispatch.NewSurfaceClient(dispatch.SurfaceClients{OpenAI: openAIWire, Anthropic: messagesWire}, surfaceTestSurfaces)

	require.NoError(t, proxySurface(client, surfaceTestMessagesModel, providers.EndpointChatCompletions))
	assert.Equal(t, []string{surfaceTestMessagesModel}, messagesWire.calls)
	assert.Empty(t, openAIWire.calls)

	require.NoError(t, proxySurface(client, surfaceTestChatModel, providers.EndpointChatCompletions))
	require.NoError(t, proxySurface(client, surfaceTestResponsesModel, providers.EndpointResponses))
	assert.Equal(t, []string{surfaceTestChatModel, surfaceTestResponsesModel}, openAIWire.calls)
	assert.Len(t, messagesWire.calls, 1)
}

func TestSurfaceClient_RejectsMismatchedEndpoint(t *testing.T) {
	openAIWire, messagesWire := &recordingSurfaceClient{}, &recordingSurfaceClient{}
	client := dispatch.NewSurfaceClient(dispatch.SurfaceClients{OpenAI: openAIWire, Anthropic: messagesWire}, surfaceTestSurfaces)

	cases := []struct {
		model    string
		endpoint providers.Endpoint
	}{
		{surfaceTestResponsesModel, providers.EndpointChatCompletions},
		{surfaceTestChatModel, providers.EndpointResponses},
		{surfaceTestMessagesModel, providers.EndpointResponses},
	}
	for _, tc := range cases {
		err := proxySurface(client, tc.model, tc.endpoint)
		assert.Truef(t, errors.Is(err, dispatch.ErrSurfaceMismatch), "%s on endpoint %d: %v", tc.model, tc.endpoint, err)
	}
	assert.Empty(t, openAIWire.calls)
	assert.Empty(t, messagesWire.calls)
}

func TestSurfaceClient_MissingMessagesClient(t *testing.T) {
	openAIWire := &recordingSurfaceClient{}
	client := dispatch.NewSurfaceClient(dispatch.SurfaceClients{OpenAI: openAIWire}, surfaceTestSurfaces)

	err := proxySurface(client, surfaceTestMessagesModel, providers.EndpointChatCompletions)
	assert.ErrorIs(t, err, dispatch.ErrProviderNotConfigured)
	assert.Empty(t, openAIWire.calls)
}

func TestSurfaceClient_PassthroughUnsupported(t *testing.T) {
	client := dispatch.NewSurfaceClient(dispatch.SurfaceClients{OpenAI: &recordingSurfaceClient{}}, surfaceTestSurfaces)
	err := client.Passthrough(context.Background(), providers.PreparedRequest{}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	assert.ErrorIs(t, err, providers.ErrNotImplemented)
}

func TestSurfaceClient_ForwardsPrincipalAndModelList(t *testing.T) {
	identified := dispatch.NewSurfaceClient(dispatch.SurfaceClients{OpenAI: &identifiedSurfaceClient{}}, surfaceTestSurfaces)
	assert.Equal(t, "principal-1", identified.DeploymentPrincipal())
	models, err := identified.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"m1", "m2"}, models)

	plain := dispatch.NewSurfaceClient(dispatch.SurfaceClients{OpenAI: &recordingSurfaceClient{}}, surfaceTestSurfaces)
	assert.Empty(t, plain.DeploymentPrincipal())
	_, err = plain.ListModels(context.Background())
	assert.ErrorIs(t, err, providers.ErrNotImplemented)
}
