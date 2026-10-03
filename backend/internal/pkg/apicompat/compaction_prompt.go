package apicompat

import "strings"

// codexCompactionHandoffPrompt is the compaction instruction for every upstream
// that is not a Claude-family model.
//
// It is Codex's own SUMMARIZATION_PROMPT, verbatim
// (codex-rs/prompts/templates/compact/prompt.md). Codex pairs that short handoff
// instruction with real retained history: build_compacted_history keeps the
// most recent user messages and remote compaction v2 keeps up to 64k tokens of
// original user/agent messages next to the summary. The summary therefore only
// has to carry what the originals cannot express -- decisions, intent and next
// steps -- and being short is an advantage rather than a loss.
//
// The Claude path is different on purpose: Claude models are served the
// exhaustive nine-section archive in claudeCodeCompactionSummaryPrompt, the
// shape Claude Code itself uses.
//
// The last line is an operational guard rather than part of the upstream
// prompt. The bridged paths still forward the client's tool definitions, and a
// tool call on a compaction turn would leave the synthesized summary empty.
const codexCompactionHandoffPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.

Reply with the handoff summary only. Do not call or use any tools.`

// codexCompactionHandoffPrefix introduces a stored summary when Codex replays
// it on a later turn. It is Codex's own SUMMARY_PREFIX, verbatim
// (codex-rs/prompts/templates/compact/summary_prefix.md), which Codex renders as
// "<prefix>\n<summary>".
//
// Note the "state of the tools" sentence: it holds here because a continued
// Codex session has the same workspace and machine behind it. The prefix
// replaces the gateway's invented <conversation_summary> wrapper on non-Claude
// upstreams, so the replayed text is the wording successor models are
// accustomed to.
const codexCompactionHandoffPrefix = `Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:`

// claudeCompactionReplayOpenTag and claudeCompactionReplayCloseTag wrap a
// replayed summary for Claude-family upstreams. claudeCodeCompactionSummaryPrompt
// refers to these tags when it tells the compactor to carry an earlier summary
// forward, so prompt and wrapper stay a matched pair.
const (
	claudeCompactionReplayOpenTag  = "<conversation_summary>"
	claudeCompactionReplayCloseTag = "</conversation_summary>"
)

// CompactionSummaryPromptForModel returns the compaction instruction to append
// for the upstream model that will answer the compaction turn.
func CompactionSummaryPromptForModel(model string) string {
	if IsClaudeFamilyModel(model) {
		return claudeCodeCompactionSummaryPrompt
	}
	return codexCompactionHandoffPrompt
}

// CompactionReplayTextForModel renders a stored compaction summary the way the
// target model expects to read it on a later turn.
func CompactionReplayTextForModel(model string, summary string) string {
	if IsClaudeFamilyModel(model) {
		return claudeCompactionReplayOpenTag + "\n" + summary + "\n" + claudeCompactionReplayCloseTag
	}
	return codexCompactionHandoffPrefix + "\n" + summary
}

// CodexCompactionHandoffPrefix returns Codex's SUMMARY_PREFIX, the text that
// introduces a replayed summary on non-Claude upstreams.
func CodexCompactionHandoffPrefix() string {
	return codexCompactionHandoffPrefix
}

// CodexCompactionHandoffMessage renders the non-Claude replay text with the
// model selector already resolved, for callers and tests that only deal with
// Codex-style upstreams.
func CodexCompactionHandoffMessage(summary string) string {
	return CompactionReplayTextForModel("", summary)
}

// IsClaudeFamilyModel reports whether a model id names an Anthropic Claude
// model. Provider-qualified ids ("anthropic/claude-opus-4-5",
// "us.anthropic.claude-sonnet-4-5") count, so a substring match is deliberate.
func IsClaudeFamilyModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "claude")
}
