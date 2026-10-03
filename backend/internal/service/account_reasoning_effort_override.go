package service

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// reasoning_effort_overrides lets an administrator declare, per account, which
// reasoning-effort levels a specific upstream model really accepts. It is the
// manual counterpart of the synced UpstreamModelMetadata snapshot: sync
// snapshots are overwritten on the next refresh, so hand-written capability
// declarations live in their own credential field.
//
// Shape (keyed by the public model id, falling back to the mapped upstream id):
//
//	"reasoning_effort_overrides": {
//	  "glm-5.3": { "default": "high", "levels": ["low", "high", "max"] }
//	}
//
// Precedence when building a Codex manifest:
// account override > synced upstream metadata > code family heuristics.
const (
	credKeyReasoningEffortOverrides = "reasoning_effort_overrides"

	maxReasoningEffortOverrideEntries     = 256
	maxReasoningEffortOverrideModelIDLen  = 200
	maxReasoningEffortOverrideLevelsCount = 8
)

// ReasoningEffortOverride is the per-model effort capability an administrator
// declared for one account.
type ReasoningEffortOverride struct {
	Default string   `json:"default,omitempty"`
	Levels  []string `json:"levels"`
}

// GetReasoningEffortOverrides returns the effective per-model overrides. The
// result is cached against the credentials pointer so repeated manifest builds
// validate each credentials map once.
func (a *Account) GetReasoningEffortOverrides() map[string]ReasoningEffortOverride {
	if a == nil || a.Credentials == nil {
		return nil
	}
	raw, ok := a.Credentials[credKeyReasoningEffortOverrides].(map[string]any)
	if !ok {
		return nil
	}

	credentialsPtr := mapPtr(a.Credentials)
	rawPtr := mapPtr(raw)
	rawLen := len(raw)
	rawSig := reasoningEffortOverridesSignature(raw)

	if a.reasoningEffortOverrideCacheReady &&
		a.reasoningEffortOverrideCacheCredentialsPtr == credentialsPtr &&
		a.reasoningEffortOverrideCacheRawPtr == rawPtr &&
		a.reasoningEffortOverrideCacheRawLen == rawLen &&
		a.reasoningEffortOverrideCacheRawSig == rawSig {
		return a.reasoningEffortOverrideCache
	}

	overrides := resolveReasoningEffortOverrides(raw)
	a.reasoningEffortOverrideCache = overrides
	a.reasoningEffortOverrideCacheReady = true
	a.reasoningEffortOverrideCacheCredentialsPtr = credentialsPtr
	a.reasoningEffortOverrideCacheRawPtr = rawPtr
	a.reasoningEffortOverrideCacheRawLen = rawLen
	a.reasoningEffortOverrideCacheRawSig = rawSig
	return overrides
}

// ReasoningEffortOverrideFor resolves an override for one model. The public
// (requested) model id wins over the mapped upstream id so an alias can carry
// its own capability declaration.
func (a *Account) ReasoningEffortOverrideFor(modelIDs ...string) (ReasoningEffortOverride, bool) {
	overrides := a.GetReasoningEffortOverrides()
	if len(overrides) == 0 {
		return ReasoningEffortOverride{}, false
	}
	for _, modelID := range modelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if override, ok := overrides[modelID]; ok {
			return override, true
		}
	}
	return ReasoningEffortOverride{}, false
}

// applyReasoningEffortOverrideToMetadata folds a manual override into a
// metadata snapshot. The override is authoritative: it can declare reasoning
// for a model whose snapshot said nothing, and it can narrow or widen levels.
func (a *Account) applyReasoningEffortOverrideToMetadata(
	metadata UpstreamModelMetadata,
	modelIDs ...string,
) UpstreamModelMetadata {
	override, ok := a.ReasoningEffortOverrideFor(modelIDs...)
	if !ok {
		return metadata
	}
	enabled := true
	metadata.Reasoning = &enabled
	metadata.SupportedReasoningLevels = append([]string(nil), override.Levels...)
	metadata.DefaultReasoningLevel = override.Default
	return metadata
}

// resolveReasoningEffortOverrides defensively re-validates raw overrides so
// accounts persisted before this feature (or hand-edited credentials) cannot
// inject unusable levels into a client manifest.
func resolveReasoningEffortOverrides(raw map[string]any) map[string]ReasoningEffortOverride {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]ReasoningEffortOverride, len(raw))
	for modelID, rawEntry := range raw {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" || len(modelID) > maxReasoningEffortOverrideModelIDLen {
			continue
		}
		override, ok := normalizeReasoningEffortOverrideValue(rawEntry)
		if !ok {
			continue
		}
		result[modelID] = override
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeReasoningEffortOverrideValue(raw any) (ReasoningEffortOverride, bool) {
	entry, ok := raw.(map[string]any)
	if !ok {
		return ReasoningEffortOverride{}, false
	}
	levels := normalizeReasoningLevels(stringSliceFromRawValue(entry["levels"]))
	if len(levels) == 0 {
		return ReasoningEffortOverride{}, false
	}
	if len(levels) > maxReasoningEffortOverrideLevelsCount {
		levels = levels[:maxReasoningEffortOverrideLevelsCount]
	}
	defaultLevel := normalizeReasoningLevel(stringValueFromRaw(entry["default"]))
	if !stringSliceContains(levels, defaultLevel) {
		defaultLevel = levels[0]
	}
	return ReasoningEffortOverride{Default: defaultLevel, Levels: levels}, true
}

