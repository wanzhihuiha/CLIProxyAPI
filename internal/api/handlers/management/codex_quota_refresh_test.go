package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type managementCodexQuotaRefreshExecutor struct {
	body       string
	statusCode int

	mu    sync.Mutex
	calls int
}

func (e *managementCodexQuotaRefreshExecutor) Identifier() string { return "codex" }

func (e *managementCodexQuotaRefreshExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *managementCodexQuotaRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *managementCodexQuotaRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *managementCodexQuotaRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *managementCodexQuotaRefreshExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	statusCode := e.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(e.body)),
		Request:    req,
	}, nil
}

func (e *managementCodexQuotaRefreshExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type codexQuotaRefreshPayload struct {
	Results []coreauth.CodexQuotaRefreshResult `json:"results"`
	Skipped int                                `json:"skipped"`
}

func TestRefreshCodexQuota_ByAuthIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &managementCodexQuotaRefreshExecutor{
		body: `{"rate_limit":{"primary_window":{"used_percent":9},"secondary_window":{"used_percent":17}}}`,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-123"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	authIndex := auth.EnsureIndex()
	h := &Handler{authManager: manager}

	payload := postCodexQuotaRefresh(t, h, `{"auth_index":"`+authIndex+`"}`)
	if len(payload.Results) != 1 {
		t.Fatalf("result count = %d, want 1", len(payload.Results))
	}
	result := payload.Results[0]
	if !result.Success {
		t.Fatalf("refresh result = %#v, want success", result)
	}
	if result.AuthID != "codex-auth" || result.AuthIndex == "" {
		t.Fatalf("auth identifiers = (%q, %q), want codex-auth and auth_index", result.AuthID, result.AuthIndex)
	}
	if result.Quota.FiveHourRemainingPercent != 91 || result.Quota.SevenDayRemainingPercent != 83 {
		t.Fatalf("quota = (%v, %v), want (91, 83)", result.Quota.FiveHourRemainingPercent, result.Quota.SevenDayRemainingPercent)
	}
	if executor.callCount() != 1 {
		t.Fatalf("refresh calls = %d, want 1", executor.callCount())
	}
}

func TestRefreshCodexQuota_AllSkipsNonCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &managementCodexQuotaRefreshExecutor{
		body: `{"rate_limit":{"primary_window":{"used_percent":50}}}`,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-123"},
	}); errRegister != nil {
		t.Fatalf("register codex auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "gemini-auth",
		Provider: "gemini",
	}); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	h := &Handler{authManager: manager}

	payload := postCodexQuotaRefresh(t, h, `{}`)
	if len(payload.Results) != 1 {
		t.Fatalf("result count = %d, want 1", len(payload.Results))
	}
	if payload.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", payload.Skipped)
	}
	if executor.callCount() != 1 {
		t.Fatalf("refresh calls = %d, want 1", executor.callCount())
	}
}

func TestRefreshCodexQuota_MissingAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{authManager: coreauth.NewManager(nil, nil, nil)}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex/quota-refresh", strings.NewReader(`{"auth_index":"missing"}`))
	ginCtx.Request.Header.Set("Content-Type", "application/json")

	h.RefreshCodexQuota(ginCtx)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRefreshCodexQuota_ReturnsReadableFailureResult(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "codex-auth", Provider: "codex", Metadata: map[string]any{"type": "codex"}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	h := &Handler{authManager: manager}

	payload := postCodexQuotaRefresh(t, h, `{"auth_id":"codex-auth"}`)
	if len(payload.Results) != 1 {
		t.Fatalf("result count = %d, want 1", len(payload.Results))
	}
	result := payload.Results[0]
	if result.Success {
		t.Fatalf("result.Success = true, want false: %#v", result)
	}
	if result.Error == "" {
		t.Fatalf("result error is empty: %#v", result)
	}
}

func postCodexQuotaRefresh(t *testing.T, h *Handler, body string) codexQuotaRefreshPayload {
	t.Helper()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex/quota-refresh", strings.NewReader(body))
	ginCtx.Request.Header.Set("Content-Type", "application/json")

	h.RefreshCodexQuota(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var payload codexQuotaRefreshPayload
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v; body=%s", errDecode, rec.Body.String())
	}
	return payload
}
