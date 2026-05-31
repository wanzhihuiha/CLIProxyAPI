package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type codexQuotaRefreshTestExecutor struct {
	statusCode int
	body       string
	err        error

	mu       sync.Mutex
	requests []*http.Request
}

func (e *codexQuotaRefreshTestExecutor) Identifier() string { return "codex" }

func (e *codexQuotaRefreshTestExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	if req == nil {
		return nil
	}
	if auth != nil {
		if auth.Attributes != nil {
			if token := strings.TrimSpace(auth.Attributes["api_key"]); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
				return nil
			}
		}
		if auth.Metadata != nil {
			if token, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(token) != "" {
				req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
			}
		}
	}
	return nil
}

func (e *codexQuotaRefreshTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexQuotaRefreshTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *codexQuotaRefreshTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *codexQuotaRefreshTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexQuotaRefreshTestExecutor) HttpRequest(_ context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if e.err != nil {
		return nil, e.err
	}
	if errPrepare := e.PrepareRequest(req, auth); errPrepare != nil {
		return nil, errPrepare
	}
	recorded := req.Clone(req.Context())
	recorded.Header = req.Header.Clone()
	e.mu.Lock()
	e.requests = append(e.requests, recorded)
	e.mu.Unlock()

	statusCode := e.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(e.body)),
		Request:    req,
	}, nil
}

func (e *codexQuotaRefreshTestExecutor) requestCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.requests)
}

func (e *codexQuotaRefreshTestExecutor) lastRequest() *http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.requests) == 0 {
		return nil
	}
	return e.requests[len(e.requests)-1]
}

func TestManagerRefreshCodexQuota_SuccessWritesQuotaAndMetadata(t *testing.T) {
	t.Parallel()

	executor := &codexQuotaRefreshTestExecutor{
		statusCode: http.StatusOK,
		body:       `{"user_id":"user-123","email":"codex@example.com","rate_limit":{"primary_window":{"used_percent":20},"secondary_window":{"used_percent":35}}}`,
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":     "token-123",
			"codex_account_id": "acct-123",
		},
		Quota: QuotaState{
			Exceeded:     true,
			Reason:       "quota",
			BackoffLevel: 2,
		},
		ModelStates: map[string]*ModelState{
			"model": {Quota: quotaSnapshotForTest(11, 22)},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	result, err := manager.RefreshCodexQuota(context.Background(), "codex-auth")
	if err != nil {
		t.Fatalf("RefreshCodexQuota() error = %v", err)
	}
	if !result.Success || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %#v, want success status 200", result)
	}

	req := executor.lastRequest()
	if req == nil {
		t.Fatal("expected wham usage request")
	}
	if req.Method != http.MethodGet || req.URL.String() != codexWhamUsageURL {
		t.Fatalf("request = %s %s, want GET %s", req.Method, req.URL.String(), codexWhamUsageURL)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want bearer token", got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-123" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-123", got)
	}

	updated, _ := manager.GetByID("codex-auth")
	if !updated.Quota.FiveHourRemainingKnown || updated.Quota.FiveHourRemainingPercent != 80 {
		t.Fatalf("5h quota = (%v, %v), want known 80", updated.Quota.FiveHourRemainingKnown, updated.Quota.FiveHourRemainingPercent)
	}
	if !updated.Quota.SevenDayRemainingKnown || updated.Quota.SevenDayRemainingPercent != 65 {
		t.Fatalf("7d quota = (%v, %v), want known 65", updated.Quota.SevenDayRemainingKnown, updated.Quota.SevenDayRemainingPercent)
	}
	if !updated.Quota.Exceeded || updated.Quota.Reason != "quota" || updated.Quota.BackoffLevel != 2 {
		t.Fatalf("cooldown fields changed: %#v", updated.Quota)
	}
	if updated.ModelStates["model"].Quota.FiveHourRemainingPercent != 11 {
		t.Fatalf("model quota changed: %#v", updated.ModelStates["model"].Quota)
	}
	if got := updated.Metadata["codex_quota_last_status"]; got != "success" {
		t.Fatalf("codex_quota_last_status = %#v, want success", got)
	}
	if got := updated.Metadata["codex_user_id"]; got != "user-123" {
		t.Fatalf("codex_user_id = %#v, want user-123", got)
	}
	if got := updated.Metadata["email"]; got != "codex@example.com" {
		t.Fatalf("email = %#v, want codex@example.com", got)
	}
}

