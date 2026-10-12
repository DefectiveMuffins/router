package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

type openCodeSeen struct {
	path, authorization, apiKey, userAgent, session, model string
}

type openCodeRecorder struct {
	mu   sync.Mutex
	seen []openCodeSeen
}

func (rec *openCodeRecorder) requests() []openCodeSeen {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]openCodeSeen(nil), rec.seen...)
}

func newOpenCodeTestServer(t *testing.T) (*httptest.Server, *openCodeRecorder) {
	t.Helper()
	rec := &openCodeRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.seen = append(rec.seen, openCodeSeen{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			apiKey:        r.Header.Get("x-api-key"),
			userAgent:     r.Header.Get("User-Agent"),
			session:       r.Header.Get("x-opencode-session"),
			model:         gjson.GetBytes(body, "model").String(),
		})
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func openCodeInbound() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", "client/9")
	r.Header.Set("x-opencode-session", "spoof")
	r.Header.Set("Authorization", "Bearer sk-test-client")
	return r
}

func TestNewOpenCodeClient_RoutesEachSurfaceWithItsAuth(t *testing.T) {
	srv, rec := newOpenCodeTestServer(t)
	client := newOpenCodeClient("test-key", srv.URL+"/zen/go/v1", srv.Client())
	const token = "0123456789abcdef0123456789abcdef"
	ctx := requestcontext.WithSessionAffinity(context.Background(), token)

	cases := []struct {
		id       catalog.ModelID
		endpoint providers.Endpoint
		path     string
		bearer   bool
	}{
		{catalog.ModelIDOpenCodeGLM53, providers.EndpointChatCompletions, "/zen/go/v1/chat/completions", true},
		{catalog.ModelIDOpenCodeGPT6Luna, providers.EndpointResponses, "/zen/go/v1/responses", true},
		{catalog.ModelIDOpenCodeClaudeHaiku55, providers.EndpointChatCompletions, "/zen/go/v1/messages", false},
	}
	for _, tc := range cases {
		prep := providers.PreparedRequest{Body: []byte(`{"model":"` + tc.id.String() + `","max_tokens":16}`), Endpoint: tc.endpoint}
		err := client.Proxy(ctx, router.Decision{Provider: providers.ProviderOpenCode, Model: tc.id.String()}, prep, httptest.NewRecorder(), openCodeInbound())
		require.NoError(t, err, tc.id)
	}

	seen := rec.requests()
	require.Len(t, seen, len(cases))
	for i, tc := range cases {
		got := seen[i]
		assert.Equal(t, tc.path, got.path, tc.id)
		assert.Equal(t, strings.TrimPrefix(tc.id.String(), providers.ProviderOpenCode+"/"), got.model, tc.id)
		if tc.bearer {
			assert.Equal(t, "Bearer test-key", got.authorization, tc.id)
			assert.Empty(t, got.apiKey, tc.id)
		} else {
			assert.Equal(t, "test-key", got.apiKey, tc.id)
			assert.Empty(t, got.authorization, tc.id)
		}
		assert.Equal(t, openCodeUserAgent, got.userAgent, tc.id)
		assert.Equal(t, token, got.session, tc.id)
	}
}

func TestNewOpenCodeClient_NoKeyLeavesMessagesUnconfigured(t *testing.T) {
	srv, rec := newOpenCodeTestServer(t)
	client := newOpenCodeClient("", srv.URL+"/zen/go/v1", srv.Client())

	err := client.Proxy(context.Background(),
		router.Decision{Provider: providers.ProviderOpenCode, Model: catalog.ModelIDOpenCodeQwen38Max.String()},
		providers.PreparedRequest{Body: []byte(`{"model":"opencode/qwen3.8-max","max_tokens":16}`)},
		httptest.NewRecorder(), openCodeInbound())

	assert.ErrorIs(t, err, dispatch.ErrProviderNotConfigured)
	assert.Empty(t, rec.requests())
}

func TestOpenCodeMessagesBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com/zen/go/v1":  "https://example.com/zen/go",
		"https://example.com/zen/go/v1/": "https://example.com/zen/go",
		"https://example.com/zen/go":     "https://example.com/zen/go",
	}
	for in, want := range cases {
		assert.Equal(t, want, openCodeMessagesBaseURL(in), in)
	}
}

func TestUpstreamIDsForProvider_OpenCode(t *testing.T) {
	ids := upstreamIDsForProvider(providers.ProviderOpenCode)
	require.Len(t, ids, 13)
	for id, upstream := range ids {
		assert.Equal(t, strings.TrimPrefix(id, providers.ProviderOpenCode+"/"), upstream)
	}
}

func TestRegisterOpenCodeProvider_LogsWithoutClaimingBYOK(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		byokOnly bool
		keyed    bool
		message  string
	}{
		{"deployment key", "test-key", false, true, "OpenCode Go provider enabled"},
		{"no key", "", false, false, "OpenCode Go provider registered but disabled (set OPENCODE_API_KEY)"},
		{"byok-only mode", "test-key", true, false, "OpenCode Go provider registered but disabled (deployment keys are not loaded in this mode)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENCODE_API_KEY", tc.key)
			srv, rec := newOpenCodeTestServer(t)
			var logs bytes.Buffer
			providerMap := map[string]providers.Client{}
			envKeyed := map[string]struct{}{}

			registerOpenCodeProvider(providerMap, envKeyed, slog.New(slog.NewTextHandler(&logs, nil)),
				srv.URL+"/zen/go/v1", tc.byokOnly, srv.Client())

			require.Contains(t, providerMap, providers.ProviderOpenCode, "registered in every mode")
			_, keyed := envKeyed[providers.ProviderOpenCode]
			assert.Equal(t, tc.keyed, keyed)
			assert.Contains(t, logs.String(), tc.message)
			assert.NotContains(t, logs.String(), "BYOK", "the plan has no BYOK path")

			// The Messages wire exists only when a deployment key was loaded, so a
			// Messages row shows whether the key was read in this mode.
			err := providerMap[providers.ProviderOpenCode].Proxy(context.Background(),
				router.Decision{Provider: providers.ProviderOpenCode, Model: catalog.ModelIDOpenCodeQwen38Max.String()},
				providers.PreparedRequest{Body: []byte(`{"model":"opencode/qwen3.8-max","max_tokens":16}`)},
				httptest.NewRecorder(), openCodeInbound())
			if !tc.keyed {
				assert.ErrorIs(t, err, dispatch.ErrProviderNotConfigured, "no deployment key reaches the plan")
				assert.Empty(t, rec.requests())
				return
			}
			require.NoError(t, err)
			seen := rec.requests()
			require.Len(t, seen, 1)
			assert.Equal(t, tc.key, seen[0].apiKey)
		})
	}
}
