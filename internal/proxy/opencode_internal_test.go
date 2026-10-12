package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

const openCodeLimitBody = `{"error":{"type":"GoUsageLimitError","message":"usage limit reached"}}`

func openCodeLimitErr(retryAfter string) error {
	headers := http.Header{}
	if retryAfter != "" {
		headers.Set("Retry-After", retryAfter)
	}
	return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Headers: headers, Body: []byte(openCodeLimitBody)}
}

func TestResolveAndInjectCredentials_OpenCodeNeverForwardsCallerCredentials(t *testing.T) {
	ctx := context.WithValue(context.Background(), CredentialsContextKey{}, &Credentials{APIKey: []byte("sk-ant-oat01-earlier-attempt"), OAuth: true})
	headers := http.Header{"Authorization": []string{"Bearer sk-test-client"}}

	out := resolveAndInjectCredentials(ctx, providers.ProviderOpenCode, catalog.ModelIDOpenCodeGLM53.String(), headers)
	assert.Nil(t, CredentialsFromContext(out), "only the deployment key may authenticate the plan")

	control := resolveAndInjectCredentials(context.Background(), providers.ProviderFireworks, "z-ai/glm-5.3", headers)
	require.NotNil(t, CredentialsFromContext(control), "the same inbound bearer is extracted for an ordinary provider")
	assert.Equal(t, []byte("sk-test-client"), CredentialsFromContext(control).APIKey)
}

func TestBindRequestLogger_StashesSessionAffinityToken(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-x","max_tokens":16,"messages":[{"role":"user","content":"refactor the parser"}]}`))
	require.NoError(t, err)

	ctx, _, key := bindRequestLogger(context.Background(), env, "key-1", "req-1", "anthropic_messages")

	token := requestcontext.SessionAffinityFromContext(ctx)
	assert.Equal(t, sessionKeyHex(key), token)
	assert.Len(t, token, 32)
}

func TestOpenCodeUsageLimitReset(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		err       error
		wantAt    time.Time
		wantLimit bool
	}{
		{"no Retry-After re-probes after an hour", openCodeLimitErr(""), now.Add(time.Hour), true},
		{"Retry-After sets the reset", openCodeLimitErr("120"), now.Add(2 * time.Minute), true},
		{"Retry-After is capped", openCodeLimitErr("99999999"), now.Add(31 * 24 * time.Hour), true},
		{"429 without the marker is a throttle", &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":"slow down"}`)}, time.Time{}, false},
		{"marker on another status", &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Body: []byte(openCodeLimitBody)}, time.Time{}, false},
		{"transport error", errors.New("connection reset"), time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at, limited := openCodeUsageLimitReset(tc.err, now)
			assert.Equal(t, tc.wantLimit, limited)
			assert.True(t, tc.wantAt.Equal(at), "reset %s, want %s", at, tc.wantAt)
		})
	}
}

func openCodeDispatch(t *testing.T, client *fakeClient, now time.Time) (*Service, error) {
	t.Helper()
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenCode: client})
	s.retrySleep = noopSleep
	s.now = func() time.Time { return now }
	s.usageObserver = usage.NewObserver([]byte("salt"), time.Minute, func() time.Time { return now })

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err := s.dispatchWithFallback(context.Background(), failoverInputs{
		w:               rec,
		buf:             buf,
		initialDecision: router.Decision{Provider: providers.ProviderOpenCode, Model: catalog.ModelIDOpenCodeGLM53.String()},
		purpose:         inference.PurposeAnthropicMessages,
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderOpenCode, UpstreamID: "glm-5.3"}},
		attempt: func(ctx context.Context, d router.Decision, p providers.Client) error {
			buf.Seal()
			return p.Proxy(ctx, d, providers.PreparedRequest{}, buf, r)
		},
		flushErr: flushBufferedIfPresent,
	})
	return s, err
}

