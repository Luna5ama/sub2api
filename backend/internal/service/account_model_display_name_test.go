package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountModelDisplayNamesResolveAndCache(t *testing.T) {
	account := Account{
		ID:       7,
		Platform: PlatformDeepseek,
		Credentials: map[string]any{
			"model_display_names": map[string]any{
				"deepseek-v4.1-flash": "Deepseek v4.1 Flash",
				"deepseek-v4-pro":     "  Deepseek v4 Pro  ",
				"":                    "ignored",
				"empty-label":         "",
			},
		},
	}

	got := account.GetModelDisplayNames()
	require.Equal(t, map[string]string{
		"deepseek-v4.1-flash": "Deepseek v4.1 Flash",
		"deepseek-v4-pro":     "Deepseek v4 Pro",
	}, got)

	// A second call exercises the credentials-pointer cache and must be stable.
	require.Equal(t, got, account.GetModelDisplayNames())

	value, ok := account.ModelDisplayNameOverride("deepseek-v4.1-flash")
	require.True(t, ok)
	require.Equal(t, "Deepseek v4.1 Flash", value)
	_, ok = account.ModelDisplayNameOverride("unknown")
	require.False(t, ok)
}

func TestNormalizeModelDisplayNameCredentials(t *testing.T) {
	t.Run("nil is a no-op", func(t *testing.T) {
		require.NoError(t, NormalizeModelDisplayNameCredentials(nil))
	})

	t.Run("absent field is a no-op", func(t *testing.T) {
		creds := map[string]any{"other": 1}
		require.NoError(t, NormalizeModelDisplayNameCredentials(creds))
		require.NotContains(t, creds, "model_display_names")
	})

	t.Run("normalizes and drops empty labels", func(t *testing.T) {
		creds := map[string]any{
			"model_display_names": map[string]any{
				"   m1 ": " Label 1 ",
				"m2":     "",
				"m3":     "Label 3",
			},
		}
		require.NoError(t, NormalizeModelDisplayNameCredentials(creds))
		require.Equal(t, map[string]any{"m1": "Label 1", "m3": "Label 3"}, creds["model_display_names"])
	})

	t.Run("all-empty removes the field", func(t *testing.T) {
		creds := map[string]any{"model_display_names": map[string]any{"m1": ""}}
		require.NoError(t, NormalizeModelDisplayNameCredentials(creds))
		require.NotContains(t, creds, "model_display_names")
	})

	t.Run("rejects non-object", func(t *testing.T) {
		err := NormalizeModelDisplayNameCredentials(map[string]any{"model_display_names": "nope"})
		require.Error(t, err)
	})

	t.Run("rejects non-string value", func(t *testing.T) {
		err := NormalizeModelDisplayNameCredentials(map[string]any{
			"model_display_names": map[string]any{"m1": 5},
		})
		require.Error(t, err)
	})
}

func TestModelDisplayNamesFromAccountsPrefersLowestID(t *testing.T) {
	accounts := []Account{
		{ID: 5, Platform: PlatformDeepseek, Credentials: map[string]any{
			"model_display_names": map[string]any{"m1": "from-5"},
		}},
		{ID: 2, Platform: PlatformDeepseek, Credentials: map[string]any{
			"model_display_names": map[string]any{"m1": "from-2", "m2": "only-2"},
		}},
	}
	require.Equal(t, map[string]string{"m1": "from-2", "m2": "only-2"}, ModelDisplayNamesFromAccounts(accounts))
}

// Scenario: the middle hop of US -> LA -> local relays the label LA learned from
// US. Mapping the upstream id onto the public alias is what lets the name travel
// with the alias a downstream instance actually requests.
func TestUpstreamModelDisplayNamesFollowModelMapping(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"my-coder":       "glm-5.3",
				"echo-alias":     "name-echoes-id",
				"unmapped-alias": "unknown-upstream",
			},
		},
	}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		DisplayNames: map[string]string{
			"glm-5.3":        "GLM 5.3 (Command Code)",
			"name-echoes-id": "name-echoes-id",
		},
	})

	require.Equal(t, map[string]string{"my-coder": "GLM 5.3 (Command Code)"}, account.UpstreamModelDisplayNames(),
		"only names richer than the upstream id survive, and only for mapped public ids")
}

// Scenario: without a model_mapping the account serves upstream ids directly, so
// the snapped names are keyed by the upstream ids themselves.
func TestUpstreamModelDisplayNamesWithoutMappingUseUpstreamIDs(t *testing.T) {
	t.Parallel()

	account := &Account{ID: 1, Platform: PlatformOpenAI}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		Models: map[string]UpstreamModelMetadata{
			"glm-5.3": {ID: "glm-5.3", DisplayName: "GLM 5.3 (Command Code)"},
		},
	})

	require.Equal(t, map[string]string{"glm-5.3": "GLM 5.3 (Command Code)"}, account.UpstreamModelDisplayNames())
}

