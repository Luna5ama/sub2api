package service

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// model_display_names lets an administrator override the display name of a
// specific model on an account. It maps the public (requested) model id to the
// label shown in clients, e.g. {"deepseek-v4.1-flash": "Deepseek v4.1 Flash"}.
//
// The override is applied after every provider-specific prettifier, so it wins
// over the built-in catalog label. Storing it in credentials mirrors
// model_mapping / header_overrides and keeps it scoped to a single account.
const (
	credKeyModelDisplayNames = "model_display_names"

	maxModelDisplayNameEntries       = 256
	maxModelDisplayNameModelIDLength = 200
	maxModelDisplayNameValueLength   = 200
)

// GetModelDisplayNames returns the effective model id -> display name overrides.
// The map is cached against the credentials pointer so repeated list/manifest
// builds only validate once per credentials mutation.
func (a *Account) GetModelDisplayNames() map[string]string {
	if a == nil || a.Credentials == nil {
		return nil
	}
	rawMapping, rawIsAnyMap := a.Credentials[credKeyModelDisplayNames].(map[string]any)
	if !rawIsAnyMap {
		// Tolerate values that were JSON-decoded into map[string]string already.
		return resolveModelDisplayNames(stringMappingFromRaw(a.Credentials[credKeyModelDisplayNames]))
	}

	credentialsPtr := mapPtr(a.Credentials)
	rawPtr := mapPtr(rawMapping)
	rawLen := len(rawMapping)
	rawSig := uint64(0)
	rawSigReady := false

	if a.modelDisplayNameCacheReady &&
		a.modelDisplayNameCacheCredentialsPtr == credentialsPtr &&
		a.modelDisplayNameCacheRawPtr == rawPtr &&
		a.modelDisplayNameCacheRawLen == rawLen {
		rawSig = modelMappingSignature(rawMapping)
		rawSigReady = true
		if a.modelDisplayNameCacheRawSig == rawSig {
			return a.modelDisplayNameCache
		}
	}

	overrides := resolveModelDisplayNames(stringMappingFromRaw(rawMapping))
	if !rawSigReady {
		rawSig = modelMappingSignature(rawMapping)
	}

	a.modelDisplayNameCache = overrides
	a.modelDisplayNameCacheReady = true
	a.modelDisplayNameCacheCredentialsPtr = credentialsPtr
	a.modelDisplayNameCacheRawPtr = rawPtr
	a.modelDisplayNameCacheRawLen = rawLen
	a.modelDisplayNameCacheRawSig = rawSig
	return overrides
}

// ModelDisplayNameOverride returns the configured display name for a model id.
func (a *Account) ModelDisplayNameOverride(modelID string) (string, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", false
	}
	value, ok := a.GetModelDisplayNames()[modelID]
	return value, ok
}

