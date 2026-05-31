package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	passiveQuotaUpdateMinInterval = 30 * time.Second
	quotaSnapshotFreshTTL         = 10 * time.Minute
	quotaSnapshotStaleGraceTTL    = 30 * time.Minute
	codexWhamUsageQuotaSource     = "codex_wham_usage"
)

var (
	fiveHourPercentHeaders = []string{
		"x-cliproxy-quota-5h-remaining-percent",
		"x-cli-proxy-quota-5h-remaining-percent",
		"x-quota-5h-remaining-percent",
		"x-quota-5-hour-remaining-percent",
		"x-quota-five-hour-remaining-percent",
		"x-ratelimit-5h-remaining-percent",
		"x-ratelimit-remaining-5h-percent",
	}
	fiveHourRemainingHeaders = []string{
		"x-cliproxy-quota-5h-remaining",
		"x-cli-proxy-quota-5h-remaining",
		"x-quota-5h-remaining",
		"x-quota-5-hour-remaining",
		"x-quota-five-hour-remaining",
		"x-ratelimit-5h-remaining",
		"x-ratelimit-remaining-5h",
		"x-ratelimit-remaining-tokens-5h",
	}
	fiveHourLimitHeaders = []string{
		"x-cliproxy-quota-5h-limit",
		"x-cli-proxy-quota-5h-limit",
		"x-quota-5h-limit",
		"x-quota-5-hour-limit",
		"x-quota-five-hour-limit",
		"x-ratelimit-5h-limit",
		"x-ratelimit-limit-5h",
		"x-ratelimit-limit-tokens-5h",
	}
	sevenDayPercentHeaders = []string{
		"x-cliproxy-quota-7d-remaining-percent",
		"x-cli-proxy-quota-7d-remaining-percent",
		"x-quota-7d-remaining-percent",
		"x-quota-7-day-remaining-percent",
		"x-quota-seven-day-remaining-percent",
		"x-ratelimit-7d-remaining-percent",
		"x-ratelimit-remaining-7d-percent",
	}
	sevenDayRemainingHeaders = []string{
		"x-cliproxy-quota-7d-remaining",
		"x-cli-proxy-quota-7d-remaining",
		"x-quota-7d-remaining",
		"x-quota-7-day-remaining",
		"x-quota-seven-day-remaining",
		"x-ratelimit-7d-remaining",
		"x-ratelimit-remaining-7d",
		"x-ratelimit-remaining-tokens-7d",
	}
	sevenDayLimitHeaders = []string{
		"x-cliproxy-quota-7d-limit",
		"x-cli-proxy-quota-7d-limit",
		"x-quota-7d-limit",
		"x-quota-7-day-limit",
		"x-quota-seven-day-limit",
		"x-ratelimit-7d-limit",
		"x-ratelimit-limit-7d",
		"x-ratelimit-limit-tokens-7d",
	}
)

// QuotaSnapshotUpdate is a quota observation parsed from an upstream signal.
type QuotaSnapshotUpdate struct {
	FiveHourRemainingKnown   bool
	FiveHourRemainingPercent float64
	SevenDayRemainingKnown   bool
	SevenDayRemainingPercent float64
	ObservedAt               time.Time
	Source                   string
}

type codexWhamUsagePayload struct {
	RateLimit struct {
		PrimaryWindow   codexWhamUsageWindow `json:"primary_window"`
		SecondaryWindow codexWhamUsageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type codexWhamUsageWindow struct {
	UsedPercent json.RawMessage `json:"used_percent"`
}

// ParseCodexWhamUsageQuotaSnapshot parses ChatGPT wham/usage quota usage into remaining percentages.
func ParseCodexWhamUsageQuotaSnapshot(body []byte) (QuotaSnapshotUpdate, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return QuotaSnapshotUpdate{}, false
	}

	var payload codexWhamUsagePayload
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return QuotaSnapshotUpdate{}, false
	}

	update := QuotaSnapshotUpdate{
		ObservedAt: time.Now().UTC(),
		Source:     codexWhamUsageQuotaSource,
	}
	if percent, ok := parseCodexWhamUsageRemainingPercent(payload.RateLimit.PrimaryWindow.UsedPercent); ok {
		update.FiveHourRemainingKnown = true
		update.FiveHourRemainingPercent = normalizedQuotaPercent(percent)
	}
	if percent, ok := parseCodexWhamUsageRemainingPercent(payload.RateLimit.SecondaryWindow.UsedPercent); ok {
		update.SevenDayRemainingKnown = true
		update.SevenDayRemainingPercent = normalizedQuotaPercent(percent)
	}
	if !update.FiveHourRemainingKnown && !update.SevenDayRemainingKnown {
		return QuotaSnapshotUpdate{}, false
	}
	return update, true
}

func parseCodexWhamUsageRemainingPercent(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	used, err := number.Float64()
	if err != nil || math.IsNaN(used) || math.IsInf(used, 0) {
		return 0, false
	}
	return 100 - used, true
}