func TestDispatchWithFallback_OpenCodeUsageLimitRecordsScopedExhaustionWithoutRetry(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{name: providers.ProviderOpenCode, outcomes: []fakeOutcome{
		{err: openCodeLimitErr("")},
		{writeBytes: []byte("ok")},
	}}

	s, err := openCodeDispatch(t, client, now)

	require.Error(t, err)
	assert.Equal(t, http.StatusTooManyRequests, upstreamStatus(err))
	assert.Equal(t, 1, client.calls, "a plan limit is not retried on the same binding")
	snap, observed := s.usageObserver.Snapshot(usage.ProviderLaneKey(providers.ProviderOpenCode))
	require.True(t, observed)
	w, ok := snap.Scoped["glm-5.3"]
	require.True(t, ok, "the refused model is recorded under its upstream id")
	assert.Equal(t, usage.StatusRejected, w.Status)
	assert.InDelta(t, 1.0, w.UsedPercent, 1e-9)
	assert.True(t, now.Add(time.Hour).Equal(w.ResetAt))
}

func TestDispatchWithFallback_OpenCodeOrdinaryThrottleIsRetriedAndNotRecorded(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{name: providers.ProviderOpenCode, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":"slow down"}`)}},
		{writeBytes: []byte("ok")},
	}}

	s, err := openCodeDispatch(t, client, now)

	require.NoError(t, err)
	assert.Equal(t, 2, client.calls)
	_, observed := s.usageObserver.Snapshot(usage.ProviderLaneKey(providers.ProviderOpenCode))
	assert.False(t, observed)
}

func TestHeadroomFromSnapshot_DropsExpiredScope(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	lane := headroomFromSnapshot(providers.ProviderOpenCode, usage.Snapshot{Scoped: map[string]usage.ScopedWindow{
		"a": {Window: usage.Window{UsedPercent: 1, ResetAt: now.Add(-time.Second)}, Status: usage.StatusRejected},
		"b": {Window: usage.Window{UsedPercent: 1, ResetAt: now.Add(time.Hour)}, Status: usage.StatusRejected},
	}}, now)

	require.Len(t, lane.Windows, 1)
	assert.Equal(t, "b", lane.Windows[0].Scope)
}

func TestOpenCodeLaneHeadroom(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }
	enabled := map[string]struct{}{providers.ProviderOpenCode: {}}
	target := func(id catalog.ModelID, upstream string) inference.Target {
		return inference.Target{Provider: providers.ProviderOpenCode, CatalogID: id.String(), UpstreamID: upstream}
	}

	assert.Nil(t, (&Service{}).openCodeLaneHeadroom(enabled), "no observer")

	s := &Service{now: clock, usageObserver: usage.NewObserver([]byte("salt"), time.Minute, clock)}
	assert.Nil(t, s.openCodeLaneHeadroom(enabled), "nothing recorded")

	s.recordOpenCodeUsageLimit(context.Background(), target(catalog.ModelIDOpenCodeGLM53, "glm-5.3"), openCodeLimitErr(""))
	assert.Nil(t, s.openCodeLaneHeadroom(map[string]struct{}{providers.ProviderFireworks: {}}), "lane not enabled")

	lanes := s.openCodeLaneHeadroom(enabled)
	require.Len(t, lanes, 1)
	assert.Equal(t, providers.ProviderOpenCode, lanes[0].Provider)
	assert.False(t, lanes[0].Exhausted, "one model's limit leaves the plan's other models eligible")
	require.Len(t, lanes[0].Windows, 1)
	w := lanes[0].Windows[0]
	assert.Equal(t, "secondary", w.Name)
	assert.Equal(t, "glm-5.3", w.Scope)
	assert.Equal(t, usage.StatusRejected, w.Status)
	assert.InDelta(t, 1.0, w.UsedFraction, 1e-9)

	s.usageObserver = usage.NewObserver([]byte("salt"), time.Minute, clock)
	s.recordOpenCodeUsageLimit(context.Background(), target(catalog.ModelIDOpenCodeKimiK3, "kimi-k3"), openCodeLimitErr("600"))
	s.recordOpenCodeUsageLimit(context.Background(), target(catalog.ModelIDOpenCodeGLM53, "glm-5.3"), openCodeLimitErr("7200"))
	now = start.Add(30 * time.Minute)
	lanes = s.openCodeLaneHeadroom(enabled)
	require.Len(t, lanes, 1)
	require.Len(t, lanes[0].Windows, 1, "the expired scope is dropped while the later one holds")
	assert.Equal(t, "glm-5.3", lanes[0].Windows[0].Scope)

	now = start.Add(2*time.Hour + time.Second)
	assert.Nil(t, s.openCodeLaneHeadroom(enabled), "every limit has reset")
}

