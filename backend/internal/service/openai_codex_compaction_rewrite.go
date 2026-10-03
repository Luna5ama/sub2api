package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex remote compaction v2 is a Codex-side protocol: the client posts
// /v1/responses with stream=true and a trailing {"type":"compaction_trigger"}
// input item, then insists on receiving exactly one output item of type
// "compaction" that carries the conversation summary.
//
// Genuine ChatGPT/Codex and api.openai.com upstreams implement that wire. A
// third-party OpenAI-compatible upstream that merely speaks the Responses API
// does not: it ignores the trigger and answers with an ordinary assistant turn,
// so Codex aborts with
//
//	remote compaction v2 expected exactly one compaction output item, got 0
//
// This file detects that situation, rewrites the completed upstream stream into
// a single compaction item, and converts the gateway's plaintext envelope back
// into a summary prompt on the following request.

// codexCompactionEnvelopePrefix mirrors the apicompat plaintext envelope. It is
// repeated here only to recognise gateway-generated summaries on the request
// side without exporting the codec.
const codexCompactionEnvelopePrefix = "sub2api-plaintext-v1:"

// openAICompactionItemType is the Codex remote-compaction result item type.
const openAICompactionItemType = "compaction"

// codexCompactionUpstreamHandlesNative reports whether the upstream behind this
// account answers a compaction_trigger itself and must therefore be relayed
// verbatim.
//
// Only genuine OpenAI upstreams qualify. Everything else either ignores the
// trigger or reaches compaction through a bridge that already synthesizes the
// item.
func codexCompactionUpstreamHandlesNative(account *Account) bool {
	if account == nil {
		return false
	}
	if account.Platform == PlatformGrok {
		// Grok compaction is converted by convertGrokResponseToOpenAICompact.
		return true
	}
	if account.IsOpenAIOAuthLike() {
		// ChatGPT/Codex backends implement remote compaction v2 natively.
		return true
	}
	if !account.IsOpenAIApiKey() {
		return true
	}
	return isOfficialOpenAIModelsBaseURL(account.GetOpenAIBaseURL())
}

// shouldRewriteCodexCompactionStream reports whether this request has to
// synthesize the remote-compaction v2 item from an ordinary upstream turn.
func shouldRewriteCodexCompactionStream(c *gin.Context, account *Account) bool {
	if !IsOpenAINativeCompactionV2(c) {
		return false
	}
	return !codexCompactionUpstreamHandlesNative(account)
}

// shouldRewriteCodexCompactionRequestForUpstream reports whether a request body
// may carry a gateway compaction summary that a non-native upstream cannot
// read.
//
// This gate is deliberately not tied to the request-scoped compaction v2
// marker: Codex replays the gateway plaintext compaction item on every later
// turn of the same conversation, and those turns carry no compaction_trigger.
// The rewrite itself is a no-op when nothing matches, so ordinary turns pass
// through unchanged.
func shouldRewriteCodexCompactionRequestForUpstream(account *Account) bool {
	if account == nil {
		return false
	}
	return !codexCompactionUpstreamHandlesNative(account)
}

// rewriteCodexReplayedCompactionSummariesForUpstream converts a gateway
// plaintext compaction item into the <conversation_summary> message a
// non-native upstream can read, on turns that are not themselves compaction
// requests.
//
// Only the gateway envelope is touched. A compaction item carrying
// upstream-native encrypted content is left exactly as it is: the retry path
// that strips a rejected invalid_encrypted_content item owns that decision, and
// dropping it here would pre-empt that recovery.
func rewriteCodexReplayedCompactionSummariesForUpstream(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	input := parseRawJSONView(body).Get("input")
	if !input.IsArray() || len(input.Array()) == 0 {
		return body, false, nil
	}

	changed := false
	rebuilt := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		if !item.IsObject() {
			rebuilt = append(rebuilt, item.Raw)
			continue
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case openAICompactionItemType, "compaction_summary":
			summary, decoded := codexCompactionSummaryFromPlaintextEnvelope(item.Get("encrypted_content").String())
			if !decoded || strings.TrimSpace(summary) == "" {
				rebuilt = append(rebuilt, item.Raw)
				continue
			}
			changed = true
			rebuilt = append(rebuilt, codexCompactionSummaryInputItem(summary))
		default:
			rebuilt = append(rebuilt, item.Raw)
		}
	}
	if !changed {
		return body, false, nil
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return body, false, err
	}
	return updated, true, nil
}

// codexCompactionSummaryItem builds the terminal compaction item from collected
// summary text. The text travels in the same plaintext envelope the bridged
// paths use, so the next request can turn it back into a summary prompt.
func codexCompactionSummaryItem(summary string) ([]byte, bool) {
	encoded := codexCompactionEnvelopePrefix + base64.StdEncoding.EncodeToString([]byte(summary))
	item, err := sjson.SetBytes([]byte(`{"type":"compaction"}`), "encrypted_content", encoded)
	if err != nil {
		return nil, false
	}
	item, err = sjson.SetBytes(item, "id", "cmp_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	if err != nil {
		return nil, false
	}
	item, err = sjson.SetBytes(item, "status", "completed")
	if err != nil {
		return nil, false
	}
	return item, true
}

