package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// relayRepoStub serves the LA account for both the sync write and the later
// gateway read, so the chain under test is exactly what production does.
type relayRepoStub struct {
	service.AccountRepository

	groupID  int64
	accounts []service.Account
}

func (r *relayRepoStub) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]service.Account, error) {
	if groupID != r.groupID {
		return nil, nil
	}
	return append([]service.Account(nil), r.accounts...), nil
}

func (r *relayRepoStub) ListByGroup(_ context.Context, groupID int64) ([]service.Account, error) {
	return r.ListSchedulableByGroupID(context.Background(), groupID)
}

func (r *relayRepoStub) ListModelAvailabilityCandidates(_ context.Context, groupID *int64, _ []string, _ bool) ([]service.Account, error) {
	if groupID == nil {
		return nil, nil
	}
	return r.ListSchedulableByGroupID(context.Background(), *groupID)
}

func (r *relayRepoStub) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	accounts := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (r *relayRepoStub) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	accounts, err := r.ListSchedulableByGroupID(context.Background(), groupID)
	if err != nil {
		return nil, err
	}
	filtered := make([]service.Account, 0, len(accounts))
	for _, account := range accounts {
		if account.Platform == platform {
			filtered = append(filtered, account)
		}
	}
	return filtered, nil
}

func (r *relayRepoStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	raw, ok := updates[service.UpstreamModelMetadataExtraKey]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var snapshot service.UpstreamModelMetadataSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return err
	}
	if len(r.accounts) == 0 {
		return nil
	}
	r.accounts[0].SetUpstreamModelMetadataSnapshot(snapshot)
	return nil
}

// relayBodyUpstream serves a fixed body for every upstream model-list request,
// standing in for the US instance.
type relayBodyUpstream struct {
	service.HTTPUpstream

	body string
}

func (u *relayBodyUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.response(), nil
}

func (u *relayBodyUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.response(), nil
}

func (u *relayBodyUpstream) response() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}
}

func testAccountExtraSnapshot(t *testing.T, account service.Account) *service.UpstreamModelMetadataSnapshot {
	t.Helper()

	raw, ok := account.Extra[service.UpstreamModelMetadataExtraKey]
	require.True(t, ok, "the synced snapshot must be stored on the account")
	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	var snapshot service.UpstreamModelMetadataSnapshot
	require.NoError(t, json.Unmarshal(encoded, &snapshot))
	return &snapshot
}

// Scenario: the exact US -> LA -> local chain, using the account shape a real
// relay deployment uses: an upstream-type account with no model_mapping. The US
// account carries a manual display-name override, LA syncs the upstream
// catalogue, and LA must advertise both the model and its label to whatever
// syncs from LA next.
func TestModelDisplayNameRelaysAcrossInstanceHops(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const usGroupID int64 = 900
	usHandler := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			usGroupID: {{
				ID:          1,
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeAPIKey,
				Status:      service.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{
					"api_key":             "us-key",
					"base_url":            "https://command-code.example/v1",
					"model_mapping":       map[string]any{"glm-5.3": "glm-5.3"},
					"model_display_names": map[string]any{"glm-5.3": "GLM 5.3 (Command Code)"},
				},
			}},
		},
	})

	usRec := httptest.NewRecorder()
	usCtx, _ := gin.CreateTestContext(usRec)
	usCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	usCtx.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: usGroupID, Platform: service.PlatformOpenAI},
	})
	usHandler.Models(usCtx)
	require.Equal(t, http.StatusOK, usRec.Code, usRec.Body.String())
	require.Equal(t, "GLM 5.3 (Command Code)",
		displayNameForModelIDForTest(t, usRec.Body.Bytes(), "glm-5.3"),
		"the US instance must advertise the operator override")
	t.Logf("US /v1/models: %s", usRec.Body.String())

	// Hop 2: LA syncs the US catalogue.
	laGroupID := int64(901)
	laRepo := &relayRepoStub{groupID: laGroupID}
	laRepo.accounts = []service.Account{{
		ID:          2,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeUpstream,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":  "la-key",
			"base_url": "https://us.example/v1",
		},
	}}
	syncService := service.NewAccountTestService(
		laRepo,
		nil, nil, nil, nil,
		&relayBodyUpstream{body: usRec.Body.String()},
		&config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		nil,
	)
	catalog, err := syncService.SyncUpstreamModelCatalog(context.Background(), &laRepo.accounts[0])
	require.NoError(t, err, "an upstream relay account must be able to sync its upstream catalogue")
	require.Contains(t, catalog.Models, "glm-5.3", "LA must list the synced model")

	snapshot := testAccountExtraSnapshot(t, laRepo.accounts[0])
	require.Equal(t, "GLM 5.3 (Command Code)", snapshot.DisplayNames["glm-5.3"],
		"the synced label must be persisted on the relay account")

	// Hop 3: LA serves its own catalogue to the next consumer.
	laHandler := newGatewayModelsHandlerForTest(laRepo)
	laRec := httptest.NewRecorder()
	laCtx, _ := gin.CreateTestContext(laRec)
	laCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	laCtx.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: laGroupID, Platform: service.PlatformOpenAI},
	})
	laHandler.Models(laCtx)
	require.Equal(t, http.StatusOK, laRec.Code, laRec.Body.String())
	t.Logf("LA /v1/models: %s", laRec.Body.String())
	require.Equal(t, "GLM 5.3 (Command Code)",
		displayNameForModelIDForTest(t, laRec.Body.Bytes(), "glm-5.3"),
		"LA must re-publish the label it synced from US")
}