func stringValueFromRaw(raw any) string {
	value, _ := raw.(string)
	return value
}

func stringSliceFromRawValue(raw any) []string {
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func reasoningEffortOverridesSignature(raw map[string]any) uint64 {
	if len(raw) == 0 {
		return 0
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	h := fnv.New64a()
	for _, key := range keys {
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{0})
		encoded, err := json.Marshal(raw[key])
		if err != nil {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write(encoded)
		}
		_, _ = h.Write([]byte{0xff})
	}
	return h.Sum64()
}

// applyReasoningEffortOverrideToModelFields rewrites the effort fields of one
// serialized Codex manifest entry. The override replaces both the default level
// and the supported scale, so a client picker matches the declared capability.
func applyReasoningEffortOverrideToModelFields(
	model map[string]json.RawMessage,
	override ReasoningEffortOverride,
) (bool, error) {
	if len(model) == 0 || len(override.Levels) == 0 {
		return false, nil
	}
	changed := false

	defaultLevel := override.Default
	if !stringSliceContains(override.Levels, defaultLevel) {
		defaultLevel = override.Levels[0]
	}
	encodedDefault, err := json.Marshal(defaultLevel)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(bytes.TrimSpace(model["default_reasoning_level"]), encodedDefault) {
		model["default_reasoning_level"] = encodedDefault
		changed = true
	}

	levels := make([]configuredCodexReasoningLevel, 0, len(override.Levels))
	for _, level := range override.Levels {
		levels = append(levels, configuredCodexReasoningLevel{
			Effort:      level,
			Description: configuredCodexReasoningLevelDescription(level),
		})
	}
	encodedLevels, err := json.Marshal(levels)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(bytes.TrimSpace(model["supported_reasoning_levels"]), encodedLevels) {
		model["supported_reasoning_levels"] = encodedLevels
		changed = true
	}
	return changed, nil
}

// NormalizeReasoningEffortOverrideCredentials validates and canonicalizes the
// optional reasoning_effort_overrides field. A nil/absent field is a no-op.
func NormalizeReasoningEffortOverrideCredentials(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	raw, ok := credentials[credKeyReasoningEffortOverrides]
	if !ok || raw == nil {
		return nil
	}
	entries, ok := raw.(map[string]any)
	if !ok {
		return infraerrors.New(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
			"reasoning_effort_overrides must be an object of model id to effort levels")
	}
	if len(entries) > maxReasoningEffortOverrideEntries {
		return infraerrors.Newf(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
			"reasoning_effort_overrides supports at most %d entries", maxReasoningEffortOverrideEntries)
	}

	normalized := make(map[string]any, len(entries))
	for rawModelID, rawEntry := range entries {
		modelID := strings.TrimSpace(rawModelID)
		if modelID == "" {
			continue
		}
		if len(modelID) > maxReasoningEffortOverrideModelIDLen {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
				"reasoning_effort_overrides model id %q exceeds %d characters", modelID, maxReasoningEffortOverrideModelIDLen)
		}
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
				"reasoning_effort_overrides value for %q must be an object", modelID)
		}
		rawLevels := stringSliceFromRawValue(entry["levels"])
		if len(rawLevels) > maxReasoningEffortOverrideLevelsCount {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
				"reasoning_effort_overrides levels for %q supports at most %d entries", modelID, maxReasoningEffortOverrideLevelsCount)
		}
		levels := normalizeReasoningLevels(rawLevels)
		if len(levels) == 0 {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
				"reasoning_effort_overrides levels for %q must list at least one supported effort", modelID)
		}
		defaultLevel := normalizeReasoningLevel(stringValueFromRaw(entry["default"]))
		if defaultLevel != "" && !stringSliceContains(levels, defaultLevel) {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
				"reasoning_effort_overrides default %q for %q is not one of the declared levels", defaultLevel, modelID)
		}
		if defaultLevel == "" {
			defaultLevel = levels[0]
		}
		normalized[modelID] = map[string]any{
			"default": defaultLevel,
			"levels":  levels,
		}
	}
	if len(normalized) == 0 {
		delete(credentials, credKeyReasoningEffortOverrides)
		return nil
	}
	credentials[credKeyReasoningEffortOverrides] = normalized
	return nil
}
