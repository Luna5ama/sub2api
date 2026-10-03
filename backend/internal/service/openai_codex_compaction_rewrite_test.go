//go:build unit

package service

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// codexCompactionSummaryItem and its decoder are the wire contract Codex relies
// on across turns: whatever the gateway emits has to come back readable.
func TestCodexCompactionSummaryItem_RoundTripsPlaintextEnvelope(t *testing.T) {
	t.Parallel()

	item, ok := codexCompactionSummaryItem("carry the work forward")
	require.True(t, ok)
	require.Equal(t, "compaction", gjson.GetBytes(item, "type").String())
	require.Equal(t, "completed", gjson.GetBytes(item, "status").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(item, "id").String(), "cmp_"))

	encrypted := gjson.GetBytes(item, "encrypted_content").String()
	require.True(t, strings.HasPrefix(encrypted, codexCompactionEnvelopePrefix))

	summary, decoded := codexCompactionSummaryFromPlaintextEnvelope(encrypted)
	require.True(t, decoded)
	require.Equal(t, "carry the work forward", summary)
}

func TestCodexCompactionSummaryFromPlaintextEnvelope_RejectsUpstreamNative(t *testing.T) {
	t.Parallel()

	_, decoded := codexCompactionSummaryFromPlaintextEnvelope("Zm9yZWlnbg==")
	require.False(t, decoded)

	// A valid prefix with a corrupt body is also rejected rather than half-read.
	_, decoded = codexCompactionSummaryFromPlaintextEnvelope(codexCompactionEnvelopePrefix + "!!!not-base64")
	require.False(t, decoded)
}

// The trigger turns into the same summarisation instruction the bridged paths
// append, so the non-native upstream is actually asked to summarise.
func TestRewriteCodexCompactionTrigger_ReplacesTriggerWithSummaryPrompt(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":"hi"},{"type":"compaction_trigger"}]}`)
	out, changed, err := rewriteCodexCompactionTriggerForUpstream(body)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(out, "input")
	require.Len(t, input.Array(), 2)
	require.Equal(t, "hi", input.Array()[0].Get("content").String())
	require.NotContains(t, string(out), "compaction_trigger")
	require.Equal(t, "user", input.Array()[1].Get("role").String())
	require.Equal(t, "input_text", input.Array()[1].Get("content.0.type").String())
	require.NotEmpty(t, input.Array()[1].Get("content.0.text").String())
}

// The compaction turn rewrites both the trigger and any replayed summary it
// carries: the trigger becomes the summarisation instruction and the plaintext
// item becomes a <conversation_summary> message.
func TestRewriteCodexCompactionTrigger_ConvertsSummaryAlongsideTrigger(t *testing.T) {
	t.Parallel()

	item, ok := codexCompactionSummaryItem("earlier context")
	require.True(t, ok)

	body := []byte(`{"model":"m","input":[` + string(item) + `,{"type":"message","role":"user","content":"next"},{"type":"compaction_trigger"}]}`)
	out, changed, err := rewriteCodexCompactionTriggerForUpstream(body)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(out, "input")
	require.Len(t, input.Array(), 3)
	require.NotContains(t, string(out), `"type":"compaction"`)
	require.Equal(t,
		"<conversation_summary>\nearlier context\n</conversation_summary>",
		input.Array()[0].Get("content.0.text").String(),
	)
	require.Equal(t, "next", input.Array()[1].Get("content").String())
	require.Contains(t, input.Array()[2].Get("content.0.text").String(), "<summary>")
}

// A non-compaction turn replays the gateway summary without a trigger. It still
// has to become a <conversation_summary> message, or the upstream sees an item
// type it cannot read. This is the regression the trigger-scoped gate caused.
func TestRewriteCodexReplayedCompactionSummaries_ConvertsWithoutTrigger(t *testing.T) {
	t.Parallel()

	item, ok := codexCompactionSummaryItem("earlier context")
	require.True(t, ok)

	body := []byte(`{"model":"m","input":[` + string(item) + `,{"type":"message","role":"user","content":"next"}]}`)
	out, changed, err := rewriteCodexReplayedCompactionSummariesForUpstream(body)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(out, "input")
	require.Len(t, input.Array(), 2)
	require.NotContains(t, string(out), `"type":"compaction"`)
	require.Equal(t,
		"<conversation_summary>\nearlier context\n</conversation_summary>",
		input.Array()[0].Get("content.0.text").String(),
	)
	require.Equal(t, "next", input.Array()[1].Get("content").String())
}