// Scenario: a Codex client pointed at LA asks for the manifest form of the
// catalogue. The synced label has to survive that representation too.
func TestModelDisplayNameRelaysIntoCodexManifest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, platform := range []string{service.PlatformOpenAI, service.PlatformComposite} {
		t.Run(platform, func(t *testing.T) {
			groupID := int64(910)
			account := service.Account{
				ID:          2,
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeAPIKey,
				Status:      service.StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{
					"api_key":       "la-key",
					"base_url":      "https://us.example/v1",
					"model_mapping": map[string]any{"glm-5.3": "glm-5.3"},
				},
			}
			account.SetUpstreamModelMetadataSnapshot(service.UpstreamModelMetadataSnapshot{
				DisplayNames: map[string]string{"glm-5.3": "GLM 5.3 (Command Code)"},
			})
			repo := &relayRepoStub{groupID: groupID, accounts: []service.Account{account}}
			gatewayService := service.NewOpenAIGatewayService(
				repo,
				nil, nil, nil, nil, nil, nil,
				&config.Config{RunMode: config.RunModeSimple},
				nil, nil, nil, nil, nil,
				&relayBodyUpstream{body: `{"object":"list","data":[{"id":"glm-5.3","object":"model"}]}`},
				nil, nil, nil, nil, nil, nil, nil, nil,
			)
			openAIHandler := &OpenAIGatewayHandler{gatewayService: gatewayService}
			generatedHandler := newGatewayModelsHandlerForTest(repo)
			group := &service.Group{ID: groupID, Platform: platform}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.150.0", nil)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{GroupID: &group.ID, Group: group})
			if platform == service.PlatformOpenAI {
				openAIHandler.CodexModels(c)
			} else {
				generatedHandler.CodexModels(c)
			}

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			t.Logf("%s codex manifest: %s", platform, rec.Body.String())
			require.Equal(t, "GLM 5.3 (Command Code)", manifestDisplayNameForTest(t, rec.Body.Bytes(), "glm-5.3"))
		})
	}
}

func manifestDisplayNameForTest(t *testing.T, body []byte, slug string) string {
	t.Helper()

	var envelope struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	for _, model := range envelope.Models {
		if model.Slug == slug {
			return model.DisplayName
		}
	}
	t.Fatalf("model %q not present in %s", slug, string(body))
	return ""
}

const hopModelName = "GLM 5.3 (Command Code)"

// usModelListBody is what the US instance publishes for an account that carries
// the operator override. LA syncs exactly this body.
func usModelListBody(modelID string) string {
	encoded, _ := json.Marshal(map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":           modelID,
			"object":       "model",
			"display_name": hopModelName,
		}},
	})
	return string(encoded)
}

type hopCase struct {
	name        string
	groupPlat   string
	accountPlat string
	accountType string
	mapping     map[string]any
	publicModel string
}

func requestModelListForHopTest(t *testing.T, repo *relayRepoStub, group *service.Group) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{GroupID: &group.ID, Group: group})
	newGatewayModelsHandlerForTest(repo).Models(c)
	return rec
}

