package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// anthropicMessageText flattens a converted Anthropic message back to plain
// text so assertions match the unescaped prompt text.
func anthropicMessageText(msg AnthropicMessage) string {
	var b strings.Builder
	for _, block := range parseContentBlocks(msg.Content) {
		if block.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func TestResponsesToChatCompletionsRequest_CompactionTriggerBecomesSummaryTask(t *testing.T) {
	req := &ResponsesRequest{
		Model: "glm-5.3",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":"Continue debugging"},
			{"type":"compaction_trigger"}
		]`),
	}

	chatReq, err := ResponsesToChatCompletionsRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	last := chatReq.Messages[len(chatReq.Messages)-1]
	if last.Role != "user" || !strings.Contains(string(last.Content), "CONTEXT CHECKPOINT COMPACTION") {
		t.Fatalf("last message = %s %s", last.Role, last.Content)
	}
}

func TestFinalizeChatCompletionsResponsesStream_CompactionOnlyEmitsOneCompactionItem(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState("glm-5.3")
	state.CompactionOnly = true
	state.MessageItemID = "msg_1"
	state.Text.WriteString("<summary>continue debugging</summary>")

	events := FinalizeChatCompletionsResponsesStream(state)
	var completed *ResponsesStreamEvent
	for _, event := range events {
		if event.Type != "response.completed" {
			continue
		}
		completed = &event
	}
	if completed == nil {
		t.Fatal("missing response.completed")
	}
	if len(completed.Response.Output) != 1 || completed.Response.Output[0].Type != "compaction" {
		t.Fatalf("output = %#v", completed.Response.Output)
	}
	summary, ok := decodePlaintextCompactionSummary(completed.Response.Output[0].EncryptedContent)
	if !ok || !strings.Contains(summary, "continue debugging") {
		t.Fatalf("summary = %q, ok=%v", summary, ok)
	}
}

func TestResponsesToAnthropicRequest_PlaintextCompactionRoundTrip(t *testing.T) {
	summary := encodePlaintextCompactionSummary("carry this forward")

	t.Run("non_claude_uses_codex_handoff", func(t *testing.T) {
		anthReq, err := ResponsesToAnthropicRequest(&ResponsesRequest{
			Model: "glm-5.3",
			Input: json.RawMessage(`[
				{"type":"compaction","encrypted_content":"` + summary + `"},
				{"type":"compaction_trigger"}
			]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		var foundSummary, foundPrompt bool
		for _, msg := range anthReq.Messages {
			text := anthropicMessageText(msg)
			if strings.Contains(text, CodexCompactionHandoffPrefix()) && strings.Contains(text, "carry this forward") {
				foundSummary = true
			}
			if strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
				foundPrompt = true
			}
			if strings.Contains(text, "<conversation_summary>") {
				t.Fatalf("non-Claude path must not use the Claude wrapper: %s", text)
			}
		}
		if !foundSummary || !foundPrompt {
			t.Fatalf("summary=%v prompt=%v messages=%#v", foundSummary, foundPrompt, anthReq.Messages)
		}
	})

	t.Run("claude_keeps_claude_code_shape", func(t *testing.T) {
		anthReq, err := ResponsesToAnthropicRequest(&ResponsesRequest{
			Model: "claude-opus-5-5",
			Input: json.RawMessage(`[
				{"type":"compaction","encrypted_content":"` + summary + `"},
				{"type":"compaction_trigger"}
			]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		var foundSummary, foundPrompt bool
		for _, msg := range anthReq.Messages {
			text := anthropicMessageText(msg)
			if strings.Contains(text, "<conversation_summary>") && strings.Contains(text, "carry this forward") {
				foundSummary = true
			}
			if strings.Contains(text, "1. Primary Request and Intent") {
				foundPrompt = true
			}
			if strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
				t.Fatalf("Claude path must not use the Codex handoff prompt: %s", text)
			}
		}
		if !foundSummary || !foundPrompt {
			t.Fatalf("summary=%v prompt=%v messages=%#v", foundSummary, foundPrompt, anthReq.Messages)
		}
	})
}