// Scenario: passthrough accounts ignore model_mapping when routing, so a stale
// mapping must not relabel their upstream names onto public ids either.
func TestUpstreamModelDisplayNamesIgnoreStaleMappingWhenPassthrough(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Extra:    map[string]any{"openai_passthrough": true},
		Credentials: map[string]any{
			"model_mapping": map[string]any{"stale-alias": "glm-5.3"},
		},
	}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		DisplayNames: map[string]string{"glm-5.3": "GLM 5.3 (Command Code)"},
	})

	require.Equal(t, map[string]string{"glm-5.3": "GLM 5.3 (Command Code)"}, account.UpstreamModelDisplayNames())
}

// Scenario: an operator's local override outranks an upstream-learned name and
// does so even when the account carrying the upstream name has a lower id, which
// is the documented precedence for the whole catalogue.
func TestModelDisplayNamesFromAccountsWithUpstreamLetsLocalOverrideWin(t *testing.T) {
	t.Parallel()

	localOverride := &Account{
		ID: 5,
		Credentials: map[string]any{
			"model_display_names": map[string]any{"glm-5.3": "Operator Label"},
		},
	}
	upstreamNamed := &Account{ID: 2}
	upstreamNamed.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		DisplayNames: map[string]string{"glm-5.3": "GLM 5.3 (Command Code)"},
	})

	merged := ModelDisplayNamesFromAccountsWithUpstream([]Account{*localOverride, *upstreamNamed})
	require.Equal(t, map[string]string{"glm-5.3": "Operator Label"}, merged)

	// The upstream name still fills ids no operator labelled.
	upstreamOnly := &Account{ID: 1, Credentials: map[string]any{"model_mapping": map[string]any{"other": "glm-5.4"}}}
	upstreamOnly.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		DisplayNames: map[string]string{"glm-5.4": "GLM 5.4 (Command Code)"},
	})
	merged = ModelDisplayNamesFromAccountsWithUpstream([]Account{*localOverride, *upstreamOnly})
	require.Equal(t, map[string]string{
		"glm-5.3": "Operator Label",
		"other":   "GLM 5.4 (Command Code)",
	}, merged)

	// Opting out of the upstream channel keeps the original local-only behaviour.
	require.Equal(t, map[string]string{"glm-5.3": "Operator Label"},
		ModelDisplayNamesFromAccounts([]Account{*localOverride, *upstreamOnly}))
}

func TestRewriteModelDisplayNamesHandlesCodexAndOpenAIEnvelopes(t *testing.T) {
	t.Run("codex models envelope keyed by slug", func(t *testing.T) {
		body := []byte(`{"models":[{"slug":"deepseek-v4.1-flash","display_name":"deepseek-v4.1-flash"},{"slug":"other","display_name":"other"}]}`)
		rewritten := RewriteModelDisplayNames(body, map[string]string{"deepseek-v4.1-flash": "Deepseek v4.1 Flash"})
		var envelope struct {
			Models []struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"display_name"`
			} `json:"models"`
		}
		require.NoError(t, json.Unmarshal(rewritten, &envelope))
		require.Equal(t, "Deepseek v4.1 Flash", envelope.Models[0].DisplayName)
		require.Equal(t, "other", envelope.Models[1].DisplayName)
	})

	t.Run("openai data envelope keyed by id", func(t *testing.T) {
		body := []byte(`{"object":"list","data":[{"id":"deepseek-v4.1-flash","display_name":"deepseek-v4.1-flash"}]}`)
		rewritten := RewriteModelDisplayNames(body, map[string]string{"deepseek-v4.1-flash": "Deepseek v4.1 Flash"})
		var envelope struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rewritten, &envelope))
		require.Equal(t, "Deepseek v4.1 Flash", envelope.Data[0].DisplayName)
	})

	t.Run("no override leaves body unchanged", func(t *testing.T) {
		body := []byte(`{"models":[{"slug":"x","display_name":"x"}]}`)
		require.Equal(t, body, RewriteModelDisplayNames(body, nil))
	})
}

func TestBuildCodexModelsManifestAppliesDisplayNameOverride(t *testing.T) {
	body, err := buildCodexModelsManifestWithDisplayNames(
		[]string{"deepseek-v4.1-flash"},
		nil,
		nil,
		nil,
		nil,
		map[string]string{"deepseek-v4.1-flash": "Deepseek v4.1 Flash"},
	)
	require.NoError(t, err)
	var envelope struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Len(t, envelope.Models, 1)
	require.Equal(t, "Deepseek v4.1 Flash", envelope.Models[0].DisplayName)
}
