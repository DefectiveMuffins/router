package requestcontext_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/requestcontext"
)

func TestSessionAffinityRoundTrip(t *testing.T) {
	ctx := requestcontext.WithSessionAffinity(context.Background(), "abc")
	assert.Equal(t, "abc", requestcontext.SessionAffinityFromContext(ctx))
	assert.Empty(t, requestcontext.SessionAffinityFromContext(context.Background()))
}

func TestApplySessionAffinityHeader(t *testing.T) {
	const header = "x-test-session"
	newUpstream := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "https://upstream.example/v1/chat/completions", nil)
		r.Header.Set(header, "spoof")
		return r
	}

	withToken := newUpstream()
	requestcontext.ApplySessionAffinityHeader(requestcontext.WithSessionAffinity(context.Background(), "token-1"), withToken, header)
	assert.Equal(t, []string{"token-1"}, withToken.Header.Values(header))

	withoutToken := newUpstream()
	requestcontext.ApplySessionAffinityHeader(context.Background(), withoutToken, header)
	assert.Empty(t, withoutToken.Header.Values(header))

	blankName := newUpstream()
	requestcontext.ApplySessionAffinityHeader(requestcontext.WithSessionAffinity(context.Background(), "token-1"), blankName, "")
	assert.Equal(t, "spoof", blankName.Header.Get(header))
}
