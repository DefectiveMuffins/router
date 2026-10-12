package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const openCodeTestSessionHeader = "x-opencode-session"

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

type openCodeHit struct {
	path    string
	model   string
	apiKey  string
	auth    string
	session string
	generic string
	userID  string
}

type openCodeUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	hits []openCodeHit
}

func (u *openCodeUpstream) recorded() []openCodeHit {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]openCodeHit(nil), u.hits...)
}

func (u *openCodeUpstream) paths() []string {
	var out []string
	for _, h := range u.recorded() {
		out = append(out, h.path)
	}
	return out
}

func newOpenCodeUpstream(t *testing.T, responsesStatus int) *openCodeUpstream {
	t.Helper()
	u := &openCodeUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.hits = append(u.hits, openCodeHit{
			path:    r.URL.Path,
			model:   gjson.GetBytes(body, "model").String(),
			apiKey:  r.Header.Get("x-api-key"),
			auth:    r.Header.Get("Authorization"),
			session: r.Header.Get(openCodeTestSessionHeader),
			generic: r.Header.Get("x-session-affinity"),
			userID:  gjson.GetBytes(body, "metadata.user_id").String(),
		})
		u.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(anthropicMessageSSE))
		case strings.HasSuffix(r.URL.Path, "/responses") && responsesStatus != http.StatusOK:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(responsesStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			writeOpenAIResponsesSSE(w)
		default:
			writeOpenAIChatSSE(w)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func openCodeTestIDs() map[string]string {
	out := make(map[string]string)
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Provider == providers.ProviderOpenCode {
				out[m.ID] = b.UpstreamID
			}
		}
	}
	return out
}

func openCodeTestClient(serverURL string) providers.Client {
	ids := openCodeTestIDs()
	return dispatch.NewSurfaceClient(dispatch.SurfaceClients{
		OpenAI: openaicompat.NewClientWithModelIDMap("test-key", serverURL+"/zen/go/v1", ids).
			WithSessionHeader(openCodeTestSessionHeader),
		Anthropic: anthropic.NewClient("test-key", serverURL+"/zen/go").
			WithModelIDMap(ids).
			WithSessionHeader(openCodeTestSessionHeader),
	}, func(model string) providers.WireSurface { return catalog.SurfaceFor(providers.ProviderOpenCode, model) })
}

func openCodeService(serverURL string, model catalog.ModelID) (*proxy.Service, *fakeRouter) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenCode, Model: model.String()}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderOpenCode: openCodeTestClient(serverURL)},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenCode: {}})
	return svc, fr
}

const openCodeMessagesTurn = `{"model":"claude-x","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"summarize this repo"}]}`

const openCodeCallerUserID = "user_abc_account_1111_session_2222"

const openCodeMessagesTurnWithUserID = `{"model":"claude-x","stream":true,"max_tokens":1024,"metadata":{"user_id":"` + openCodeCallerUserID + `"},"messages":[{"role":"user","content":"summarize this repo"}]}`

const openCodeChatTurn = `{"model":"auto","stream":true,"messages":[{"role":"user","content":"summarize this repo"}]}`

func TestProxyMessages_OpenCodeMessagesRowUsesMessagesWire(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusOK)
	svc, _ := openCodeService(upstream.URL, catalog.ModelIDOpenCodeQwen38Max)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(openCodeMessagesTurnWithUserID), rec, req))

	hits := upstream.recorded()
	require.Len(t, hits, 1)
	assert.Equal(t, "/zen/go/v1/messages", hits[0].path)
	assert.Equal(t, "qwen3.8-max", hits[0].model)
	assert.Equal(t, "test-key", hits[0].apiKey)
	assert.Regexp(t, hex32, hits[0].session)
	assert.NotEqual(t, openCodeCallerUserID, hits[0].userID, "the caller's user identifier never reaches the plan")
	if hits[0].userID != "" {
		assert.Regexp(t, hex32, hits[0].userID, "only a session digest may stand in for it")
	}
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: message_start")
}

