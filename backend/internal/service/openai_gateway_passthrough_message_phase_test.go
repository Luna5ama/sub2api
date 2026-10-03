package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Native /responses upstreams announce every interim assistant message with
// phase "final_answer" and only correct it to "commentary" in the matching
// response.output_item.done. Codex treats a streaming message tagged
// "final_answer" as the turn's terminal answer, so an interim preamble
// collapses the work log until the next item reopens it: the turn appears to
// finish and then resume. An added item is still in progress, so its phase is a
// guess and the done event owns the authoritative value.
func TestNormalizeOpenAIResponsesStreamingMessagePhase(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		want        string
		wantChanged bool
	}{
		{
			name:        "premature final answer on streaming assistant message is deferred to done",
			input:       `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[],"status":"in_progress","id":"msg_1","phase":"final_answer"}}`,
			want:        `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[],"status":"in_progress","id":"msg_1"}}`,
			wantChanged: true,
		},
		{
			name:        "commentary phase on added is already correct",
			input:       `{"type":"response.output_item.added","item":{"type":"message","role":"assistant","status":"in_progress","id":"msg_1","phase":"commentary"}}`,
			want:        `{"type":"response.output_item.added","item":{"type":"message","role":"assistant","status":"in_progress","id":"msg_1","phase":"commentary"}}`,
			wantChanged: false,
		},
		{
			name:        "done event keeps its authoritative phase",
			input:       `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"commentary"}}`,
			want:        `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"commentary"}}`,
			wantChanged: false,
		},
		{
			name:        "genuine final answer survives on done",
			input:       `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"final_answer"}}`,
			want:        `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"final_answer"}}`,
			wantChanged: false,
		},
		{
			name:        "terminal response output keeps its phase",
			input:       `{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"final_answer"}]}}`,
			want:        `{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"final_answer"}]}}`,
			wantChanged: false,
		},
		{
			name:        "non-assistant and non-message added items are untouched",
			input:       `{"type":"response.output_item.added","item":{"type":"function_call","name":"shell","status":"in_progress","id":"fc_1"}}`,
			want:        `{"type":"response.output_item.added","item":{"type":"function_call","name":"shell","status":"in_progress","id":"fc_1"}}`,
			wantChanged: false,
		},
		{
			name:        "completed status on an added event is left alone",
			input:       `{"type":"response.output_item.added","item":{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"final_answer"}}`,
			want:        `{"type":"response.output_item.added","item":{"type":"message","role":"assistant","status":"completed","id":"msg_1","phase":"final_answer"}}`,
			wantChanged: false,
		},
		{
			name:        "invalid json is passed through",
			input:       `{"type":"response.output_item.added","item":`,
			want:        `{"type":"response.output_item.added","item":`,
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := normalizeOpenAIResponsesStreamingMessagePhase([]byte(tt.input))

			require.Equal(t, tt.wantChanged, changed)
			require.Equal(t, tt.want, string(got))
		})
	}
}