// ParseCodexWhamUsageMetadata extracts safe account identity observations from wham/usage.
func ParseCodexWhamUsageMetadata(reqHeaders http.Header, respBody []byte) map[string]string {
	metadata := make(map[string]string)
	if accountID := strings.TrimSpace(reqHeaders.Get("Chatgpt-Account-Id")); accountID != "" {
		metadata["codex_account_id"] = accountID
	}

	var payload any
	decoder := json.NewDecoder(bytes.NewReader(respBody))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return metadata
	}
	if _, exists := metadata["codex_account_id"]; !exists {
		if accountID := firstJSONScalarByKey(payload, "account_id"); accountID != "" {
			metadata["codex_account_id"] = accountID
		}
	}
	if userID := firstJSONScalarByKey(payload, "user_id"); userID != "" {
		metadata["codex_user_id"] = userID
	}
	if email := firstJSONScalarByKey(payload, "email"); email != "" {
		metadata["email"] = email
	}
	return metadata
}

func firstJSONScalarByKey(value any, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		for candidateKey, candidateValue := range typed {
			if strings.EqualFold(strings.TrimSpace(candidateKey), key) {
				if scalar := jsonScalarString(candidateValue); scalar != "" {
					return scalar
				}
			}
		}
		for _, candidateValue := range typed {
			if scalar := firstJSONScalarByKey(candidateValue, key); scalar != "" {
				return scalar
			}
		}
	case []any:
		for _, candidateValue := range typed {
			if scalar := firstJSONScalarByKey(candidateValue, key); scalar != "" {
				return scalar
			}
		}
	}
	return ""
}

func jsonScalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func parseQuotaSnapshotFromHeaders(provider, model string, headers http.Header) (QuotaSnapshotUpdate, bool) {
	_ = provider
	_ = model
	if len(headers) == 0 {
		return QuotaSnapshotUpdate{}, false
	}

	update := QuotaSnapshotUpdate{
		ObservedAt: time.Now().UTC(),
		Source:     "headers",
	}
	if percent, ok := parseQuotaSnapshotField(headers, fiveHourPercentHeaders, fiveHourRemainingHeaders, fiveHourLimitHeaders); ok {
		update.FiveHourRemainingKnown = true
		update.FiveHourRemainingPercent = normalizedQuotaPercent(percent)
	}
	if percent, ok := parseQuotaSnapshotField(headers, sevenDayPercentHeaders, sevenDayRemainingHeaders, sevenDayLimitHeaders); ok {
		update.SevenDayRemainingKnown = true
		update.SevenDayRemainingPercent = normalizedQuotaPercent(percent)
	}
	if !update.FiveHourRemainingKnown && !update.SevenDayRemainingKnown {
		return QuotaSnapshotUpdate{}, false
	}
	return update, true
}