// Upstream-native encrypted content is left alone: the invalid_encrypted_content
// retry path owns stripping it, and dropping it here would pre-empt recovery.
func TestRewriteCodexReplayedCompactionSummaries_KeepsForeignEncryptedItem(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","input":[{"type":"compaction","encrypted_content":"Zm9yZWlnbg=="},{"type":"message","role":"user","content":"next"}]}`)
	out, changed, err := rewriteCodexReplayedCompactionSummariesForUpstream(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestRewriteCodexReplayedCompactionSummaries_NoopOnOrdinaryTurn(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, changed, err := rewriteCodexReplayedCompactionSummariesForUpstream(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestRewriteCodexCompactionTrigger_NoopOnOrdinaryTurn(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, changed, err := rewriteCodexCompactionTriggerForUpstream(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestRewriteCodexCompactionTrigger_NoopWithoutArrayInput(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","input":"hi"}`)
	out, changed, err := rewriteCodexCompactionTriggerForUpstream(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestShouldRewriteCodexCompactionRequestForUpstream(t *testing.T) {
	t.Parallel()

	require.False(t, shouldRewriteCodexCompactionRequestForUpstream(nil))
	require.False(t, shouldRewriteCodexCompactionRequestForUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeOAuth,
	}))
	require.False(t, shouldRewriteCodexCompactionRequestForUpstream(&Account{
		Platform: PlatformGrok, Type: AccountTypeAPIKey,
	}))
	require.False(t, shouldRewriteCodexCompactionRequestForUpstream(&Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.openai.com/v1"},
	}))
	require.True(t, shouldRewriteCodexCompactionRequestForUpstream(&Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://vibe.mic.run"},
	}))
}

// A clean non-native turn that ignored the trigger is rewritten into exactly
// one compaction item; the ordinary reasoning/message items must disappear so
// Codex sees one item rather than "0 from 2".
func TestPlanCodexCompactionStreamRewrite_SynthesizesSingleItem(t *testing.T) {
	t.Parallel()

	body := codexCompactionOrdinaryUpstreamStream()
	payload, responseID, usage, ok := planCodexCompactionStreamRewrite(&OpenAIGatewayService{}, []byte(body))
	require.True(t, ok)
	require.Equal(t, "resp_compact_1", responseID)
	require.NotNil(t, usage)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)

	var doneItems []gjson.Result
	var completedOutput []gjson.Result
	forEachOpenAISSEFrame(string(payload), func(eventType string, data []byte) {
		switch eventType {
		case "response.output_item.done":
			doneItems = append(doneItems, gjson.GetBytes(data, "item"))
		case "response.completed":
			completedOutput = gjson.GetBytes(data, "response.output").Array()
		}
	})

	require.Len(t, doneItems, 1)
	require.Equal(t, "compaction", doneItems[0].Get("type").String())
	require.NotContains(t, string(payload), `"type":"message"`)
	require.NotContains(t, string(payload), `"type":"reasoning"`)

	summary, decoded := codexCompactionSummaryFromPlaintextEnvelope(doneItems[0].Get("encrypted_content").String())
	require.True(t, decoded)
	require.Equal(t, "summary of the session", summary)

	// The terminal event has to agree with the emitted item.
	require.Len(t, completedOutput, 1)
	require.Equal(t, "compaction", completedOutput[0].Get("type").String())
	require.Equal(t,
		doneItems[0].Get("encrypted_content").String(),
		completedOutput[0].Get("encrypted_content").String(),
	)
}

// Reasoning-only turns still have to produce a usable summary (some compatible
// upstreams stream reasoning summaries as deltas and never populate output[]).
func TestPlanCodexCompactionStreamRewrite_FallsBackToReasoningDeltas(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"from reasoning"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_r","output":[{"id":"rs_1","type":"reasoning","summary":[]}]}}`,
		``,
	}, "\n")

	payload, _, _, ok := planCodexCompactionStreamRewrite(&OpenAIGatewayService{}, []byte(body))
	require.True(t, ok)

	var item gjson.Result
	forEachOpenAISSEFrame(string(payload), func(eventType string, data []byte) {
		if eventType == "response.output_item.done" {
			item = gjson.GetBytes(data, "item")
		}
	})
	summary, decoded := codexCompactionSummaryFromPlaintextEnvelope(item.Get("encrypted_content").String())
	require.True(t, decoded)
	require.Equal(t, "from reasoning", summary)
}

func TestPlanCodexCompactionStreamRewrite_LeavesAlreadyCompactedStream(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction","status":"completed","encrypted_content":"AAAA"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"cmp_1","type":"compaction","status":"completed","encrypted_content":"AAAA"}]}}`,
		``,
	}, "\n")

	_, _, _, ok := planCodexCompactionStreamRewrite(&OpenAIGatewayService{}, []byte(body))
	require.False(t, ok, "an upstream that already produced a compaction item must be relayed verbatim")
}