func TestRecordOpenCodeUsageLimit_IgnoresOtherProviders(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := &Service{now: clock, usageObserver: usage.NewObserver([]byte("salt"), time.Minute, clock)}

	s.recordOpenCodeUsageLimit(context.Background(), inference.Target{Provider: providers.ProviderFireworks, CatalogID: "z-ai/glm-5.3"}, openCodeLimitErr(""))

	_, observed := s.usageObserver.Snapshot(usage.ProviderLaneKey(providers.ProviderOpenCode))
	assert.False(t, observed)
}

func TestTranslationPlan_NativeOpenAIChatExcludesOpenCodeMessagesRows(t *testing.T) {
	svc := &Service{clients: dispatch.NewClients(map[string]providers.Client{
		providers.ProviderOpenAI:   nil,
		providers.ProviderOpenCode: nil,
	})}
	messagesRows := []catalog.ModelID{catalog.ModelIDOpenCodeClaudeHaiku55, catalog.ModelIDOpenCodeQwen38Max, catalog.ModelIDOpenCodeQwen38Flash}
	req := router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderOpenCode: {}},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat:     router.WireFormatOpenAI,
			Endpoint:         router.EndpointOpenAIChat,
			StructuredOutput: true,
		},
	}

	svc.translationCompatibilityMode = TranslationCompatibilityEnforce
	enforce := svc.planTranslation(req)
	for _, id := range messagesRows {
		assert.Contains(t, enforce.ExcludedModels, id.String())
		requireModelExclusion(t, enforce, "surface_family_mismatch", id.String(), true)
	}
	assert.NotContains(t, enforce.ExcludedModels, catalog.ModelIDOpenCodeGLM53.String())
	assert.NotContains(t, enforce.ExcludedModels, catalog.ModelIDOpenCodeGrok47.String())
	assert.Contains(t, enforce.EnabledProviders, providers.ProviderOpenCode)

	svc.translationCompatibilityMode = TranslationCompatibilityShadow
	shadow := svc.planTranslation(req)
	for _, id := range messagesRows {
		assert.NotContains(t, shadow.ExcludedModels, id.String())
		requireModelExclusion(t, shadow, "surface_family_mismatch", id.String(), false)
	}
}

func TestEffortEscalationExemption_CoversNamespacedGrok(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	id := catalog.ModelIDOpenCodeGrok47.String()

	got := svc.resolveEffort(context.Background(), router.Decision{Provider: providers.ProviderOpenCode, Model: id}, router.Lookup(id), false)

	assert.Equal(t, "low", got.Sent, "the grok default applies with escalation off, as for the vendor row")
	assert.Equal(t, effortSourceModelPolicy, got.Source)
}

func TestResolveForceModel_OpenCodeTails(t *testing.T) {
	cases := []struct {
		input, wantID, wantProvider string
		wantKnown                   bool
	}{
		{"deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash", providers.ProviderFireworks, true},
		{"mimo-v2.6-pro", "xiaomi/mimo-v2.6-pro", providers.ProviderDeepInfra, true},
		{"mimo-v2.6-flash", "xiaomi/mimo-v2.6-flash", providers.ProviderDeepInfra, true},
		{"claude-haiku-5-5", "claude-haiku-5-5", providers.ProviderAnthropic, false},
		{catalog.ModelIDOpenCodeQwen38Flash.String(), catalog.ModelIDOpenCodeQwen38Flash.String(), providers.ProviderOpenCode, true},
		{"qwen3.8-flash", catalog.ModelIDOpenCodeQwen38Flash.String(), providers.ProviderOpenCode, true},
		{"qwen3.8-max", "qwen/qwen3.8-max", providers.ProviderFireworks, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			id, provider, known := resolveForceModel(tc.input)
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantProvider, provider)
			assert.Equal(t, tc.wantKnown, known)
		})
	}
}

