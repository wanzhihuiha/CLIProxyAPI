package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerStartCodexQuotaRefresh_StartupRefreshesMissingSnapshot(t *testing.T) {
	executor := &codexQuotaRefreshTestExecutor{
		statusCode: http.StatusOK,
		body:       `{"rate_limit":{"primary_window":{"used_percent":25},"secondary_window":{"used_percent":40}}}`,
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

	manager.StartCodexQuotaRefresh(context.Background(), CodexQuotaRefreshOptions{
		Enabled:        true,
		Interval:       time.Hour,
		MaxConcurrency: 2,
	})
	defer manager.StopCodexQuotaRefresh()

	waitForCodexQuotaRefreshCondition(t, 2*time.Second, func() bool {
		return executor.requestCount() >= 1
	})
	updated, _ := manager.GetByID("codex-auth")
	if !updated.Quota.FiveHourRemainingKnown || updated.Quota.FiveHourRemainingPercent != 75 {
		t.Fatalf("5h quota = (%v, %v), want known 75", updated.Quota.FiveHourRemainingKnown, updated.Quota.FiveHourRemainingPercent)
	}
	if !updated.Quota.SevenDayRemainingKnown || updated.Quota.SevenDayRemainingPercent != 60 {
		t.Fatalf("7d quota = (%v, %v), want known 60", updated.Quota.SevenDayRemainingKnown, updated.Quota.SevenDayRemainingPercent)
	}
}

func TestManagerStartCodexQuotaRefresh_DisabledDoesNotStart(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.StartCodexQuotaRefresh(context.Background(), CodexQuotaRefreshOptions{
		Enabled:        false,
		Interval:       time.Millisecond,
		MaxConcurrency: 1,
	})
	if manager.codexQuotaRefreshLoop != nil {
		t.Fatal("codexQuotaRefreshLoop is set, want nil when disabled")
	}
}

func TestManagerStartCodexQuotaRefresh_MaxConcurrency(t *testing.T) {
	executor := &blockingCodexQuotaRefreshExecutor{
		body:    `{"rate_limit":{"primary_window":{"used_percent":10}}}`,
		release: make(chan struct{}),
		started: make(chan struct{}, 3),
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	for _, id := range []string{"codex-a", "codex-b", "codex-c"} {
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:       id,
			Provider: "codex",
			Metadata: map[string]any{"access_token": "token-" + id},
		}); errRegister != nil {
			t.Fatalf("register auth %s: %v", id, errRegister)
		}
	}

	manager.StartCodexQuotaRefresh(context.Background(), CodexQuotaRefreshOptions{
		Enabled:        true,
		Interval:       time.Hour,
		MaxConcurrency: 2,
	})
	defer manager.StopCodexQuotaRefresh()

	waitForStartedCodexQuotaRequests(t, executor.started, 2)
	select {
	case <-executor.started:
		t.Fatal("third request started before release; max concurrency was not enforced")
	case <-time.After(50 * time.Millisecond):
	}
	close(executor.release)
	waitForCodexQuotaRefreshCondition(t, 2*time.Second, func() bool {
		return executor.requestCount() == 3
	})
	if got := executor.maxActive(); got > 2 {
		t.Fatalf("max active requests = %d, want <= 2", got)
	}
}

func TestNextCodexQuotaRefreshAt_ExpiredSnapshotDueImmediately(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-123"},
		Quota: QuotaState{
			FiveHourRemainingKnown:   true,
			FiveHourRemainingPercent: 80,
			SnapshotUpdatedAt:        now.Add(-quotaSnapshotStaleGraceTTL - time.Minute),
		},
	}
	got, ok := nextCodexQuotaRefreshAt(now, auth, time.Hour)
	if !ok {
		t.Fatal("nextCodexQuotaRefreshAt() ok = false, want true")
	}
	if !got.Equal(now) {
		t.Fatalf("nextCodexQuotaRefreshAt() = %s, want now", got)
	}
}

func TestNextCodexQuotaRefreshAt_PeriodicInterval(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	updatedAt := now.Add(-5 * time.Minute)
	auth := &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-123"},
		Quota: QuotaState{
			FiveHourRemainingKnown:   true,
			FiveHourRemainingPercent: 80,
			SnapshotUpdatedAt:        updatedAt,
		},
	}
	got, ok := nextCodexQuotaRefreshAt(now, auth, 10*time.Minute)
	if !ok {
		t.Fatal("nextCodexQuotaRefreshAt() ok = false, want true")
	}
	want := updatedAt.Add(10 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("nextCodexQuotaRefreshAt() = %s, want %s", got, want)
	}
}

func TestNextCodexQuotaRefreshAt_RespectsFailureBackoff(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	nextRetry := now.Add(5 * time.Minute)
	auth := &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":                 "token-123",
			codexQuotaMetadataNextRetryKey: nextRetry.Format(time.RFC3339Nano),
		},
		Quota: QuotaState{
			FiveHourRemainingKnown:   true,
			FiveHourRemainingPercent: 80,
			SnapshotUpdatedAt:        now.Add(-time.Hour),
		},
	}
	got, ok := nextCodexQuotaRefreshAt(now, auth, time.Second)
	if !ok {
		t.Fatal("nextCodexQuotaRefreshAt() ok = false, want true")
	}
	if !got.Equal(nextRetry) {
		t.Fatalf("nextCodexQuotaRefreshAt() = %s, want next retry %s", got, nextRetry)
	}
}

type blockingCodexQuotaRefreshExecutor struct {
	body    string
	release chan struct{}
	started chan struct{}

	mu       sync.Mutex
	requests int
	active   int
	max      int
}

func (e *blockingCodexQuotaRefreshExecutor) Identifier() string { return "codex" }

func (e *blockingCodexQuotaRefreshExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	if req == nil || auth == nil || auth.Metadata == nil {
		return nil
	}
	if token, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	return nil
}

func (e *blockingCodexQuotaRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingCodexQuotaRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *blockingCodexQuotaRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *blockingCodexQuotaRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingCodexQuotaRefreshExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if errPrepare := e.PrepareRequest(req, auth); errPrepare != nil {
		return nil, errPrepare
	}
	e.mu.Lock()
	e.requests++
	e.active++
	if e.active > e.max {
		e.max = e.active
	}
	e.mu.Unlock()

	select {
	case e.started <- struct{}{}:
	default:
	}

	select {
	case <-ctx.Done():
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
		return nil, ctx.Err()
	case <-e.release:
	}

	e.mu.Lock()
	e.active--
	e.mu.Unlock()

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(e.body)),
		Request:    req,
	}, nil
}

func (e *blockingCodexQuotaRefreshExecutor) requestCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requests
}

func (e *blockingCodexQuotaRefreshExecutor) maxActive() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.max
}

func waitForStartedCodexQuotaRequests(t *testing.T, started <-chan struct{}, count int) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for i := 0; i < count; i++ {
		select {
		case <-started:
		case <-timeout:
			t.Fatalf("timed out waiting for %d started requests", count)
		}
	}
}

func waitForCodexQuotaRefreshCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for codex quota refresh condition")
}