// The client only ever sees the normalized stream, so assert on what the
// passthrough handler actually writes: the interim preamble must be announced
// without a phase and its done event must still report commentary, while the
// real final answer keeps final_answer from start to finish.
func TestHandleStreamingResponsePassthroughDefersStreamingMessagePhase(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamBody := strings.Join([]string{
		passthroughSSEData(`{"type":"response.created","response":{"id":"resp_phase","model":"deepseek-v4.1-flash"}}`),
		passthroughSSEData(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[],"status":"in_progress","id":"msg_interim","phase":"final_answer"}}`),
		passthroughSSEData(`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_interim","content_index":0,"delta":"I'll run alpha."}`),
		passthroughSSEData(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll run alpha."}],"status":"completed","id":"msg_interim","phase":"commentary"}}`),
		passthroughSSEData(`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[],"status":"in_progress","id":"msg_final","phase":"final_answer"}}`),
		passthroughSSEData(`{"type":"response.output_text.delta","output_index":1,"item_id":"msg_final","content_index":0,"delta":"done"}`),
		passthroughSSEData(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}],"status":"completed","id":"msg_final","phase":"final_answer"}}`),
		passthroughSSEData(`{"type":"response.completed","response":{"id":"resp_phase","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll run alpha."}],"status":"completed","id":"msg_interim","phase":"commentary"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}],"status":"completed","id":"msg_final","phase":"final_answer"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`),
		"data: [DONE]\n\n",
	}, "")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}

	svc := &OpenAIGatewayService{}
	result, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "deepseek-v4.1-flash", "deepseek-v4.1-flash")
	require.NoError(t, err)
	require.NotNil(t, result)

	events := collectSSEDataPayloads(t, rec.Body.String())

	interimAdded := findSSEEvent(t, events, "response.output_item.added", "")
	require.Equal(t, "msg_interim", gjson.Get(interimAdded, "item.id").String())
	require.False(t, gjson.Get(interimAdded, "item.phase").Exists(),
		"an in-progress interim message must not claim to be the final answer")
	require.Equal(t, "in_progress", gjson.Get(interimAdded, "item.status").String())

	interimDone := ""
	finalAdded := ""
	finalDone := ""
	for _, event := range events {
		switch gjson.Get(event, "type").String() {
		case "response.output_item.done":
			switch gjson.Get(event, "item.id").String() {
			case "msg_interim":
				interimDone = event
			case "msg_final":
				finalDone = event
			}
		case "response.output_item.added":
			if gjson.Get(event, "item.id").String() == "msg_final" {
				finalAdded = event
			}
		}
	}
	require.NotEmpty(t, interimDone, "interim output_item.done missing")
	require.Equal(t, "commentary", gjson.Get(interimDone, "item.phase").String(),
		"the done event carries the authoritative phase and must survive")

	require.NotEmpty(t, finalAdded, "final output_item.added missing")
	require.False(t, gjson.Get(finalAdded, "item.phase").Exists(),
		"a streaming message never knows its phase yet, even the last one")
	require.NotEmpty(t, finalDone, "final output_item.done missing")
	require.Equal(t, "final_answer", gjson.Get(finalDone, "item.phase").String(),
		"the terminal phase must still reach the client on the done item")

	completed := findSSEEvent(t, events, "response.completed", "")
	require.Equal(t, "commentary", gjson.Get(completed, "response.output.0.phase").String())
	require.Equal(t, "final_answer", gjson.Get(completed, "response.output.1.phase").String())
}

// Codex reaches the gateway through the Responses WebSocket v2 relay, which
// writes client frames from its own adapter, so the deferral has to hold on
// that path too: the client must never receive a streaming message that claims
// to be the turn's final answer while the turn is still running.
func TestPassthroughWebSocketDefersStreamingMessagePhase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	server, serverErr := startPassthroughLifecycleServer(
		t,
		controlCtx,
		newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream),
		passthroughLifecycleAccount(),
	)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())

	upstream.Send(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[],"status":"in_progress","id":"msg_interim","phase":"final_answer"}}`)
	interimAdded, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.output_item.added", gjson.GetBytes(interimAdded, "type").String())
	require.Equal(t, "msg_interim", gjson.GetBytes(interimAdded, "item.id").String())
	require.False(t, gjson.GetBytes(interimAdded, "item.phase").Exists(),
		"an in-progress interim message must not claim to be the final answer over the websocket relay")

	upstream.Send(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll run alpha."}],"status":"completed","id":"msg_interim","phase":"commentary"}}`)
	interimDone, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.output_item.done", gjson.GetBytes(interimDone, "type").String())
	require.Equal(t, "commentary", gjson.GetBytes(interimDone, "item.phase").String(),
		"the done event carries the authoritative phase and must survive")

	upstream.Send(`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[],"status":"in_progress","id":"msg_final","phase":"final_answer"}}`)
	upstream.Send(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}],"status":"completed","id":"msg_final","phase":"final_answer"}}`)
	finalAdded, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "msg_final", gjson.GetBytes(finalAdded, "item.id").String())
	require.False(t, gjson.GetBytes(finalAdded, "item.phase").Exists(),
		"a streaming message never knows its phase yet, even the last one")

	finalDone, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.output_item.done", gjson.GetBytes(finalDone, "type").String())
	require.Equal(t, "final_answer", gjson.GetBytes(finalDone, "item.phase").String(),
		"the terminal phase must still reach the client on the done item")

	upstream.Send(`{"type":"response.completed","response":{"id":"resp_phase_ws","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	completed, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough message phase test did not exit")
	}
}
