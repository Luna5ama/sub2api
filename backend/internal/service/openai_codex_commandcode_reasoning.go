package service

import "strings"

// commandCodeCodexReasoningLevels mirrors the thinking levels the Command Code
// CLI advertises for the models it serves (command-code 1.74.1, the internal
// reasoning-effort registry). Command Code calls the disabled level "off"; it is
// stored as the canonical "none" here.
//
// This table is a fallback. A built-in family rule (Claude, Grok, DeepSeek,
// GPT) that already declares a scale wins, so an upstream change to Command
// Code never silently rewrites a family that Sub2API models explicitly.
var commandCodeCodexReasoningLevels = map[string][]string{
	"claude-fable-5":               {"low", "medium", "high", "xhigh", "max"},
	"claude-fable-5-1":             {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-4-7":              {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-4-8":              {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-5":                {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-5-5":              {"low", "medium", "high", "xhigh", "max"},
	"claude-sonnet-4-6":            {"low", "medium", "high", "xhigh", "max"},
	"claude-sonnet-5":              {"low", "medium", "high", "xhigh", "max"},
	"claude-sonnet-5-5":            {"low", "medium", "high", "xhigh", "max"},
	"deepseek-v4-flash":            {"none", "high", "max"},
	"deepseek-v4-flash-fast":       {"low", "high", "max"},
	"deepseek-v4-flash-vision-exp": {"none", "high", "max"},
	"deepseek-v4-pro":              {"none", "high", "max"},
	"deepseek-v4.1-flash":          {"none", "low", "high", "max"},
	"deepseek-v4.1-flash-fast":     {"none", "low", "high", "max"},
	"fugu-ultra":                   {"high", "xhigh"},
	"gemini-3.1-flash-lite":        {"low", "medium", "high"},
	"gemini-3.5-flash":             {"low", "medium", "high"},
	"gemini-3.5-flash-lite":        {"low", "medium", "high"},
	"gemini-3.6-flash":             {"low", "medium", "high"},
	"gemini-3.7-flash":             {"low", "medium", "high"},
	"gemini-3.8-flash":             {"low", "medium", "high"},
	"glm-5.2":                      {"high", "max"},
	"glm-5.3":                      {"low", "high", "max"},
	"glm-5.3-flash":                {"low", "high", "max"},
	"glm-5.3-flashx":               {"low", "high", "max"},
	"gpt-5.3-codex":                {"low", "medium", "high", "xhigh"},
	"gpt-5.4":                      {"low", "medium", "high", "xhigh"},
	"gpt-5.4-mini":                 {"low", "medium", "high"},
	"gpt-5.5":                      {"low", "medium", "high", "xhigh"},
	"gpt-5.6-luna":                 {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-sol":                  {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-terra":                {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-astra":                  {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-luna":                   {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-sol":                    {"low", "medium", "high", "xhigh", "max"},
	"gpt-6.1-sol":                  {"low", "medium", "high", "xhigh", "max"},
	"grok-4.5":                     {"low", "medium", "high"},
	"grok-4.6":                     {"low", "medium", "high", "xhigh"},
	"grok-4.7":                     {"low", "medium", "high", "xhigh"},
	"hy4-preview":                  {"low", "medium", "high"},
	"kimi-k3":                      {"low", "high", "max"},
	"ling-3.1-flash":               {"low", "medium", "high"},
	"minimax-m3":                   {"low", "medium", "high"},
	"minimax-m3-free":              {"low", "medium", "high"},
	"muse-spark-1.1":               {"low", "medium", "high", "xhigh"},
	"muse-spark-1.2":               {"low", "medium", "high", "xhigh"},
	"muse-spark-1.2-contributor":   {"low", "medium", "high", "xhigh"},
	"muse-spark-1.3":               {"low", "medium", "high", "xhigh", "max"},
	"muse-spark-1.3-contributor":   {"low", "medium", "high", "xhigh"},
	"pixel-canary":                 {"low", "medium", "xhigh"},
	"qwen3.8-27b":                  {"low", "medium", "xhigh"},
	"qwen3.8-flash":                {"low", "medium", "xhigh"},
	"qwen3.8-max":                  {"low", "medium", "xhigh"},
	"qwen3.8-max-0902":             {"low", "medium", "xhigh"},
	"qwen3.8-omni-flash":           {"low", "medium", "xhigh"},
	"space-bunny-alpha":            {"low", "medium", "high", "max"},
	"step-5-preview":               {"low", "medium", "high"},
}

// commandCodeCodexReasoningLevelsForModel resolves a public or upstream model
// ID against the Command Code registry. Provider-qualified slugs such as
// "zai-org/GLM-5.3" and "z-ai/glm-5.3-flash" share one bare key.
func commandCodeCodexReasoningLevelsForModel(modelID string) []configuredCodexReasoningLevel {
	efforts := commandCodeCodexReasoningEffortsForModel(modelID)
	if len(efforts) == 0 {
		return nil
	}
	levels := make([]configuredCodexReasoningLevel, 0, len(efforts))
	for _, effort := range efforts {
		levels = append(levels, configuredCodexReasoningLevel{
			Effort:      effort,
			Description: configuredCodexReasoningLevelDescription(effort),
		})
	}
	return levels
}

func commandCodeCodexReasoningEffortsForModel(modelID string) []string {
	normalized := strings.ToLower(codexProviderQualifiedModelID(modelID))
	if normalized == "" {
		return nil
	}
	if efforts, ok := commandCodeCodexReasoningLevels[normalized]; ok {
		return efforts
	}
	// Command Code lists the free Ling SKU with a ":free" suffix while some
	// deployments expose it without one.
	if trimmed := strings.TrimSuffix(normalized, ":free"); trimmed != normalized {
		if efforts, ok := commandCodeCodexReasoningLevels[trimmed]; ok {
			return efforts
		}
	}
	return nil
}

// commandCodeCodexDefaultReasoningLevel prefers the "high" tier that Sub2API
// uses for its other non-OpenAI families, then falls back to the middle of the
// upstream scale. Command Code stores no default of its own: leaving the level
// unset there means "send no effort field", so any advertised level is a valid
// default for a Codex manifest that requires one.
func commandCodeCodexDefaultReasoningLevel(levels []configuredCodexReasoningLevel) string {
	for _, preferred := range []string{"high", "medium", "low"} {
		for _, level := range levels {
			if level.Effort == preferred {
				return preferred
			}
		}
	}
	if len(levels) == 0 {
		return ""
	}
	return levels[0].Effort
}

// hasOnlyDefaultCodexReasoningPlaceholder reports whether a descriptor still
// carries the "none"-only placeholder produced by newConfiguredCodexModelDescriptor.
// A family rule that already declared real levels replaces that placeholder.
func hasOnlyDefaultCodexReasoningPlaceholder(levels []configuredCodexReasoningLevel) bool {
	return len(levels) == 1 && levels[0].Effort == "none"
}