func TestManagerRefreshCodexQuota_SuccessWithoutAccountHeaderSavesResponseIdentity(t *testing.T) {
	t.Parallel()

	executor := &codexQuotaRefreshTestExecutor{
		statusCode: http.StatusOK,
		body:       `{"account":{"account_id":"acct-body"},"rate_limit":{"primary_window":{"used_percent":10}}}`,
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-123"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, err := manager.RefreshCodexQuota(context.Background(), "codex-auth"); err != nil {
		t.Fatalf("RefreshCodexQuota() error = %v", err)
	}

	req := executor.lastRequest()
	if req == nil {
		t.Fatal("expected wham usage request")
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "" {
		t.Fatalf("Chatgpt-Account-Id = %q, want empty", got)
	}
	updated, _ := manager.GetByID("codex-auth")
	if got := updated.Metadata["codex_account_id"]; got != "acct-body" {
		t.Fatalf("codex_account_id = %#v, want acct-body", got)
	}
}

func TestManagerRefreshCodexQuota_APIKeyAuthDoesNotCallWhamUsage(t *testing.T) {
	t.Parallel()

	executor := &codexQuotaRefreshTestExecutor{
		statusCode: http.StatusOK,
		body:       `{"rate_limit":{"primary_window":{"used_percent":10}}}`,
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "codex-api-key-auth",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	result, err := manager.RefreshCodexQuota(context.Background(), "codex-api-key-auth")
	if err == nil {
		t.Fatal("expected RefreshCodexQuota() error")
	}
	if result.Success {
		t.Fatalf("result.Success = true, want false: %#v", result)
	}
	if executor.requestCount() != 0 {
		t.Fatalf("api-key auth sent %d wham/usage requests, want 0", executor.requestCount())
	}
}

func TestManagerRefreshCodexQuota_FailuresDoNotClearQuotaOrDisableAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		err        error
		withToken  bool
		wantStatus int
	}{
		{name: "missing token", withToken: false},
		{name: "401", statusCode: http.StatusUnauthorized, body: `{"error":"unauthorized"}`, withToken: true, wantStatus: http.StatusUnauthorized},
		{name: "403", statusCode: http.StatusForbidden, body: `{"error":"forbidden"}`, withToken: true, wantStatus: http.StatusForbidden},
		{name: "429", statusCode: http.StatusTooManyRequests, body: `{"error":"rate limit"}`, withToken: true, wantStatus: http.StatusTooManyRequests},
		{name: "network", err: errors.New("dial failed"), withToken: true},
		{name: "invalid body", statusCode: http.StatusOK, body: `{"ok":true}`, withToken: true, wantStatus: http.StatusOK},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := &codexQuotaRefreshTestExecutor{
				statusCode: tc.statusCode,
				body:       tc.body,
				err:        tc.err,
			}
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			metadata := map[string]any{"type": "codex"}
			if tc.withToken {
				metadata["access_token"] = "token-123"
			}
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "codex-auth",
				Provider: "codex",
				Metadata: metadata,
				Quota:    quotaSnapshotForTest(55, 66),
			}); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			result, err := manager.RefreshCodexQuota(context.Background(), "codex-auth")
			if err == nil {
				t.Fatal("expected RefreshCodexQuota() error")
			}
			if result.Success {
				t.Fatalf("result.Success = true, want false: %#v", result)
			}
			if result.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", result.StatusCode, tc.wantStatus)
			}
			if result.Error == "" {
				t.Fatalf("expected error summary in result: %#v", result)
			}
			if !tc.withToken && executor.requestCount() != 0 {
				t.Fatalf("missing token sent %d requests, want 0", executor.requestCount())
			}

			updated, _ := manager.GetByID("codex-auth")
			if !updated.Quota.FiveHourRemainingKnown || updated.Quota.FiveHourRemainingPercent != 55 ||
				!updated.Quota.SevenDayRemainingKnown || updated.Quota.SevenDayRemainingPercent != 66 {
				t.Fatalf("quota changed after failure: %#v", updated.Quota)
			}
			if updated.Disabled || updated.Unavailable {
				t.Fatalf("auth availability changed after failure: disabled=%v unavailable=%v", updated.Disabled, updated.Unavailable)
			}
			if got := updated.Metadata["codex_quota_last_status"]; got != "failure" {
				t.Fatalf("codex_quota_last_status = %#v, want failure", got)
			}
			if got := updated.Metadata["codex_quota_last_error"]; got == "" {
				t.Fatalf("codex_quota_last_error = %#v, want non-empty", got)
			}
			if got := updated.Metadata["codex_quota_next_retry_after"]; got == "" {
				t.Fatalf("codex_quota_next_retry_after = %#v, want non-empty", got)
			}
		})
	}
}
