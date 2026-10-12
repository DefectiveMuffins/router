package requestcontext

import (
	"context"
	"net/http"
)

type sessionAffinityKey struct{}

// WithSessionAffinity stashes the conversation's opaque affinity token on ctx.
func WithSessionAffinity(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, sessionAffinityKey{}, token)
}

// SessionAffinityFromContext returns the token stashed by WithSessionAffinity, or "".
func SessionAffinityFromContext(ctx context.Context) string {
	token, _ := ctx.Value(sessionAffinityKey{}).(string)
	return token
}

// ApplySessionAffinityHeader sets header name to the request's affinity token.
// Without a token it removes the header, so a client-sent value never selects
// another conversation's upstream session. A blank name is a no-op.
func ApplySessionAffinityHeader(ctx context.Context, upstream *http.Request, name string) {
	if name == "" {
		return
	}
	if token := SessionAffinityFromContext(ctx); token != "" {
		upstream.Header.Set(name, token)
		return
	}
	upstream.Header.Del(name)
}