// codexCompactionSummaryFromPlaintextEnvelope decodes the gateway-generated
// plaintext summary envelope. It reports false for upstream-native encrypted
// content, which only the upstream itself can read.
func codexCompactionSummaryFromPlaintextEnvelope(encrypted string) (string, bool) {
	encrypted = strings.TrimSpace(encrypted)
	if !strings.HasPrefix(encrypted, codexCompactionEnvelopePrefix) {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encrypted, codexCompactionEnvelopePrefix))
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// rewriteCodexCompactionTriggerForUpstream converts a native compaction v2 body
// into the shape a non-native upstream understands.
//
// Such an upstream ignores compaction_trigger and cannot decrypt the gateway's
// plaintext compaction items, so the trigger becomes the same summarisation
// instruction the bridged paths use, and plaintext compaction items in the
// replayed history become a <conversation_summary> user message. History items
// the gateway cannot decode (upstream-native encrypted content) are dropped
// rather than forwarded as an unknown item type.
func rewriteCodexCompactionTriggerForUpstream(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	input := parseRawJSONView(body).Get("input")
	if !input.IsArray() || len(input.Array()) == 0 {
		return body, false, nil
	}

	changed := false
	rebuilt := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		if !item.IsObject() {
			rebuilt = append(rebuilt, item.Raw)
			continue
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case "compaction_trigger":
			changed = true
			rebuilt = append(rebuilt, codexCompactionPromptInputItem())
		case openAICompactionItemType, "compaction_summary":
			changed = true
			summary, decoded := codexCompactionSummaryFromPlaintextEnvelope(item.Get("encrypted_content").String())
			if !decoded || strings.TrimSpace(summary) == "" {
				continue
			}
			rebuilt = append(rebuilt, codexCompactionSummaryInputItem(summary))
		default:
			rebuilt = append(rebuilt, item.Raw)
		}
	}
	if !changed {
		return body, false, nil
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return body, false, err
	}
	return updated, true, nil
}

func codexCompactionPromptInputItem() string {
	item, err := sjson.SetBytes([]byte(`{"type":"message","role":"user"}`), "content.0.type", "input_text")
	if err != nil {
		return ""
	}
	item, err = sjson.SetBytes(item, "content.0.text", apicompat.CodexCompactionSummaryPrompt())
	if err != nil {
		return ""
	}
	return string(item)
}

func codexCompactionSummaryInputItem(summary string) string {
	item, err := sjson.SetBytes([]byte(`{"type":"message","role":"user"}`), "content.0.type", "input_text")
	if err != nil {
		return ""
	}
	item, err = sjson.SetBytes(item, "content.0.text", "<conversation_summary>\n"+summary+"\n</conversation_summary>")
	if err != nil {
		return ""
	}
	return string(item)
}

// planCodexCompactionStreamRewrite inspects a completed upstream Responses SSE
// body and decides whether it must be replaced by a synthesized compaction
// stream.
//
// The rewrite applies only to a clean, successful turn that did not already
// carry a compaction item. Failures, empty bodies and upstream-native
// compaction are left untouched so the regular relay keeps ownership of
// failover, error passthrough and usage accounting.
func planCodexCompactionStreamRewrite(s *OpenAIGatewayService, body []byte) (payload []byte, responseID string, usage *OpenAIUsage, ok bool) {
	bodyText := string(body)
	if strings.TrimSpace(bodyText) == "" {
		return nil, "", nil, false
	}
	sawCompaction := false
	sawTerminal := false
	sawFailure := false
	forEachOpenAISSEFrame(bodyText, func(eventType string, data []byte) {
		switch eventType {
		case "response.failed", "error":
			sawFailure = true
		case "response.output_item.done", "response.output_item.added":
			if isResponsesCompactionItemType(gjson.GetBytes(data, "item.type").String()) {
				sawCompaction = true
			}
		case "response.completed", "response.done":
			sawTerminal = true
			for _, item := range gjson.GetBytes(data, "response.output").Array() {
				if isResponsesCompactionItemType(item.Get("type").String()) {
					sawCompaction = true
				}
			}
		}
	})
	if sawFailure || !sawTerminal || sawCompaction {
		return nil, "", nil, false
	}

	finalResponse, found := extractCodexFinalResponse(bodyText)
	if !found {
		return nil, "", nil, false
	}
	item, built := codexCompactionSummaryItem(codexCompactionSummaryText(finalResponse, bodyText))
	if !built {
		return nil, "", nil, false
	}
	response, err := sjson.SetRawBytes(finalResponse, "output", []byte("["+string(item)+"]"))
	if err != nil {
		return nil, "", nil, false
	}
	stream, built := buildOpenAICompactSSEPayload(response)
	if !built {
		return nil, "", nil, false
	}
	usage = &OpenAIUsage{}
	if s != nil {
		usage = s.parseSSEUsageFromBody(bodyText)
	}
	return stream, strings.TrimSpace(gjson.GetBytes(finalResponse, "id").String()), usage, true
}