// resolveModelDisplayNames defensively re-validates raw overrides so accounts
// persisted before this feature (or with hand-edited credentials) cannot inject
// blank/oversized labels.
func resolveModelDisplayNames(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]string, len(raw))
	for modelID, displayName := range raw {
		modelID, displayName, err := normalizeModelDisplayNameEntry(modelID, displayName)
		if err != nil || modelID == "" || displayName == "" {
			continue
		}
		result[modelID] = displayName
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// normalizeModelDisplayNameEntry trims and validates one override. An entry with
// both sides empty is a placeholder and returns ("", "", nil).
func normalizeModelDisplayNameEntry(modelID, displayName string) (string, string, error) {
	modelID = strings.TrimSpace(modelID)
	displayName = strings.TrimSpace(displayName)
	if modelID == "" {
		if displayName == "" {
			return "", "", nil
		}
		return "", "", infraerrors.New(http.StatusBadRequest, "INVALID_MODEL_DISPLAY_NAME",
			"model_display_names id must not be empty")
	}
	if len(modelID) > maxModelDisplayNameModelIDLength {
		return "", "", infraerrors.Newf(http.StatusBadRequest, "INVALID_MODEL_DISPLAY_NAME",
			"model_display_names id %q exceeds %d characters", modelID, maxModelDisplayNameModelIDLength)
	}
	if displayName == "" {
		// Empty label means "keep the built-in display name".
		return modelID, "", nil
	}
	if len(displayName) > maxModelDisplayNameValueLength {
		return "", "", infraerrors.Newf(http.StatusBadRequest, "INVALID_MODEL_DISPLAY_NAME",
			"model_display_names value for %q exceeds %d characters", modelID, maxModelDisplayNameValueLength)
	}
	return modelID, displayName, nil
}

// NormalizeModelDisplayNameCredentials validates and canonicalizes the optional
// model_display_names field. A nil/absent field is a no-op.
func NormalizeModelDisplayNameCredentials(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	raw, ok := credentials[credKeyModelDisplayNames]
	if !ok || raw == nil {
		return nil
	}

	var entries map[string]any
	switch m := raw.(type) {
	case map[string]any:
		entries = m
	case map[string]string:
		entries = make(map[string]any, len(m))
		for k, v := range m {
			entries[k] = v
		}
	default:
		return infraerrors.New(http.StatusBadRequest, "INVALID_MODEL_DISPLAY_NAME",
			"model_display_names must be an object of model id to display name")
	}

	if len(entries) > maxModelDisplayNameEntries {
		return infraerrors.Newf(http.StatusBadRequest, "INVALID_MODEL_DISPLAY_NAME",
			"model_display_names supports at most %d entries", maxModelDisplayNameEntries)
	}

	normalized := make(map[string]any, len(entries))
	for rawModelID, rawValue := range entries {
		value, isString := rawValue.(string)
		if !isString {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_MODEL_DISPLAY_NAME",
				"model_display_names value for %q must be a string", rawModelID)
		}
		modelID, displayName, err := normalizeModelDisplayNameEntry(rawModelID, value)
		if err != nil {
			return err
		}
		if modelID == "" || displayName == "" {
			continue
		}
		normalized[modelID] = displayName
	}
	if len(normalized) == 0 {
		delete(credentials, credKeyModelDisplayNames)
		return nil
	}
	credentials[credKeyModelDisplayNames] = normalized
	return nil
}

// ModelDisplayNamesFromAccounts merges per-account overrides into a single map.
// Accounts are inspected in ascending id order so the result is deterministic
// when several accounts define the same model id; the lowest account id wins.
func ModelDisplayNamesFromAccounts(accounts []Account) map[string]string {
	if len(accounts) == 0 {
		return nil
	}
	ordered := make([]*Account, 0, len(accounts))
	for i := range accounts {
		ordered = append(ordered, &accounts[i])
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	var merged map[string]string
	for _, account := range ordered {
		for modelID, displayName := range account.GetModelDisplayNames() {
			if merged == nil {
				merged = make(map[string]string)
			}
			if _, exists := merged[modelID]; !exists {
				merged[modelID] = displayName
			}
		}
	}
	return merged
}

// RewriteModelDisplayNames overlays per-model display names onto a serialized
// model catalog. It understands both the Codex manifest envelope (top-level
// "models") and the OpenAI-compatible list envelope (top-level "data"), leaving
// bodies it cannot parse untouched. "id" is used for the OpenAI list shape and
// "slug" for the Codex manifest shape.
func RewriteModelDisplayNames(body []byte, overrides map[string]string) []byte {
	if len(overrides) == 0 || len(body) == 0 {
		return body
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	changedAny := false
	for _, key := range []string{"models", "data"} {
		rawModels, ok := envelope[key]
		if !ok {
			continue
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(rawModels, &entries); err != nil {
			continue
		}
		changed := false
		for i, entry := range entries {
			rewritten, entryChanged := applyModelDisplayNameToDescriptor(entry, overrides)
			if entryChanged {
				entries[i] = rewritten
				changed = true
			}
		}
		if !changed {
			continue
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			return body
		}
		envelope[key] = encoded
		changedAny = true
	}
	if !changedAny {
		return body
	}
	rewritten, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return rewritten
}

// applyModelDisplayNameToDescriptor overlays an override onto one serialized
// model entry, keyed by its slug/id. It reports whether the entry was changed.
func applyModelDisplayNameToDescriptor(raw json.RawMessage, overrides map[string]string) (json.RawMessage, bool) {
	if len(overrides) == 0 || len(raw) == 0 {
		return raw, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw, false
	}
	modelID := rawJSONString(fields["slug"])
	if modelID == "" {
		modelID = rawJSONString(fields["id"])
	}
	override, ok := overrides[modelID]
	if modelID == "" || !ok {
		return raw, false
	}
	encoded, err := json.Marshal(override)
	if err != nil {
		return raw, false
	}
	fields["display_name"] = encoded
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return raw, false
	}
	return rewritten, true
}

func rawJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