func requireModelExclusion(t *testing.T, plan TranslationPlan, code, model string, enforced bool) {
	t.Helper()
	for _, exclusion := range plan.Exclusions {
		if exclusion.Code == code && exclusion.Model == model {
			assert.Equal(t, enforced, exclusion.Enforced)
			return
		}
	}
	t.Fatalf("missing exclusion code=%q model=%q in %#v", code, model, plan.Exclusions)
}

func TestOpenCodeLimitedModels_SoftExclusionUntilReset(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }
	s := &Service{now: clock, usageObserver: usage.NewObserver([]byte("salt"), time.Minute, clock)}
	glm, kimi := catalog.ModelIDOpenCodeGLM53.String(), catalog.ModelIDOpenCodeKimiK3.String()

	assert.Nil(t, s.globalAutomaticExcludedModels(context.Background()), "nothing recorded")

	s.recordOpenCodeUsageLimit(context.Background(), inference.Target{Provider: providers.ProviderOpenCode, CatalogID: glm, UpstreamID: "glm-5.3"}, openCodeLimitErr("600"))
	excluded := s.globalAutomaticExcludedModels(context.Background())
	assert.Equal(t, map[string]struct{}{glm: {}}, excluded, "only the refused model is withheld")
	assert.NotContains(t, excluded, kimi)
	reason, disabled := s.globalAutomaticExclusionReason(context.Background(), glm)
	assert.True(t, disabled)
	assert.Equal(t, openCodeUsageLimitExclusionReason, reason)

	now = start.Add(10*time.Minute + time.Second)
	assert.Nil(t, s.globalAutomaticExcludedModels(context.Background()), "readmitted once the limit resets")
	_, disabled = s.globalAutomaticExclusionReason(context.Background(), glm)
	assert.False(t, disabled)

	// A later limit on another model keeps the lane's snapshot fresh, so only
	// the per-scope reset readmits the model whose own limit has passed.
	now = start
	s.usageObserver = usage.NewObserver([]byte("salt"), time.Minute, clock)
	s.recordOpenCodeUsageLimit(context.Background(), inference.Target{Provider: providers.ProviderOpenCode, CatalogID: kimi, UpstreamID: "kimi-k3"}, openCodeLimitErr("600"))
	s.recordOpenCodeUsageLimit(context.Background(), inference.Target{Provider: providers.ProviderOpenCode, CatalogID: glm, UpstreamID: "glm-5.3"}, openCodeLimitErr("7200"))
	now = start.Add(30 * time.Minute)
	assert.Equal(t, map[string]struct{}{glm: {}}, s.globalAutomaticExcludedModels(context.Background()),
		"the model whose limit reset is readmitted while the later limit holds")
	_, disabled = s.globalAutomaticExclusionReason(context.Background(), kimi)
	assert.False(t, disabled)
	_, disabled = s.globalAutomaticExclusionReason(context.Background(), glm)
	assert.True(t, disabled)
}

// openCodeCapturingRouter records the request the turn loop routed with.
type openCodeCapturingRouter struct {
	captured *router.Request
	decision router.Decision
}

func (r *openCodeCapturingRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.captured = &req
	return r.decision, nil
}

const openCodeChatSSE = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