// codexCompactionSummaryText collects the summary an ordinary upstream produced
// for the compaction turn. Assistant message text is authoritative; reasoning
// summaries are used only when the upstream emitted no assistant text at all.
func codexCompactionSummaryText(finalResponse []byte, bodyText string) string {
	var messageText, reasoningText strings.Builder
	for _, item := range gjson.GetBytes(finalResponse, "output").Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "message":
			for _, part := range item.Get("content").Array() {
				_, _ = messageText.WriteString(part.Get("text").String())
			}
		case "reasoning":
			for _, part := range item.Get("summary").Array() {
				_, _ = reasoningText.WriteString(part.Get("text").String())
			}
		}
	}
	if summary := strings.TrimSpace(messageText.String()); summary != "" {
		return summary
	}
	// Compatible upstreams stream reasoning summaries as deltas that never
	// reach the terminal output array.
	_, _ = reasoningText.WriteString(codexCompactionReasoningDeltas(bodyText))
	if summary := strings.TrimSpace(reasoningText.String()); summary != "" {
		return summary
	}
	return codexCompactionMessageDeltas(bodyText)
}

func codexCompactionReasoningDeltas(bodyText string) string {
	var sb strings.Builder
	forEachOpenAISSEFrame(bodyText, func(eventType string, data []byte) {
		switch eventType {
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			_, _ = sb.WriteString(gjson.GetBytes(data, "delta").String())
		}
	})
	return sb.String()
}

func codexCompactionMessageDeltas(bodyText string) string {
	var sb strings.Builder
	forEachOpenAISSEFrame(bodyText, func(eventType string, data []byte) {
		if eventType == "response.output_text.delta" {
			_, _ = sb.WriteString(gjson.GetBytes(data, "delta").String())
		}
	})
	return sb.String()
}

// handleCodexCompactionStreamRewrite buffers one non-native upstream compaction
// turn and, when the upstream ignored the trigger, writes the single-item
// compaction stream Codex requires.
//
// handled=false means the upstream turn already satisfies the protocol or is a
// failure: the body is restored untouched and the regular relay keeps
// ownership of failover, error passthrough and usage accounting.
func (s *OpenAIGatewayService) handleCodexCompactionStreamRewrite(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	startTime time.Time,
	originalModel string,
) (*openaiStreamingResult, error, bool) {
	if resp == nil || resp.Body == nil {
		return nil, nil, false
	}
	payload, responseID, usage, elapsed, ok, err := s.bufferCodexCompactionStreamRewrite(resp, c, account, startTime, originalModel)
	if !ok || err != nil {
		return nil, err, ok
	}
	if _, writeErr := c.Writer.Write(payload); writeErr != nil {
		return nil, writeErr, true
	}
	c.Writer.Flush()
	return &openaiStreamingResult{usage: usage, firstTokenMs: &elapsed, responseID: responseID}, nil, true
}

// bufferCodexCompactionStreamRewrite reads one non-native upstream compaction
// turn and, when the upstream ignored the trigger, prepares the single-item
// compaction stream Codex requires. It writes the response headers only once a
// rewrite is certain, so handled=false leaves the response untouched for the
// regular relay.
//
// The buffered body is restored whenever the stream is not rewritten, which
// lets both the standard relay and the passthrough relay keep ownership of
// failover, error passthrough and usage accounting for every other case.
func (s *OpenAIGatewayService) bufferCodexCompactionStreamRewrite(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	startTime time.Time,
	originalModel string,
) (payload []byte, responseID string, usage *OpenAIUsage, elapsedMs int, ok bool, err error) {
	if resp == nil || resp.Body == nil {
		return nil, "", nil, 0, false, nil
	}
	// A compaction turn is one long upstream read with no bytes to send the
	// client until the summary completes, so keep the downstream connection
	// warm while the body is buffered.
	stopKeepalive := func() {}
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		stopKeepalive = startOpenAISSEKeepalive(c,
			time.Duration(s.cfg.Gateway.StreamKeepaliveInterval)*time.Second)
	}
	defer stopKeepalive()

	body, readErr := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if readErr != nil {
		return nil, "", nil, 0, true, readErr
	}
	payload, responseID, usage, planned := planCodexCompactionStreamRewrite(s, body)
	if !planned {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil, "", nil, 0, false, nil
	}

	s.relayOpenAICodexTurnState(c, account, resp.Header)
	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	} else if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
		c.Header("x-request-id", requestID)
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	MarkResponseCommitted(c)

	elapsed := int(time.Since(startTime).Milliseconds())
	logger.LegacyPrintf("service.openai_gateway",
		"[OpenAI] Synthesized Codex remote compaction v2 item for non-native upstream (account: %s, model: %s)",
		accountName(account), originalModel)
	return payload, responseID, usage, elapsed, true, nil
}

func accountName(account *Account) string {
	if account == nil {
		return ""
	}
	return account.Name
}
