package apicompat

import (
	"strings"
	"testing"
)

func TestAnthropicResponseToCodexCompactionResponseEmitsSingleItem(t *testing.T) {
	resp := &AnthropicResponse{
		ID: "msg_1",
		Content: []AnthropicContentBlock{
			{Type: "thinking", Thinking: "private reasoning"},
			{Type: "text", Text: "<summary>continue from here</summary>"},
		},
	}

	out := AnthropicResponseToCodexCompactionResponse(resp)
	if len(out.Output) != 1 || out.Output[0].Type != "compaction" {
		t.Fatalf("output = %#v", out.Output)
	}
	summary, ok := decodePlaintextCompactionSummary(out.Output[0].EncryptedContent)
	if !ok || !strings.Contains(summary, "continue from here") {
		t.Fatalf("summary = %q, ok=%v", summary, ok)
	}
}

func TestFinalizeAnthropicResponsesStreamCompactionOnlyEmitsSingleItem(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.CompactionOnly = true
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_start"}, state)
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_delta",
		Delta: &AnthropicDelta{
			Type: "text_delta",
			Text: "<summary>continue from here</summary>",
		},
	}, state)
	events := AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)
	var completed *ResponsesStreamEvent
	for _, event := range events {
		if event.Type == "response.completed" {
			completed = &event
		}
	}
	if completed == nil {
		t.Fatal("missing response.completed")
	}
	if len(completed.Response.Output) != 1 || completed.Response.Output[0].Type != "compaction" {
		t.Fatalf("output = %#v", completed.Response.Output)
	}
	summary, ok := decodePlaintextCompactionSummary(completed.Response.Output[0].EncryptedContent)
	if !ok || !strings.Contains(summary, "continue from here") {
		t.Fatalf("summary = %q, ok=%v", summary, ok)
	}
}

// A compaction turn must emit no visible reasoning events even when the
// upstream streams thinking deltas alongside its answer; Codex accepts only the
// synthesized compaction item.
func TestAnthropicCompactionOnlySuppressesThinkingDeltas(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.CompactionOnly = true
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:    "msg_1",
			Model: "claude-opus-5-5",
		},
	}, state)
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type:         "content_block_start",
		ContentBlock: &AnthropicContentBlock{Type: "thinking", Thinking: ""},
	}, state)
	events := AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_delta",
		Delta: &AnthropicDelta{
			Type:     "thinking_delta",
			Thinking: "secret chain of thought",
		},
	}, state)
	if len(events) != 0 {
		t.Fatalf("thinking delta leaked %#v", events)
	}
	events = AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
		Type: "content_block_delta",
		Delta: &AnthropicDelta{
			Type: "text_delta",
			Text: "<summary>continue</summary>",
		},
	}, state)
	if len(events) != 0 {
		t.Fatalf("text delta leaked %#v", events)
	}
	events = AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)
	for _, event := range events {
		if strings.Contains(event.Type, "delta") {
			t.Fatalf("unexpected delta event %q", event.Type)
		}
	}
}