func TestProxyMessages_OpenCodeResponsesRowUsesResponsesWire(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusOK)
	svc, _ := openCodeService(upstream.URL, catalog.ModelIDOpenCodeGrok47)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(openCodeMessagesTurn), rec, req))

	hits := upstream.recorded()
	require.Len(t, hits, 1)
	assert.Equal(t, "/zen/go/v1/responses", hits[0].path)
	assert.Equal(t, "grok-4.7", hits[0].model)
	assert.Equal(t, "Bearer test-key", hits[0].auth)
	assert.Regexp(t, hex32, hits[0].session)
	assert.Contains(t, rec.Body.String(), "event: message_start")
}

func TestProxyMessages_OpenCodeChatRowHasNoGenericAffinityHeader(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusOK)
	svc, _ := openCodeService(upstream.URL, catalog.ModelIDOpenCodeGLM53)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(openCodeMessagesTurn), rec, req))

	hits := upstream.recorded()
	require.Len(t, hits, 1)
	assert.Equal(t, "/zen/go/v1/chat/completions", hits[0].path)
	assert.Equal(t, "glm-5.3", hits[0].model)
	assert.Empty(t, hits[0].generic)
	assert.Regexp(t, hex32, hits[0].session)
}

func TestProxyOpenAIChat_OpenCodeResponsesRowTranslatesBackToChat(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusOK)
	svc, _ := openCodeService(upstream.URL, catalog.ModelIDOpenCodeGPT6Luna)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(openCodeChatTurn), rec, req))

	assert.Equal(t, []string{"/zen/go/v1/responses"}, upstream.paths())
	assert.Contains(t, rec.Body.String(), `"object":"chat.completion.chunk"`)
}

func TestProxyOpenAIChat_OpenCodeResponsesRow404IsNotRetriedOnChat(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusNotFound)
	svc, _ := openCodeService(upstream.URL, catalog.ModelIDOpenCodeGPT6Luna)
	body := strings.Replace(openCodeChatTurn, `"stream":true`, `"stream":false`, 1)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	err := svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, req)

	require.Error(t, err)
	assert.Equal(t, []string{"/zen/go/v1/responses"}, upstream.paths())
	assert.Equal(t, http.StatusNotFound, rec.Code, "the upstream verdict reaches the client")
	assert.Contains(t, rec.Body.String(), "not found")
}

func TestProxyMessages_OpenCodeResponsesRow404IsNotRetriedOnChat(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusNotFound)
	svc, _ := openCodeService(upstream.URL, catalog.ModelIDOpenCodeGrok47)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	_ = svc.ProxyMessages(context.Background(), []byte(openCodeMessagesTurn), rec, req)

	assert.Equal(t, []string{"/zen/go/v1/responses"}, upstream.paths())
	assert.Equal(t, http.StatusNotFound, rec.Code, "the upstream verdict reaches the client")
}

func TestProxyOpenAIChat_OpenCodeMessagesRowUsesMessagesWire(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusOK)
	svc, _ := openCodeService(upstream.URL, catalog.ModelIDOpenCodeQwen38Max)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(openCodeChatTurn), rec, req))

	assert.Equal(t, []string{"/zen/go/v1/messages"}, upstream.paths())
	hits := upstream.recorded()
	require.Len(t, hits, 1)
	assert.Equal(t, "qwen3.8-max", hits[0].model)
	assert.Equal(t, "test-key", hits[0].apiKey)
	assert.Contains(t, rec.Body.String(), `"content":"hi"`, "the Messages reply is translated back to chat chunks")
	assert.NotContains(t, rec.Body.String(), "event: message_start")
}

// openCodeTelemetryService is openCodeService with a pin store and telemetry
// sink, so the turn's usage is extracted and recorded.
func openCodeTelemetryService(serverURL string, model catalog.ModelID) (*proxy.Service, *fakeRouter, *fakePinStore, *captureTelemetry) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenCode, Model: model.String()}}
	store := newFakePinStore()
	telem := newCaptureTelemetry()
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderOpenCode: openCodeTestClient(serverURL)},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", telem,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenCode: {}})
	return svc, fr, store, telem
}