func TestPlanCodexCompactionStreamRewrite_LeavesFailuresAndEmptyBodies(t *testing.T) {
	t.Parallel()

	failed := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","output":[]}}`,
		``,
	}, "\n")
	_, _, _, ok := planCodexCompactionStreamRewrite(&OpenAIGatewayService{}, []byte(failed))
	require.False(t, ok, "failure streams keep their own failover/passthrough handling")

	for _, body := range []string{"", "   ", `event: response.created` + "\n" + `data: {"type":"response.created"}` + "\n\n"} {
		_, _, _, ok := planCodexCompactionStreamRewrite(&OpenAIGatewayService{}, []byte(body))
		require.False(t, ok)
	}
}

// End to end through the relay hook: the client must receive a well-formed
// single-item compaction stream and the result must carry upstream usage.
func TestHandleCodexCompactionStreamRewrite_EmitsCompactionStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(codexCompactionOrdinaryUpstreamStream())),
	}
	resp.Header = http.Header{}
	resp.Header.Set("x-request-id", "rid_compact")

	svc := &OpenAIGatewayService{}
	result, err, handled := svc.handleCodexCompactionStreamRewrite(
		nil, resp, c, &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Name: "cmd-code"},
		time.Now(), "deepseek-v4.1-flash",
	)
	require.True(t, handled)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 11, result.usage.InputTokens)
	require.Equal(t, "resp_compact_1", result.responseID)

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "rid_compact", rec.Header().Get("x-request-id"))

	out := rec.Body.String()
	require.Contains(t, out, `"type":"compaction"`)
	require.NotContains(t, out, `"type":"message"`)
	require.NotContains(t, out, `"type":"reasoning"`)
	require.Equal(t, 1, strings.Count(out, "event: response.output_item.done"))
}

// handled=false is the contract that lets the regular relay keep ownership of
// failover and error passthrough. The buffered body must be restored so the
// relay can still read it.
func TestHandleCodexCompactionStreamRewrite_PassesThroughFailureTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	body := `event: response.failed` + "\n" +
		`data: {"type":"response.failed","response":{"id":"resp_x","output":[]}}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	svc := &OpenAIGatewayService{}
	result, err, handled := svc.handleCodexCompactionStreamRewrite(
		nil, resp, c, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, time.Now(), "m",
	)
	require.False(t, handled)
	require.Nil(t, result)
	require.NoError(t, err)

	// Body is restored for the regular relay to re-read.
	restored, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Equal(t, body, string(restored))
}

func codexCompactionOrdinaryUpstreamStream() string {
	return strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_compact_1","output":[]}}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"internal deliberation"}]}}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"summary of the session"}]}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_compact_1","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"internal deliberation"}]},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"summary of the session"}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}`,
		``,
	}, "\n")
}

// Guard against accidental drift in the envelope prefix this module mirrors from
// apicompat; both sides must agree or cross-turn replay silently breaks.
func TestCodexCompactionEnvelopePrefix_MatchesBase64RoundTrip(t *testing.T) {
	t.Parallel()

	encoded := codexCompactionEnvelopePrefix + base64.StdEncoding.EncodeToString([]byte("x"))
	summary, ok := codexCompactionSummaryFromPlaintextEnvelope(encoded)
	require.True(t, ok)
	require.Equal(t, "x", summary)
	require.Equal(t, "sub2api-plaintext-v1:", codexCompactionEnvelopePrefix)
}
