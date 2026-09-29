package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CoalesceDuplicateToolResults merges every tool result that shares a call id
// into the first one, appending later contents in order and removing the
// later results. Codex code-mode records each notify() from an exec script as
// another output under the running call's id; OpenAI accepts that, but
// Anthropic 400s on a tool_use with more than one tool_result, and Codex
// resends the history every turn, so one such call bricks the session.
// Returns the number of duplicate results removed; the body is untouched when
// zero. Gemini-format envelopes are left alone.
func (e *RequestEnvelope) CoalesceDuplicateToolResults() int {
	if e == nil {
		return 0
	}
	switch e.format {
	case FormatOpenAI:
		return e.coalesceOpenAIToolResults()
	case FormatAnthropic:
		return e.coalesceAnthropicToolResults()
	default:
		return 0
	}
}

func (e *RequestEnvelope) coalesceOpenAIToolResults() int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	contents := make(map[string][]gjson.Result)
	duplicated := false
	for _, m := range all {
		if id := openAIToolResultID(m); id != "" {
			contents[id] = append(contents[id], m.Get("content"))
			duplicated = duplicated || len(contents[id]) > 1
		}
	}
	if !duplicated {
		return 0
	}

	out := make([]string, 0, len(all))
	seen := make(map[string]struct{}, len(contents))
	removed := 0
	for _, m := range all {
		id := openAIToolResultID(m)
		group := contents[id]
		if id == "" || len(group) < 2 {
			out = append(out, m.Raw)
			continue
		}
		if _, ok := seen[id]; ok {
			removed++
			continue
		}
		seen[id] = struct{}{}
		merged, err := sjson.SetRawBytes([]byte(m.Raw), "content", []byte(mergeToolResultContents(group)))
		if err != nil {
			return 0
		}
		out = append(out, string(merged))
	}
	body, err := sjson.SetRawBytes(e.body, "messages", []byte("["+strings.Join(out, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = body
	return removed
}

func openAIToolResultID(m gjson.Result) string {
	if m.Get("role").String() != "tool" {
		return ""
	}
	return m.Get("tool_call_id").String()
}

// anthropicToolResultGroup accumulates the tool_result blocks for one
// tool_use_id in history order.
type anthropicToolResultGroup struct {
	contents     []gjson.Result
	isError      bool
	cacheControl string
	hasCache     bool
}

func (e *RequestEnvelope) coalesceAnthropicToolResults() int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	groups := make(map[string]*anthropicToolResultGroup)
	duplicated := false
	for _, m := range all {
		if m.Get("role").String() != "user" {
			continue
		}
		m.Get("content").ForEach(func(_, block gjson.Result) bool {
			id := anthropicToolResultID(block)
			if id == "" {
				return true
			}
			g := groups[id]
			if g == nil {
				g = &anthropicToolResultGroup{}
				groups[id] = g
			}
			g.contents = append(g.contents, block.Get("content"))
			g.isError = g.isError || block.Get("is_error").Bool()
			if cc := block.Get("cache_control"); cc.Exists() && !g.hasCache {
				g.cacheControl, g.hasCache = cc.Raw, true
			}
			duplicated = duplicated || len(g.contents) > 1
			return true
		})
	}
	if !duplicated {
		return 0
	}

	out := make([]string, 0, len(all))
	seen := make(map[string]struct{}, len(groups))
	removed := 0
	for _, m := range all {
		content := m.Get("content")
		if m.Get("role").String() != "user" || !content.IsArray() {
			out = append(out, m.Raw)
			continue
		}
		changed := false
		var kept []string
		var mergeErr error
		content.ForEach(func(_, block gjson.Result) bool {
			id := anthropicToolResultID(block)
			g := groups[id]
			if id == "" || len(g.contents) < 2 {
				kept = append(kept, block.Raw)
				return true
			}
			changed = true
			if _, ok := seen[id]; ok {
				removed++
				return true
			}
			seen[id] = struct{}{}
			merged, err := mergeAnthropicToolResultBlock(block, g)
			if err != nil {
				mergeErr = err
				return false
			}
			kept = append(kept, merged)
			return true
		})
		if mergeErr != nil {
			return 0
		}
		if !changed {
			out = append(out, m.Raw)
			continue
		}
		if len(kept) == 0 {
			continue
		}
		rebuilt, err := sjson.SetRawBytes([]byte(m.Raw), "content", []byte("["+strings.Join(kept, ",")+"]"))
		if err != nil {
			return 0
		}
		out = append(out, string(rebuilt))
	}
	body, err := sjson.SetRawBytes(e.body, "messages", []byte("["+strings.Join(out, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = body
	return removed
}

func anthropicToolResultID(block gjson.Result) string {
	if block.Get("type").String() != "tool_result" {
		return ""
	}
	return block.Get("tool_use_id").String()
}

func mergeAnthropicToolResultBlock(first gjson.Result, g *anthropicToolResultGroup) (string, error) {
	out, err := sjson.SetRawBytes([]byte(first.Raw), "content", []byte(mergeToolResultContents(g.contents)))
	if err != nil {
		return "", err
	}
	if g.isError {
		if out, err = sjson.SetBytes(out, "is_error", true); err != nil {
			return "", err
		}
	}
	if g.hasCache && !first.Get("cache_control").Exists() {
		if out, err = sjson.SetRawBytes(out, "cache_control", []byte(g.cacheControl)); err != nil {
			return "", err
		}
	}
	return string(out), nil
}

func textPart(text string) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("text")
	jw.Key("text")
	jw.Str(text)
	jw.EndObj()
	return string(jw.Bytes())
}

// mergeToolResultContents returns the raw JSON for the concatenation of
// contents: a "\n\n"-joined string when every content is a string, otherwise
// a part array with strings promoted to text parts. Empty and missing
// contents are skipped.
func mergeToolResultContents(contents []gjson.Result) string {
	allStrings := true
	for _, c := range contents {
		if c.IsArray() {
			allStrings = false
			break
		}
	}
	if allStrings {
		texts := make([]string, 0, len(contents))
		for _, c := range contents {
			if c.Type == gjson.String && c.Str != "" {
				texts = append(texts, c.Str)
			}
		}
		jw := newJSONWriter()
		jw.Str(strings.Join(texts, "\n\n"))
		return string(jw.Bytes())
	}
	var parts []string
	for _, c := range contents {
		switch {
		case c.Type == gjson.String && c.Str != "":
			parts = append(parts, textPart(c.Str))
		case c.IsArray():
			c.ForEach(func(_, p gjson.Result) bool {
				parts = append(parts, p.Raw)
				return true
			})
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}
