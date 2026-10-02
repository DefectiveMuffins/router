package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const localGLM = "z-ai/glm-5.3-flash"

func localKeyCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{
		Provider:     providers.ProviderLocalOpenAI,
		Plaintext:    []byte("spark-key"),
		BaseURL:      "http://192.0.2.10:8000/v1",
		ModelAliases: map[string]string{localGLM: "served-glm"},
	}})
}

func TestEnabledProvidersForRequest_LocalKeyKeepsSubscriptionsEligible(t *testing.T) {
	s := &Service{
		clients: dispatch.NewClients(map[string]providers.Client{
			providers.ProviderAnthropic:   nil,
			providers.ProviderOpenAI:      nil,
			providers.ProviderLocalOpenAI: nil,
		}),
		deploymentKeyedProviders:     map[string]struct{}{},
		passthroughEligibleProviders: map[string]struct{}{},
	}
	ctx := context.WithValue(localKeyCtx(context.Background()), InstallationIDContextKey{}, uuid.NewString())
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-claude-code"}}

	got := s.enabledProvidersForRequest(ctx, providers.ProviderAnthropic, headers)

	assert.Contains(t, got, providers.ProviderAnthropic, "a local server must not displace the Claude subscription")
	assert.Contains(t, got, providers.ProviderLocalOpenAI)
	assert.Empty(t, s.gatewayProvidersForRequest(ctx), "a local server is not a gateway")
}

func TestForcedModelBinding_LocalAliasWinsOverUnkeyedCatalogVendor(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	binding, reason := svc.forcedModelBinding(localKeyCtx(context.Background()), localGLM, providers.ProviderDeepInfra)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderLocalOpenAI, binding)
}

func TestForcedModelBinding_UnaliasedModelKeepsCatalogProvider(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	binding, reason := svc.forcedModelBinding(localKeyCtx(context.Background()), "claude-sonnet-5", providers.ProviderAnthropic)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAnthropic, binding)
}

// A Codex request carries the caller's ChatGPT bearer; the local server must
// receive only its own key, never the subscription token.
func TestResolveCredentials_LocalServerGetsOnlyItsOwnKey(t *testing.T) {
	ctx := context.WithValue(localKeyCtx(context.Background()), InstallationIDContextKey{}, uuid.NewString())
	headers := http.Header{
		"Authorization":      []string{"Bearer chatgpt-oauth-jwt"},
		"Chatgpt-Account-Id": []string{"acct-1"},
	}

	creds := CredentialsFromContext(resolveAndInjectCredentials(ctx, providers.ProviderLocalOpenAI, localGLM, headers))

	require.NotNil(t, creds)
	assert.Equal(t, []byte("spark-key"), creds.APIKey)
	assert.False(t, creds.OAuth)
	assert.Equal(t, "http://192.0.2.10:8000/v1", creds.BaseURL)
}
