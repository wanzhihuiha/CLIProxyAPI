package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestActiveSessionTracker_EndDropsInFlightButKeepsActiveUntilIdle(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	tracker := NewActiveSessionTracker(ActiveSessionConfig{
		IdleTimeout: 5 * time.Minute,
		HardTTL:     time.Hour,
	})

	tracker.Begin("session-a", "auth-a", base)
	snapshot := tracker.Snapshot("auth-a", base)
	if snapshot.ActiveSessions != 1 || snapshot.InFlight != 1 {
		t.Fatalf("Snapshot() after begin = %+v, want active=1 in_flight=1", snapshot)
	}

	tracker.end("session-a", "auth-a", base.Add(time.Minute))
	snapshot = tracker.Snapshot("auth-a", base.Add(2*time.Minute))
	if snapshot.ActiveSessions != 1 || snapshot.InFlight != 0 {
		t.Fatalf("Snapshot() after end = %+v, want active=1 in_flight=0", snapshot)
	}

	snapshot = tracker.Snapshot("auth-a", base.Add(7*time.Minute))
	if snapshot.ActiveSessions != 0 || snapshot.InFlight != 0 {
		t.Fatalf("Snapshot() after idle timeout = %+v, want active=0 in_flight=0", snapshot)
	}
}

func TestActiveSessionTracker_HardTTLWaitsForInFlight(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	tracker := NewActiveSessionTracker(ActiveSessionConfig{
		IdleTimeout: time.Hour,
		HardTTL:     time.Minute,
	})

	tracker.Begin("session-a", "auth-a", base)
	snapshot := tracker.Snapshot("auth-a", base.Add(2*time.Minute))
	if snapshot.ActiveSessions != 1 || snapshot.InFlight != 1 {
		t.Fatalf("Snapshot() past hard ttl with in-flight = %+v, want active=1 in_flight=1", snapshot)
	}

	tracker.end("session-a", "auth-a", base.Add(2*time.Minute))
	snapshot = tracker.Snapshot("auth-a", base.Add(2*time.Minute))
	if snapshot.ActiveSessions != 0 || snapshot.InFlight != 0 {
		t.Fatalf("Snapshot() after pending release = %+v, want active=0 in_flight=0", snapshot)
	}
}

func TestStickyQuotaProtectSelectorPick_ActiveSessionCountSpreadsNewSessions(t *testing.T) {
	t.Parallel()

	activeSessions := NewActiveSessionTracker(ActiveSessionConfig{
		IdleTimeout: time.Hour,
		HardTTL:     time.Hour,
	})
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:       NewStickyQuotaProtectSelector(activeSessions),
		TTL:            time.Hour,
		ActiveSessions: activeSessions,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a", Quota: quotaSnapshotForTest(90, 80)},
		{ID: "auth-b", Quota: quotaSnapshotForTest(90, 80)},
	}

	firstOpts := cliproxyexecutor.Options{
		Headers:  sessionHeadersForTest("active-session-a"),
		Metadata: map[string]any{},
	}
	first, err := selector.Pick(context.Background(), "gemini", "model", firstOpts, auths)
	if err != nil {
		t.Fatalf("first Pick() error = %v", err)
	}
	if first == nil || first.ID != "auth-a" {
		t.Fatalf("first Pick() auth = %v, want auth-a", first)
	}
	defer releaseActiveSessionLease(takeActiveSessionLeaseFromMetadata(firstOpts.Metadata))

	secondOpts := cliproxyexecutor.Options{
		Headers:  sessionHeadersForTest("active-session-b"),
		Metadata: map[string]any{},
	}
	second, err := selector.Pick(context.Background(), "gemini", "model", secondOpts, auths)
	if err != nil {
		t.Fatalf("second Pick() error = %v", err)
	}
	if second == nil || second.ID != "auth-b" {
		t.Fatalf("second Pick() auth = %v, want auth-b", second)
	}
	defer releaseActiveSessionLease(takeActiveSessionLeaseFromMetadata(secondOpts.Metadata))
}

func TestManagerExecute_ActiveSessionLeaseReleasedAfterNonStream(t *testing.T) {
	t.Parallel()

	activeSessions := NewActiveSessionTracker(ActiveSessionConfig{
		IdleTimeout: time.Hour,
		HardTTL:     time.Hour,
	})
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:       NewStickyQuotaProtectSelector(activeSessions),
		TTL:            time.Hour,
		ActiveSessions: activeSessions,
	})
	defer selector.Stop()

	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(schedulerTestExecutor{})
	if _, err := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "test"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	_, err := manager.Execute(context.Background(), []string{"test"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		Headers:  sessionHeadersForTest("manager-active-session"),
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	snapshot := activeSessions.Snapshot("auth-a", time.Now())
	if snapshot.ActiveSessions != 1 || snapshot.InFlight != 0 {
		t.Fatalf("Snapshot() after Execute = %+v, want active=1 in_flight=0", snapshot)
	}
}
