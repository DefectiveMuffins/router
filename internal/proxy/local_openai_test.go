package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingLocalServer struct {
	models  []string
	bodies  []string
	apiKeys []string
}

func (l *recordingLocalServer) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	l.models = append(l.models, decision.Model)
	l.bodies = append(l.bodies, string(requestcontext.ApplyModelAlias(ctx, prep.Body, decision.Model)))
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		l.apiKeys = append(l.apiKeys, string(creds.APIKey))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	return nil
}

func (l *recordingLocalServer) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func TestService_ForceModelHeader_ServesLocalAliasOnLocalServer(t *testing.T) {
	local := &recordingLocalServer{}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-5", Reason: "cluster"}}
	svc := proxy.NewService(
		fr, map[string]providers.Client{providers.ProviderLocalOpenAI: local},
		nil, false, nil, newFakePinStore(), false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithByokOnly(true)

	installationID := uuid.New().String()
	ctx := context.WithValue(authedCtx(installationID), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{
		InstallationID: installationID,
		Provider:       providers.ProviderLocalOpenAI,
		Plaintext:      []byte("spark-key"),
		BaseURL:        "http://192.0.2.10:8000/v1",
		ModelAliases:   map[string]string{"z-ai/glm-5.3-flash": "served-glm"},
	}})
	body := `{"model":"z-ai/glm-5.3-flash","messages":[{"role":"user","content":"Reply with just: hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	req.Header.Set(proxy.ForceModelHeader, "z-ai/glm-5.3-flash")
	req.Header.Set("Authorization", "Bearer chatgpt-oauth-jwt")
	req.Header.Set("ChatGPT-Account-ID", "acct-1")
	rec := httptest.NewRecorder()

	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req))

	assert.Equal(t, []string{"z-ai/glm-5.3-flash"}, local.models)
	require.Len(t, local.bodies, 1)
	assert.Contains(t, local.bodies[0], `"model":"served-glm"`, "the server's own model name goes upstream")
	assert.Equal(t, []string{"spark-key"}, local.apiKeys, "the local server never sees the caller's bearer")
}