func TestProxyMessages_OpenCodeLaneReachesPolicyRequest(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, clock)
	observer.Record(usage.ProviderLaneKey(providers.ProviderOpenCode), usage.Snapshot{
		Scoped: map[string]usage.ScopedWindow{"kimi-k3": {
			Window: usage.Window{UsedPercent: 1, WindowMinutes: 61, ResetAt: now.Add(time.Hour)},
			Status: usage.StatusRejected,
		}},
	})
	fr := &openCodeCapturingRouter{decision: router.Decision{Provider: providers.ProviderOpenCode, Model: catalog.ModelIDOpenCodeGLM53.String()}}
	client := &fakeClient{name: providers.ProviderOpenCode, outcomes: []fakeOutcome{{writeBytes: []byte(openCodeChatSSE)}, {writeBytes: []byte(openCodeChatSSE)}}}
	svc := NewService(fr, map[string]providers.Client{providers.ProviderOpenCode: client},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenCode: {}}).WithUsageObserver(observer)
	svc.now = clock
	body := []byte(`{"model":"claude-x","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"summarize this repo"}]}`)

	require.NoError(t, svc.ProxyMessages(context.Background(), body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))

	require.NotNil(t, fr.captured)
	var lane *router.SubscriptionHeadroom
	for i := range fr.captured.SubscriptionHeadroom {
		if fr.captured.SubscriptionHeadroom[i].Provider == providers.ProviderOpenCode {
			lane = &fr.captured.SubscriptionHeadroom[i]
		}
	}
	require.NotNil(t, lane, "the plan lane reaches the policy request")
	require.Len(t, lane.Windows, 1)
	assert.Equal(t, "kimi-k3", lane.Windows[0].Scope)
	assert.Equal(t, usage.StatusRejected, lane.Windows[0].Status)
	assert.Contains(t, fr.captured.AutomaticExcludedModels, catalog.ModelIDOpenCodeKimiK3.String(), "Go withholds the limited model before the policy runs")
	assert.NotContains(t, fr.captured.AutomaticExcludedModels, catalog.ModelIDOpenCodeGLM53.String())

	now = now.Add(time.Hour + time.Second)
	fr.captured = nil
	require.NoError(t, svc.ProxyMessages(context.Background(), body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	require.NotNil(t, fr.captured)
	assert.NotContains(t, fr.captured.AutomaticExcludedModels, catalog.ModelIDOpenCodeKimiK3.String(), "readmitted after its reset")
}

func TestEffortOnChatIngress_AnthropicFamilyTargetSendsNoLevel(t *testing.T) {
	selected := effortResolution{Arm: "high", Selected: "high", Sent: "high", Source: effortSourceArm}
	cases := []struct {
		name     string
		decision router.Decision
		wantSent string
	}{
		{"opencode Messages row", router.Decision{Provider: providers.ProviderOpenCode, Model: catalog.ModelIDOpenCodeClaudeHaiku55.String()}, ""},
		{"first-party Anthropic", router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5"}, ""},
		{"opencode chat row", router.Decision{Provider: providers.ProviderOpenCode, Model: catalog.ModelIDOpenCodeGLM53.String()}, "high"},
		{"opencode Responses row", router.Decision{Provider: providers.ProviderOpenCode, Model: catalog.ModelIDOpenCodeGPT6Luna.String()}, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selected.onChatIngress(tc.decision)
			assert.Equal(t, tc.wantSent, got.Sent)
			assert.Equal(t, tc.wantSent == "", got.Mismatch(), "a level that never reached the wire must not train the arm")
			assert.Equal(t, "high", got.Arm)
		})
	}
}

func TestModelStripsAnthropicSignatures_FollowsTheRowSurface(t *testing.T) {
	assert.False(t, modelStripsAnthropicSignatures(catalog.ModelIDOpenCodeQwen38Max.String()), "a Messages row keeps signed thinking")
	assert.False(t, modelStripsAnthropicSignatures(catalog.ModelIDOpenCodeClaudeHaiku55.String()))
	assert.True(t, modelStripsAnthropicSignatures(catalog.ModelIDOpenCodeGLM53.String()), "a chat row drops it")
	assert.True(t, modelStripsAnthropicSignatures(catalog.ModelIDOpenCodeGrok47.String()))
}

func TestDispatchWithFallback_OtherProviderMarkerBodyIsStillRetried(t *testing.T) {
	client := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{
		{err: openCodeLimitErr("")},
		{writeBytes: []byte("ok")},
	}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: client})
	s.retrySleep = noopSleep
	s.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err := s.dispatchWithFallback(context.Background(), failoverInputs{
		w:               rec,
		buf:             buf,
		initialDecision: router.Decision{Provider: providers.ProviderFireworks, Model: "z-ai/glm-5.3"},
		purpose:         inference.PurposeAnthropicMessages,
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderFireworks, UpstreamID: "glm-5.3"}},
		attempt: func(ctx context.Context, d router.Decision, p providers.Client) error {
			buf.Seal()
			return p.Proxy(ctx, d, providers.PreparedRequest{}, buf, r)
		},
		flushErr: flushBufferedIfPresent,
	})

	require.NoError(t, err)
	assert.Equal(t, 2, client.calls, "only the plan's own limit ends same-binding retries")
}
