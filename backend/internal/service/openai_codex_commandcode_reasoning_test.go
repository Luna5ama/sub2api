package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Scenario: Command Code supplies thinking levels for models Sub2API has no
// family rule for; the bare and provider-qualified spellings resolve alike.
func TestConfiguredCodexModelDescriptorUsesCommandCodeReasoningLevels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model  string
		levels []string
	}{
		{"glm-5.3", []string{"low", "high", "max"}},
		{"zai-org/GLM-5.3", []string{"low", "high", "max"}},
		{"z-ai/glm-5.3-flash", []string{"low", "high", "max"}},
		{"glm-5.3-flashx", []string{"low", "high", "max"}},
		{"glm-5.2", []string{"high", "max"}},
		{"moonshotai/Kimi-K3", []string{"low", "high", "max"}},
		{"Qwen/Qwen3.8-Max", []string{"low", "medium", "xhigh"}},
		{"google/gemini-3.8-flash", []string{"low", "medium", "high"}},
		{"MiniMaxAI/MiniMax-M3", []string{"low", "medium", "high"}},
		{"meta/muse-spark-1.3", []string{"low", "medium", "high", "xhigh", "max"}},
		{"sakana/fugu-ultra", []string{"high", "xhigh"}},
		{"tencent/hy4-preview", []string{"low", "medium", "high"}},
	}

	for _, tc := range cases {
		descriptor := newConfiguredCodexModelDescriptor(tc.model)
		require.Equal(t, tc.levels, effortsFromConfiguredCodexLevels(descriptor.SupportedReasoningLevels), tc.model)
		require.NotNil(t, descriptor.DefaultReasoningLevel, tc.model)
		require.Contains(t, tc.levels, *descriptor.DefaultReasoningLevel, tc.model)
	}
}

// Scenario: an existing built-in family rule is never replaced by Command Code.
// DeepSeek V4 Flash advertises off/high/max upstream, but Sub2API keeps its
// deliberate low/high/max scale for that family.
func TestConfiguredCodexModelDescriptorKeepsExistingFamilyReasoningLevels(t *testing.T) {
	t.Parallel()

	deepSeek := newConfiguredCodexModelDescriptor("deepseek-v4-flash")
	require.Equal(t, []string{"low", "high", "max"}, effortsFromConfiguredCodexLevels(deepSeek.SupportedReasoningLevels))
	require.NotContains(t, effortsFromConfiguredCodexLevels(deepSeek.SupportedReasoningLevels), "none")

	deepSeekQualified := newConfiguredCodexModelDescriptor("deepseek/deepseek-v4.1-flash")
	require.Equal(t, []string{"low", "high", "max"}, effortsFromConfiguredCodexLevels(deepSeekQualified.SupportedReasoningLevels))

	claude := newConfiguredCodexModelDescriptor("claude-opus-5")
	require.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, effortsFromConfiguredCodexLevels(claude.SupportedReasoningLevels))

	// Command Code advertises xhigh for Claude Sonnet 4.6; Sub2API's own Claude
	// scale for that model stops at max, and that deliberate scale wins.
	claudeSonnet46 := newConfiguredCodexModelDescriptor("claude-sonnet-4-6")
	require.Equal(t, []string{"low", "medium", "high", "max"}, effortsFromConfiguredCodexLevels(claudeSonnet46.SupportedReasoningLevels))
	require.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, commandCodeCodexReasoningEffortsForModel("claude-sonnet-4-6"))

	grok := newConfiguredCodexModelDescriptor("xai/grok-4.6")
	require.Equal(t, []string{"low", "medium", "high", "xhigh"}, effortsFromConfiguredCodexLevels(grok.SupportedReasoningLevels))
}

// Scenario: models absent from the Command Code registry still fall back to the
// non-reasoning placeholder instead of inventing a scale.
func TestConfiguredCodexModelDescriptorIgnoresUnknownCommandCodeModel(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"claude-haiku-4-5-20251001", "company-coding-model", "grok-4.20-0309-non-reasoning"} {
		descriptor := newConfiguredCodexModelDescriptor(model)
		require.Equal(t, []string{"none"}, effortsFromConfiguredCodexLevels(descriptor.SupportedReasoningLevels), model)
	}
}

// Scenario: the fallback reaches the emitted manifest, including the level
// descriptions Codex renders in the effort picker.
func TestBuildCodexModelsManifestAdvertisesCommandCodeReasoningLevels(t *testing.T) {
	t.Parallel()

	body, err := BuildCodexModelsManifest([]string{"glm-5.3", "qwen3.8-max", "company-coding-model"})
	require.NoError(t, err)

	var manifest struct {
		Models []struct {
			Slug                     string `json:"slug"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			SupportedReasoningLevels []struct {
				Effort      string `json:"effort"`
				Description string `json:"description"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &manifest))
	require.Len(t, manifest.Models, 3)

	bySlug := make(map[string]int, len(manifest.Models))
	for i, model := range manifest.Models {
		bySlug[model.Slug] = i
	}

	glm := manifest.Models[bySlug["glm-5.3"]]
	require.Equal(t, "high", glm.DefaultReasoningLevel)
	require.Equal(t, []string{"low", "high", "max"}, manifestEffortValues(glm.SupportedReasoningLevels))
	for _, level := range glm.SupportedReasoningLevels {
		require.NotEmpty(t, level.Description, level.Effort)
	}

	qwen := manifest.Models[bySlug["qwen3.8-max"]]
	require.Equal(t, "medium", qwen.DefaultReasoningLevel)
	require.Equal(t, []string{"low", "medium", "xhigh"}, manifestEffortValues(qwen.SupportedReasoningLevels))

	unknown := manifest.Models[bySlug["company-coding-model"]]
	require.Equal(t, "none", unknown.DefaultReasoningLevel)
	require.Equal(t, []string{"none"}, manifestEffortValues(unknown.SupportedReasoningLevels))
}

// Scenario: Command Code is stored under its own level name for the disabled
// tier, so the "off" value must arrive as the canonical "none".
func TestCommandCodeReasoningLevelsNormalizeOffToNone(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"none", "high", "max"}, commandCodeCodexReasoningEffortsForModel("deepseek/deepseek-v4-pro"))
	require.Equal(t, []string{"none", "low", "high", "max"}, commandCodeCodexReasoningEffortsForModel("deepseek-v4.1-flash"))
	require.Equal(t, []string{"low", "medium", "high"}, commandCodeCodexReasoningEffortsForModel("inclusionai/ling-3.1-flash:free"))
	require.Equal(t, []string{"low", "medium", "high"}, commandCodeCodexReasoningEffortsForModel("ling-3.1-flash"))
	require.Nil(t, commandCodeCodexReasoningEffortsForModel("company-coding-model"))
}

func manifestEffortValues(levels []struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}) []string {
	efforts := make([]string, 0, len(levels))
	for _, level := range levels {
		efforts = append(efforts, level.Effort)
	}
	return efforts
}
