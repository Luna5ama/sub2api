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