func TestProxyMessages_OpenCodeMessagesRowRecordsAnthropicUsage(t *testing.T) {
	upstream := newOpenCodeUpstream(t, http.StatusOK)
	svc, _, store, telem := openCodeTelemetryService(upstream.URL, catalog.ModelIDOpenCodeQwen38Max)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(authedCtx(uuid.New().String()), []byte(openCodeMessagesTurn), rec, req))

	select {
	case <-store.usageCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected UpdateUsage within 2s; none observed")
	}
	store.mu.Lock()
	require.NotEmpty(t, store.usages)
	got := store.usages[len(store.usages)-1]
	store.mu.Unlock()
	assert.Equal(t, 5, got.InputTokens, "the Messages stream's usage is parsed as Anthropic SSE")
	assert.Equal(t, 1, got.OutputTokens)
	assert.Equal(t, int32(5), telem.firstRow(t).InputTokens)
}

const openCodeClaudeCodeToolTurn = `{"model":"claude-x","stream":true,"max_tokens":1024,` +
	`"system":[{"type":"text","text":"You are Claude Code."}],` +
	`"tools":[{"name":"Read","description":"r","input_schema":{"type":"object"}}],` +
	`"messages":[{"role":"user","content":"why is revenue too high in fct_orders?"}]}`

func TestProxyMessages_OpenCodeWorkspaceAppendFiredFollowsTheRowSurface(t *testing.T) {
	cases := []struct {
		model catalog.ModelID
		fired bool
	}{
		{catalog.ModelIDOpenCodeQwen38Max, false},
		{catalog.ModelIDOpenCodeGLM53, true},
	}
	for _, tc := range cases {
		t.Run(tc.model.String(), func(t *testing.T) {
			upstream := newOpenCodeUpstream(t, http.StatusOK)
			svc, _, _, telem := openCodeTelemetryService(upstream.URL, tc.model)
			svc = svc.WithCCWorkspaceSystemAppend(true)
			ctx := requestcontext.WithClientIdentity(authedCtx(uuid.New().String()), requestcontext.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			require.NoError(t, svc.ProxyMessages(ctx, []byte(openCodeClaudeCodeToolTurn), rec, req))

			row := telem.firstRow(t)
			if tc.fired {
				require.NotNil(t, row.WorkspaceAppendFired)
				assert.True(t, *row.WorkspaceAppendFired)
			} else {
				assert.Nil(t, row.WorkspaceAppendFired, "PrepareAnthropic serves a Messages row without the append")
			}
		})
	}
}

func TestProxyOpenAIChat_OpenCodeMessagesRowReportsEffortUnsent(t *testing.T) {
	cases := []struct {
		model    catalog.ModelID
		wantSent string
	}{
		{catalog.ModelIDOpenCodeClaudeHaiku55, ""},
		{catalog.ModelIDOpenCodeGLM53, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.model.String(), func(t *testing.T) {
			upstream := newOpenCodeUpstream(t, http.StatusOK)
			svc, fr, _, telem := openCodeTelemetryService(upstream.URL, tc.model)
			fr.decision.Effort = "high"

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
			require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.New().String()), []byte(openCodeChatTurn), rec, req))

			row := telem.firstRow(t)
			assert.Equal(t, "high", row.EffortArm)
			assert.Equal(t, tc.wantSent, row.EffortSent, "the chat-to-Messages emit writes no effort, so none is reported as sent")
		})
	}
}

// A chat caller's turn to first-party Anthropic takes the same emit as an
// opencode Messages row, so it too reports the arm's level as unsent.
func TestProxyOpenAIChat_FirstPartyAnthropicReportsEffortUnsent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.False(t, gjson.GetBytes(body, "thinking").Exists())
		assert.False(t, gjson.GetBytes(body, "output_config").Exists())
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageSSE))
	}))
	t.Cleanup(upstream.Close)
	telem := newCaptureTelemetry()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-5", Effort: "high"}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("test-key", upstream.URL)},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", telem,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.New().String()), []byte(openCodeChatTurn), rec, req))

	row := telem.firstRow(t)
	assert.Equal(t, "high", row.EffortArm)
	assert.Equal(t, "high", row.EffortSelected)
	assert.Empty(t, row.EffortSent, "the chat-to-Messages emit writes no effort for any Anthropic-family target")
}
