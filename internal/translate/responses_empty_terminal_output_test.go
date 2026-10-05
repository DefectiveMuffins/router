package translate_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Synthetic stream: the answer arrives only as streamed output items and the
// terminal response.completed carries an empty output list, the shape an
// always-streaming upstream can produce for a buffered (non-streaming) client.
const responsesEmptyTerminalOutputFixture = `event: response.created
data: {"type":"response.created","response":{"id":"resp_empty","status":"in_progress","model":"gpt-6.1-sol","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"The agent read the file."}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"The agent read the file.","annotations":[]}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_empty","status":"completed","model":"gpt-6.1-sol","output":[],"usage":{"input_tokens":120,"output_tokens":60}}}

`

func TestResponsesToAnthropicWriter_NonStreamingRecoversStreamedOutputWhenTerminalListIsEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToAnthropicWriter(rec, "claude-haiku-4-5", nil)

	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(responsesEmptyTerminalOutputFixture))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	require.Equal(t, 200, rec.Code, rec.Body.String())
	var msg map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &msg))
	assert.Equal(t, "The agent read the file.", gjson.GetBytes(rec.Body.Bytes(), `content.#(type=="text").text`).String())
	assert.Equal(t, "end_turn", msg["stop_reason"])
}

func TestResponsesToOpenAIChatWriter_NonStreamingRecoversStreamedOutputWhenTerminalListIsEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-6.1-sol", nil)

	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(responsesEmptyTerminalOutputFixture))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Equal(t, "The agent read the file.", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
}
