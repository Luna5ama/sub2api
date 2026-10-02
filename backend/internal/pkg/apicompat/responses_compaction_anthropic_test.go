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