func requestCodexManifestForHopTest(t *testing.T, repo *relayRepoStub, group *service.Group) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.150.0", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{GroupID: &group.ID, Group: group})
	newGatewayModelsHandlerForTest(repo).CodexModels(c)
	return rec
}

// Scenario: every account shape an operator can use for a relay hop has to sync
// the upstream catalogue and then re-publish both the public model ids and the
// labels, in the plain list and the Codex manifest alike.
func TestModelDisplayNameRelaysAcrossAccountShapes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []hopCase{
		{name: "openai identity mapping", groupPlat: service.PlatformOpenAI, accountPlat: service.PlatformOpenAI, accountType: service.AccountTypeAPIKey,
			mapping: map[string]any{"glm-5.3": "glm-5.3"}, publicModel: "glm-5.3"},
		{name: "openai alias mapping", groupPlat: service.PlatformOpenAI, accountPlat: service.PlatformOpenAI, accountType: service.AccountTypeAPIKey,
			mapping: map[string]any{"team-glm": "glm-5.3"}, publicModel: "team-glm"},
		{name: "openai upstream relay account", groupPlat: service.PlatformOpenAI, accountPlat: service.PlatformOpenAI, accountType: service.AccountTypeUpstream,
			publicModel: "glm-5.3"},
		{name: "openai unmapped api key account", groupPlat: service.PlatformOpenAI, accountPlat: service.PlatformOpenAI, accountType: service.AccountTypeAPIKey,
			publicModel: "glm-5.3"},
		{name: "openai wildcard mapping", groupPlat: service.PlatformOpenAI, accountPlat: service.PlatformOpenAI, accountType: service.AccountTypeAPIKey,
			mapping: map[string]any{"glm-*": "glm-5.3"}, publicModel: "glm-5.3"},
		{name: "zhipu identity mapping", groupPlat: service.PlatformZhipu, accountPlat: service.PlatformZhipu, accountType: service.AccountTypeAPIKey,
			mapping: map[string]any{"glm-5.3": "glm-5.3"}, publicModel: "glm-5.3"},
		{name: "deepseek identity mapping", groupPlat: service.PlatformDeepseek, accountPlat: service.PlatformDeepseek, accountType: service.AccountTypeAPIKey,
			mapping: map[string]any{"glm-5.3": "glm-5.3"}, publicModel: "glm-5.3"},
		{name: "deepseek upstream relay account", groupPlat: service.PlatformDeepseek, accountPlat: service.PlatformDeepseek, accountType: service.AccountTypeUpstream,
			publicModel: "glm-5.3"},
		{name: "composite openai identity mapping", groupPlat: service.PlatformComposite, accountPlat: service.PlatformOpenAI, accountType: service.AccountTypeAPIKey,
			mapping: map[string]any{"glm-5.3": "glm-5.3"}, publicModel: "glm-5.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const groupID int64 = 960
			credentials := map[string]any{"api_key": "la-key", "base_url": "https://us.example/v1"}
			if tc.mapping != nil {
				credentials["model_mapping"] = tc.mapping
			}
			repository := &relayRepoStub{groupID: groupID, accounts: []service.Account{{
				ID:          2,
				Platform:    tc.accountPlat,
				Type:        tc.accountType,
				Status:      service.StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: credentials,
			}}}

			syncService := service.NewAccountTestService(
				repository,
				nil, nil, nil, nil,
				&relayBodyUpstream{body: usModelListBody("glm-5.3")},
				&config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
				nil,
			)
			_, err := syncService.SyncUpstreamModelCatalog(context.Background(), &repository.accounts[0])
			require.NoError(t, err, "LA must be able to sync this account shape")

			group := &service.Group{ID: groupID, Platform: tc.groupPlat}
			plain := requestModelListForHopTest(t, repository, group)
			require.Equal(t, http.StatusOK, plain.Code, plain.Body.String())
			require.Equal(t, hopModelName, displayNameForModelIDForTest(t, plain.Body.Bytes(), tc.publicModel),
				"plain /v1/models must re-publish the synced label; body=%s", plain.Body.String())

			manifest := requestCodexManifestForHopTest(t, repository, group)
			require.Equal(t, http.StatusOK, manifest.Code, manifest.Body.String())
			require.Equal(t, hopModelName, manifestDisplayNameForTest(t, manifest.Body.Bytes(), tc.publicModel),
				"the Codex manifest must re-publish the synced label; body=%s", manifest.Body.String())
		})
	}
}
