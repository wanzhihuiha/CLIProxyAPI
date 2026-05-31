package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type stickyFailureStatusError struct {
	status  int
	message string
}

func (e stickyFailureStatusError) Error() string {
	return e.message
}

func (e stickyFailureStatusError) StatusCode() int {
	return e.status
}

type stickyFailureExecutor struct {
	mu        sync.Mutex
	failures  map[string]int
	status    int
	threshold int
	attempts  []string
}

func (e *stickyFailureExecutor) Identifier() string { return "test" }

func (e *stickyFailureExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.attempts = append(e.attempts, auth.ID)
	if auth.ID == "auth-a" {
		if e.failures == nil {
			e.failures = make(map[string]int)
		}
		limit := e.threshold
		if limit <= 0 {
			limit = 1
		}
		if e.failures[auth.ID] < limit {
			e.failures[auth.ID]++
			status := e.status
			if status == 0 {
				status = http.StatusInternalServerError
			}
			return cliproxyexecutor.Response{}, stickyFailureStatusError{status: status, message: http.StatusText(status)}
		}
	}
	return cliproxyexecutor.Response{}, nil
}

func (e *stickyFailureExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *stickyFailureExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *stickyFailureExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *stickyFailureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestSessionCache_StickyFailureCountClearsOnSuccess(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(timeHourForStickyFailureTest())
	defer cache.Stop()

	cache.Set("session-a", "auth-a")
	if failures, unbound := cache.RecordFailure("session-a", "auth-a", 2); failures != 1 || unbound {
		t.Fatalf("RecordFailure() = %d, %v; want 1, false", failures, unbound)
	}
	if ok := cache.RecordSuccess("session-a", "auth-a"); !ok {
		t.Fatalf("RecordSuccess() = false, want true")
	}
	if got := cache.FailureCount("session-a", "auth-a"); got != 0 {
		t.Fatalf("FailureCount() = %d, want 0", got)
	}
}

func TestManagerExecute_StickyRetryableFailureKeepsBindingUntilThreshold(t *testing.T) {
	t.Parallel()

	selector := newStickyFailureTestSelector(t)
	executor := &stickyFailureExecutor{status: http.StatusInternalServerError, threshold: 2}
	manager := newStickyFailureTestManager(t, selector, executor)

	opts := cliproxyexecutor.Options{
		Headers: sessionHeadersForTest("sticky-retryable-failure"),
	}
	if _, err := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{}, opts); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	cacheKey := "mixed::header:sticky-retryable-failure::"
	if got, ok := selector.cache.Get(cacheKey); !ok || got != "auth-a" {
		t.Fatalf("cache after first failure = %q, %v; want auth-a, true", got, ok)
	}

	if _, err := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{}, opts); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if got, ok := selector.cache.Get(cacheKey); !ok || got != "auth-b" {
		t.Fatalf("cache after threshold = %q, %v; want auth-b, true", got, ok)
	}
}

func TestManagerExecute_StickyUnauthorizedFailureUnbindsImmediately(t *testing.T) {
	t.Parallel()

	selector := newStickyFailureTestSelector(t)
	executor := &stickyFailureExecutor{status: http.StatusUnauthorized, threshold: 1}
	manager := newStickyFailureTestManager(t, selector, executor)

	opts := cliproxyexecutor.Options{
		Headers: sessionHeadersForTest("sticky-unauthorized-failure"),
	}
	if _, err := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{}, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	cacheKey := "mixed::header:sticky-unauthorized-failure::"
	if got, ok := selector.cache.Get(cacheKey); !ok || got != "auth-b" {
		t.Fatalf("cache after unauthorized failure = %q, %v; want auth-b, true", got, ok)
	}
}

func newStickyFailureTestSelector(t *testing.T) *SessionAffinitySelector {
	t.Helper()
	activeSessions := NewActiveSessionTracker(ActiveSessionConfig{})
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:       NewStickyQuotaProtectSelector(activeSessions),
		TTL:            timeHourForStickyFailureTest(),
		ActiveSessions: activeSessions,
	})
	t.Cleanup(selector.Stop)
	return selector
}

func newStickyFailureTestManager(t *testing.T, selector *SessionAffinitySelector, executor *stickyFailureExecutor) *Manager {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)
	auths := []*Auth{
		{
			ID:       "auth-a",
			Provider: "test",
			Metadata: map[string]any{
				"disable_cooling": true,
			},
			Quota: quotaSnapshotForTest(90, 80),
		},
		{
			ID:       "auth-b",
			Provider: "test",
			Quota:    quotaSnapshotForTest(80, 80),
		},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}
	return manager
}

func timeHourForStickyFailureTest() time.Duration {
	return time.Hour
}
