package apicompat

// CodexCompactionSummaryPrompt returns the summarisation instruction every
// bridged Codex remote-compaction path appends to the upstream conversation.
//
// It is exported so the responses bridge and the service-side compaction
// rewrite share one prompt instead of drifting apart. The canonical literal
// stays unexported next to the Anthropic request builder.
func CodexCompactionSummaryPrompt() string {
	return codexCompactionSummaryPrompt
}
