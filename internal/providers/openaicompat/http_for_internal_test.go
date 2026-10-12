package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestHTTPFor_NamespacedGrokRidesTheGrokTransport(t *testing.T) {
	c := NewClient("test-key", "https://upstream.example/v1")
	assert.Same(t, c.grokHTTP, c.httpFor("plan/grok-4.7", "grok-4.7"), "the rewritten upstream id still names a grok model")
	assert.Same(t, c.grokHTTP, c.httpFor("grok-4.7"))
	assert.Same(t, c.http, c.httpFor("plan/glm-5.3", "glm-5.3"))
}

type countingRoundTripper struct {
	calls atomic.Int32
}

func (rt *countingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

func TestProxy_MappedGrokUpstreamRidesTheGrokTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClientWithModelIDMap("k", srv.URL, map[string]string{"plan/grok-4.7": "grok-4.7"})
	grok := &countingRoundTripper{}
	c.grokHTTP = &http.Client{Transport: grok}

	err := c.Proxy(context.Background(), router.Decision{Model: "plan/grok-4.7"},
		providers.PreparedRequest{Body: []byte(`{"model":"plan/grok-4.7","messages":[]}`)},
		httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	require.NoError(t, err)
	assert.Equal(t, int32(1), grok.calls.Load(), "the call site checks the rewritten upstream id too")
}
