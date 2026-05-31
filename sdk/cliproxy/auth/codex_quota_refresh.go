package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	codexWhamUsageURL               = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaRefreshFailureBackoff = 5 * time.Minute
	codexQuotaMetadataStatusKey     = "codex_quota_last_status"
	codexQuotaMetadataSuccessAtKey  = "codex_quota_last_success_at"
	codexQuotaMetadataFailureAtKey  = "codex_quota_last_failure_at"
	codexQuotaMetadataErrorKey      = "codex_quota_last_error"
	codexQuotaMetadataNextRetryKey  = "codex_quota_next_retry_after"
	codexQuotaMetadataStatusCodeKey = "codex_quota_last_status_code"
	codexQuotaRefreshStatusSuccess  = "success"
	codexQuotaRefreshStatusFailure  = "failure"
)

// CodexQuotaRefreshResult summarizes one proactive wham/usage refresh attempt.
type CodexQuotaRefreshResult struct {
	AuthID         string     `json:"auth_id"`
	AuthIndex      string     `json:"auth_index,omitempty"`
	Success        bool       `json:"success"`
	StatusCode     int        `json:"status_code,omitempty"`
	Quota          QuotaState `json:"quota"`
	Error          string     `json:"error,omitempty"`
	RefreshedAt    time.Time  `json:"refreshed_at,omitempty"`
	NextRetryAfter time.Time  `json:"next_retry_after,omitempty"`
}

// RefreshCodexQuota proactively fetches ChatGPT wham/usage for one Codex auth and stores account quota.
func (m *Manager) RefreshCodexQuota(ctx context.Context, authID string) (CodexQuotaRefreshResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	authID = strings.TrimSpace(authID)
	result := CodexQuotaRefreshResult{AuthID: authID}
	if m == nil {
		err := fmt.Errorf("auth manager is nil")
		result.Error = err.Error()
		return result, err
	}
	auth, ok := m.GetByID(authID)
	if !ok || auth == nil {
		err := fmt.Errorf("auth not found")
		result.Error = err.Error()
		return result, err
	}
	auth.EnsureIndex()
	result.AuthID = auth.ID
	result.AuthIndex = auth.Index
	result.Quota = auth.Quota
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		err := fmt.Errorf("auth provider is not codex")
		result.Error = err.Error()
		return result, err
	}
	if !codexQuotaAuthHasToken(auth) {
		err := fmt.Errorf("codex quota refresh token not found")
		return m.recordCodexQuotaRefreshFailure(ctx, auth.ID, result, 0, err, time.Now().UTC())
	}

	headers := http.Header{}
	if accountID := codexQuotaAccountID(auth); accountID != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, codexWhamUsageURL, nil)
	if errReq != nil {
		return m.recordCodexQuotaRefreshFailure(ctx, auth.ID, result, 0, errReq, time.Now().UTC())
	}
	req.Header = headers

	resp, errDo := m.HttpRequest(ctx, auth, req)
	if errDo != nil {
		return m.recordCodexQuotaRefreshFailure(ctx, auth.ID, result, 0, errDo, time.Now().UTC())
	}
	if resp == nil {
		err := fmt.Errorf("codex quota refresh returned nil response")
		return m.recordCodexQuotaRefreshFailure(ctx, auth.ID, result, 0, err, time.Now().UTC())
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return m.recordCodexQuotaRefreshFailure(ctx, auth.ID, result, resp.StatusCode, errRead, time.Now().UTC())
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("codex quota refresh failed with status %d", resp.StatusCode)
		return m.recordCodexQuotaRefreshFailure(ctx, auth.ID, result, resp.StatusCode, err, time.Now().UTC())
	}

	update, okParse := ParseCodexWhamUsageQuotaSnapshot(body)
	if !okParse {
		err := fmt.Errorf("codex quota refresh response did not contain quota snapshot")
		return m.recordCodexQuotaRefreshFailure(ctx, auth.ID, result, resp.StatusCode, err, time.Now().UTC())
	}
	now := time.Now().UTC()
	update.ObservedAt = now
	metadata := ParseCodexWhamUsageMetadata(req.Header, body)
	metadata[codexQuotaMetadataStatusKey] = codexQuotaRefreshStatusSuccess
	metadata[codexQuotaMetadataSuccessAtKey] = now.Format(time.RFC3339Nano)
	metadata[codexQuotaMetadataStatusCodeKey] = fmt.Sprintf("%d", resp.StatusCode)

	_, errUpdate := m.UpdateAccountQuotaSnapshotWithMetadata(ctx, auth.ID, update, metadata)
	updated, _ := m.GetByID(auth.ID)
	if updated != nil {
		result.Quota = updated.Quota
	}
	result.Success = errUpdate == nil
	result.StatusCode = resp.StatusCode
	result.RefreshedAt = now
	if errUpdate != nil {
		result.Error = errUpdate.Error()
		return result, errUpdate
	}
	return result, nil
}

func (m *Manager) recordCodexQuotaRefreshFailure(ctx context.Context, authID string, result CodexQuotaRefreshResult, statusCode int, cause error, now time.Time) (CodexQuotaRefreshResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	errText := ""
	if cause != nil {
		errText = cause.Error()
	}
	nextRetry := now.Add(codexQuotaRefreshFailureBackoff)
	metadata := map[string]string{
		codexQuotaMetadataStatusKey:     codexQuotaRefreshStatusFailure,
		codexQuotaMetadataFailureAtKey:  now.Format(time.RFC3339Nano),
		codexQuotaMetadataErrorKey:      errText,
		codexQuotaMetadataNextRetryKey:  nextRetry.Format(time.RFC3339Nano),
		codexQuotaMetadataStatusCodeKey: fmt.Sprintf("%d", statusCode),
	}
	_, errUpdate := m.UpdateAccountQuotaSnapshotWithMetadata(ctx, authID, QuotaSnapshotUpdate{
		ObservedAt: now,
		Source:     codexWhamUsageQuotaSource,
	}, metadata)
	updated, _ := m.GetByID(authID)
	if updated != nil {
		result.Quota = updated.Quota
	}
	result.Success = false
	result.StatusCode = statusCode
	result.Error = errText
	result.RefreshedAt = now
	result.NextRetryAfter = nextRetry
	if errUpdate != nil && cause != nil {
		return result, fmt.Errorf("%w; failed to persist codex quota refresh failure: %v", cause, errUpdate)
	}
	if errUpdate != nil {
		return result, errUpdate
	}
	if cause != nil {
		return result, cause
	}
	return result, nil
}

func codexQuotaAccountID(auth *Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range []string{"codex_account_id", "account_id"} {
		if value, ok := auth.Metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func codexQuotaAuthHasToken(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Metadata == nil {
		return false
	}
	if token, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(token) != "" {
		return true
	}
	return false
}