// UpdateQuotaSnapshotFromHeaders passively merges explicit upstream quota headers into auth state.
func (m *Manager) UpdateQuotaSnapshotFromHeaders(ctx context.Context, authID, provider, model string, headers http.Header) bool {
	if m == nil || strings.TrimSpace(authID) == "" {
		return false
	}
	update, ok := parseQuotaSnapshotFromHeaders(provider, model, headers)
	if !ok {
		return false
	}
	if update.ObservedAt.IsZero() {
		update.ObservedAt = time.Now().UTC()
	}

	var authSnapshot *Auth
	m.mu.Lock()
	auth, ok := m.auths[authID]
	if ok && auth != nil {
		target := &auth.Quota
		modelKey := strings.TrimSpace(model)
		if modelKey != "" {
			state := ensureModelState(auth, modelKey)
			state.UpdatedAt = update.ObservedAt
			target = &state.Quota
		}
		if !shouldThrottleQuotaSnapshotUpdate(*target, update) {
			applyQuotaSnapshotUpdate(target, update)
			auth.UpdatedAt = update.ObservedAt
			_ = m.persist(ctx, auth)
			authSnapshot = auth.Clone()
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil && authSnapshot != nil {
		m.scheduler.upsertAuth(authSnapshot)
	}
	return authSnapshot != nil
}

// UpdateAccountQuotaSnapshot merges an account-level quota observation into auth state.
func (m *Manager) UpdateAccountQuotaSnapshot(ctx context.Context, authID string, update QuotaSnapshotUpdate) (bool, error) {
	return m.UpdateAccountQuotaSnapshotWithMetadata(ctx, authID, update, nil)
}

// UpdateAccountQuotaSnapshotWithMetadata merges account quota and safe metadata observations into auth state.
func (m *Manager) UpdateAccountQuotaSnapshotWithMetadata(ctx context.Context, authID string, update QuotaSnapshotUpdate, metadata map[string]string) (bool, error) {
	if m == nil || strings.TrimSpace(authID) == "" || (!quotaSnapshotUpdateHasFields(update) && len(metadata) == 0) {
		return false, nil
	}
	if update.ObservedAt.IsZero() {
		update.ObservedAt = time.Now().UTC()
	}

	var authSnapshot *Auth
	var errPersist error
	m.mu.Lock()
	auth, ok := m.auths[authID]
	if ok && auth != nil {
		changed := false
		if quotaSnapshotUpdateHasFields(update) {
			applyQuotaSnapshotUpdate(&auth.Quota, update)
			changed = true
		}
		for key, value := range metadata {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			if auth.Metadata == nil {
				auth.Metadata = make(map[string]any)
			}
			auth.Metadata[key] = value
			changed = true
		}
		if changed {
			auth.UpdatedAt = update.ObservedAt
			errPersist = m.persist(ctx, auth)
			authSnapshot = auth.Clone()
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil && authSnapshot != nil {
		m.scheduler.upsertAuth(authSnapshot)
	}
	if authSnapshot != nil {
		m.queueCodexQuotaRefreshReschedule(authID)
	}
	return authSnapshot != nil, errPersist
}

func quotaSnapshotUpdateHasFields(update QuotaSnapshotUpdate) bool {
	return update.FiveHourRemainingKnown || update.SevenDayRemainingKnown
}

func parseQuotaSnapshotField(headers http.Header, percentHeaders, remainingHeaders, limitHeaders []string) (float64, bool) {
	if raw, ok := headerFirstValue(headers, percentHeaders...); ok {
		return parseQuotaPercent(raw)
	}
	remainingRaw, okRemaining := headerFirstValue(headers, remainingHeaders...)
	limitRaw, okLimit := headerFirstValue(headers, limitHeaders...)
	if !okRemaining || !okLimit {
		return 0, false
	}
	remaining, okRemaining := parseQuotaNumber(remainingRaw)
	limit, okLimit := parseQuotaNumber(limitRaw)
	if !okRemaining || !okLimit || limit <= 0 {
		return 0, false
	}
	return (remaining / limit) * 100, true
}

func headerFirstValue(headers http.Header, names ...string) (string, bool) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for key, values := range headers {
			if !strings.EqualFold(key, name) {
				continue
			}
			for _, value := range values {
				value = strings.TrimSpace(value)
				if value != "" {
					return value, true
				}
			}
		}
	}
	return "", false
}

func parseQuotaPercent(raw string) (float64, bool) {
	return parseQuotaNumber(raw)
}

func parseQuotaNumber(raw string) (float64, bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, "%")
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func applyQuotaSnapshotUpdate(quota *QuotaState, update QuotaSnapshotUpdate) {
	if quota == nil {
		return
	}
	if update.FiveHourRemainingKnown {
		quota.FiveHourRemainingKnown = true
		quota.FiveHourRemainingPercent = normalizedQuotaPercent(update.FiveHourRemainingPercent)
	}
	if update.SevenDayRemainingKnown {
		quota.SevenDayRemainingKnown = true
		quota.SevenDayRemainingPercent = normalizedQuotaPercent(update.SevenDayRemainingPercent)
	}
	if update.ObservedAt.IsZero() {
		update.ObservedAt = time.Now().UTC()
	}
	quota.SnapshotUpdatedAt = update.ObservedAt
}

func shouldThrottleQuotaSnapshotUpdate(current QuotaState, update QuotaSnapshotUpdate) bool {
	if update.ObservedAt.IsZero() || current.SnapshotUpdatedAt.IsZero() {
		return false
	}
	if quotaSnapshotUpdateIsLow(update) || quotaSnapshotUpdateAddsField(current, update) {
		return false
	}
	return update.ObservedAt.Sub(current.SnapshotUpdatedAt) < passiveQuotaUpdateMinInterval
}

func quotaSnapshotUpdateAddsField(current QuotaState, update QuotaSnapshotUpdate) bool {
	return (update.FiveHourRemainingKnown && !current.FiveHourRemainingKnown) ||
		(update.SevenDayRemainingKnown && !current.SevenDayRemainingKnown)
}

func quotaSnapshotUpdateIsLow(update QuotaSnapshotUpdate) bool {
	if update.FiveHourRemainingKnown && normalizedQuotaPercent(update.FiveHourRemainingPercent) < stickyQuotaFiveHourMinThreshold {
		return true
	}
	return update.SevenDayRemainingKnown && normalizedQuotaPercent(update.SevenDayRemainingPercent) < stickyQuotaSevenDayMin
}

func clearQuotaCooldownFields(quota *QuotaState) {
	if quota == nil {
		return
	}
	quota.Exceeded = false
	quota.Reason = ""
	quota.NextRecoverAt = time.Time{}
	quota.BackoffLevel = 0
}

func quotaSnapshotFreshnessPenalty(quota QuotaState, now time.Time) (int, bool) {
	if !quotaHasSnapshot(quota) {
		return 2, true
	}
	if quota.SnapshotUpdatedAt.IsZero() {
		return 1, true
	}
	age := now.Sub(quota.SnapshotUpdatedAt)
	if age <= quotaSnapshotFreshTTL {
		return 0, true
	}
	if age <= quotaSnapshotStaleGraceTTL {
		return 1, true
	}
	return 0, false
}
