package auth

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const persistentQuotaMetadataKey = "quota"

// MergePersistentRuntimeMetadata mirrors safe runtime state into auth metadata before persistence.
func MergePersistentRuntimeMetadata(auth *Auth) {
	if auth == nil {
		return
	}
	if !quotaStateHasPersistentFields(auth.Quota) {
		if auth.Metadata != nil {
			delete(auth.Metadata, persistentQuotaMetadataKey)
		}
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[persistentQuotaMetadataKey] = persistentQuotaMetadata(auth.Quota)
}

// ApplyPersistentRuntimeMetadata restores safe runtime state from auth metadata after loading JSON.
func ApplyPersistentRuntimeMetadata(auth *Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}
	if quota, ok := quotaStateFromPersistentMetadata(auth.Metadata[persistentQuotaMetadataKey]); ok {
		auth.Quota = quota
	}
}

func quotaStateHasPersistentFields(quota QuotaState) bool {
	return quota.Exceeded ||
		strings.TrimSpace(quota.Reason) != "" ||
		!quota.NextRecoverAt.IsZero() ||
		quota.BackoffLevel != 0 ||
		quota.FiveHourRemainingKnown ||
		quota.FiveHourRemainingPercent != 0 ||
		quota.SevenDayRemainingKnown ||
		quota.SevenDayRemainingPercent != 0 ||
		!quota.SnapshotUpdatedAt.IsZero()
}

func persistentQuotaMetadata(quota QuotaState) map[string]any {
	out := make(map[string]any)
	if quota.Exceeded {
		out["exceeded"] = quota.Exceeded
	}
	if reason := strings.TrimSpace(quota.Reason); reason != "" {
		out["reason"] = reason
	}
	if !quota.NextRecoverAt.IsZero() {
		out["next_recover_at"] = quota.NextRecoverAt.UTC().Format(time.RFC3339Nano)
	}
	if quota.BackoffLevel != 0 {
		out["backoff_level"] = quota.BackoffLevel
	}
	if quota.FiveHourRemainingKnown {
		out["five_hour_remaining_known"] = true
	}
	if quota.FiveHourRemainingKnown || quota.FiveHourRemainingPercent != 0 {
		out["five_hour_remaining_percent"] = quota.FiveHourRemainingPercent
	}
	if quota.SevenDayRemainingKnown {
		out["seven_day_remaining_known"] = true
	}
	if quota.SevenDayRemainingKnown || quota.SevenDayRemainingPercent != 0 {
		out["seven_day_remaining_percent"] = quota.SevenDayRemainingPercent
	}
	if !quota.SnapshotUpdatedAt.IsZero() {
		out["snapshot_updated_at"] = quota.SnapshotUpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func quotaStateFromPersistentMetadata(value any) (QuotaState, bool) {
	switch typed := value.(type) {
	case nil:
		return QuotaState{}, false
	case QuotaState:
		return typed, quotaStateHasPersistentFields(typed)
	case map[string]any:
		return quotaStateFromPersistentMap(typed)
	case map[string]string:
		asAny := make(map[string]any, len(typed))
		for key, val := range typed {
			asAny[key] = val
		}
		return quotaStateFromPersistentMap(asAny)
	case json.RawMessage:
		var parsed map[string]any
		if err := json.Unmarshal(typed, &parsed); err != nil {
			return QuotaState{}, false
		}
		return quotaStateFromPersistentMap(parsed)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return QuotaState{}, false
		}
		var parsed map[string]any
		if err = json.Unmarshal(raw, &parsed); err != nil {
			return QuotaState{}, false
		}
		return quotaStateFromPersistentMap(parsed)
	}
}

func quotaStateFromPersistentMap(raw map[string]any) (QuotaState, bool) {
	if len(raw) == 0 {
		return QuotaState{}, false
	}
	var quota QuotaState
	observed := false
	if value, ok := persistentBool(raw, "exceeded"); ok {
		quota.Exceeded = value
		observed = true
	}
	if value, ok := persistentString(raw, "reason"); ok {
		quota.Reason = value
		observed = true
	}
	if value, ok := persistentTime(raw, "next_recover_at", "nextRecoverAt"); ok {
		quota.NextRecoverAt = value
		observed = true
	}
	if value, ok := persistentInt(raw, "backoff_level", "backoffLevel"); ok {
		quota.BackoffLevel = value
		observed = true
	}
	if value, ok := persistentBool(raw, "five_hour_remaining_known", "fiveHourRemainingKnown"); ok {
		quota.FiveHourRemainingKnown = value
		observed = true
	}
	if value, ok := persistentFloat(raw, "five_hour_remaining_percent", "fiveHourRemainingPercent"); ok {
		quota.FiveHourRemainingPercent = value
		observed = true
	}
	if value, ok := persistentBool(raw, "seven_day_remaining_known", "sevenDayRemainingKnown"); ok {
		quota.SevenDayRemainingKnown = value
		observed = true
	}
	if value, ok := persistentFloat(raw, "seven_day_remaining_percent", "sevenDayRemainingPercent"); ok {
		quota.SevenDayRemainingPercent = value
		observed = true
	}
	if value, ok := persistentTime(raw, "snapshot_updated_at", "snapshotUpdatedAt"); ok {
		quota.SnapshotUpdatedAt = value
		observed = true
	}
	return quota, observed
}

func persistentValue(raw map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func persistentBool(raw map[string]any, keys ...string) (bool, bool) {
	value, ok := persistentValue(raw, keys...)
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}

func persistentString(raw map[string]any, keys ...string) (string, bool) {
	value, ok := persistentValue(raw, keys...)
	if !ok {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), strings.TrimSpace(typed) != ""
	default:
		return "", false
	}
}

func persistentInt(raw map[string]any, keys ...string) (int, bool) {
	value, ok := persistentValue(raw, keys...)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func persistentFloat(raw map[string]any, keys ...string) (float64, bool) {
	value, ok := persistentValue(raw, keys...)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(typed, "%")), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func persistentTime(raw map[string]any, keys ...string) (time.Time, bool) {
	value, ok := persistentValue(raw, keys...)
	if !ok {
		return time.Time{}, false
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), !typed.IsZero()
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.HasPrefix(trimmed, "0001-01-01") {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed.UTC(), true
			}
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}
