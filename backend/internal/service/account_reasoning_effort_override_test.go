package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeReasoningEffortOverrideCredentials(t *testing.T) {
	t.Parallel()

	require.NoError(t, NormalizeReasoningEffortOverrideCredentials(nil))

	creds := map[string]any{}
	require.NoError(t, NormalizeReasoningEffortOverrideCredentials(creds))
	require.NotContains(t, creds, credKeyReasoningEffortOverrides)

	creds = map[string]any{
		credKeyReasoningEffortOverrides: map[string]any{
			"glm-5.3": map[string]any{
				"default": "high",
				"levels":  []any{"low", "high", "max"},
			},
			"  ": map[string]any{"levels": []any{"low"}},
		},
	}
	require.NoError(t, NormalizeReasoningEffortOverrideCredentials(creds))
	entries, ok := creds[credKeyReasoningEffortOverrides].(map[string]any)
	require.True(t, ok)
	require.Len(t, entries, 1)
	entry, ok := entries["glm-5.3"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "high", entry["default"])
	require.Equal(t, []string{"low", "high", "max"}, entry["levels"])

	// An absent default falls back to the first declared level.
	creds = map[string]any{
		credKeyReasoningEffortOverrides: map[string]any{
			"deepseek-v4.1-flash": map[string]any{"levels": []any{"off", "high"}},
		},
	}
	require.NoError(t, NormalizeReasoningEffortOverrideCredentials(creds))
	entry = creds[credKeyReasoningEffortOverrides].(map[string]any)["deepseek-v4.1-flash"].(map[string]any)
	require.Equal(t, "none", entry["default"])
	require.Equal(t, []string{"none", "high"}, entry["levels"])

	// An empty table is dropped instead of persisting an empty object.
	creds = map[string]any{credKeyReasoningEffortOverrides: map[string]any{}}
	require.NoError(t, NormalizeReasoningEffortOverrideCredentials(creds))
	require.NotContains(t, creds, credKeyReasoningEffortOverrides)
}

func TestNormalizeReasoningEffortOverrideCredentialsRejectsInvalid(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]any{
		"not an object":   "glm-5.3",
		"unknown level":   map[string]any{"glm-5.3": map[string]any{"levels": []any{"turbo"}}},
		"empty levels":    map[string]any{"glm-5.3": map[string]any{"levels": []any{}}},
		"default missing": map[string]any{"glm-5.3": map[string]any{"default": "max", "levels": []any{"low"}}},
		"wrong shape":     map[string]any{"glm-5.3": "low"},
		"too many levels": map[string]any{"glm-5.3": map[string]any{
			"levels": []any{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "low"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			err := NormalizeReasoningEffortOverrideCredentials(map[string]any{
				credKeyReasoningEffortOverrides: raw,
			})
			require.Error(t, err)
		})
	}
}

func TestAccountReasoningEffortOverrideForPrefersPublicModelID(t *testing.T) {
	t.Parallel()

	account := Account{Credentials: map[string]any{
		credKeyReasoningEffortOverrides: map[string]any{
			"public-alias": map[string]any{"default": "high", "levels": []any{"low", "high", "max"}},
			"real-model":   map[string]any{"default": "low", "levels": []any{"low", "high"}},
		},
	}}

	override, ok := account.ReasoningEffortOverrideFor("public-alias", "real-model")
	require.True(t, ok)
	require.Equal(t, "high", override.Default)
	require.Equal(t, []string{"low", "high", "max"}, override.Levels)

	override, ok = account.ReasoningEffortOverrideFor("real-model")
	require.True(t, ok)
	require.Equal(t, "low", override.Default)

	_, ok = account.ReasoningEffortOverrideFor("unlisted")
	require.False(t, ok)
}

func TestAccountReasoningEffortOverrideSkipsInvalidPersistedEntries(t *testing.T) {
	t.Parallel()

	account := Account{Credentials: map[string]any{
		credKeyReasoningEffortOverrides: map[string]any{
			"glm-5.3":       map[string]any{"levels": []any{"low", "high", "max"}},
			"broken":        map[string]any{"levels": []any{"not-a-level"}},
			"no-levels":     map[string]any{"default": "high"},
			"not-an-object": true,
		},
	}}

	overrides := account.GetReasoningEffortOverrides()
	require.Len(t, overrides, 1)
	require.Equal(t, []string{"low", "high", "max"}, overrides["glm-5.3"].Levels)
	require.Equal(t, "low", overrides["glm-5.3"].Default)
}
