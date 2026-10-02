package translate

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Message-thread request types Claude Code sends in the top-level `thread`
// field. A create carries the full transcript; a continue carries only the
// messages after previous_message_id and may omit unchanged system/tools.
const (
	MessageThreadCreate   = "create"
	MessageThreadContinue = "continue"
)

const messageThreadsBetaPrefix = "message-threads-"

// MessageThreadType returns the Anthropic Messages body's thread.type, or "".
func MessageThreadType(body []byte) string {
	return gjson.GetBytes(body, "thread.type").String()
}

// StripMessageThread removes the top-level `thread` field.
func StripMessageThread(body []byte) ([]byte, error) {
	out, err := sjson.DeleteBytes(body, "thread")
	if err != nil {
		return body, fmt.Errorf("delete thread: %w", err)
	}
	return out, nil
}

// StripMessageThreadsBeta removes message-threads tokens from anthropic-beta.
func StripMessageThreadsBeta(h http.Header) {
	values := h.Values("anthropic-beta")
	if len(values) == 0 {
		return
	}
	kept := joinKept(strings.Join(values, ","), func(token string) bool {
		return !strings.HasPrefix(token, messageThreadsBetaPrefix)
	})
	if kept == "" {
		h.Del("anthropic-beta")
		return
	}
	h.Set("anthropic-beta", kept)
}
