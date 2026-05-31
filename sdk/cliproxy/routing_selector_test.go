package cliproxy

import (
	"context"
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestNormalizeRoutingStrategyName_StickyQuotaProtectAliases(t *testing.T) {
	t.Parallel()

	tests := []string{
		"sticky-quota-protect",
		"sticky_quota_protect",
		"stickyquotaprotect",
		"sqp",
	}
	for _, input := range tests {
		if got := normalizeRoutingStrategyName(input); got != routingStrategyStickyQuotaProtect {
			t.Fatalf("normalizeRoutingStrategyName(%q) = %q, want %q", input, got, routingStrategyStickyQuotaProtect)
		}
	}
}

func TestNewRoutingSelector_StickyQuotaProtectForcesSessionAffinity(t *testing.T) {
	t.Parallel()

	selector := newRoutingSelector("sticky_quota_protect", false, "1m")
	authA := &coreauth.Auth{ID: "auth-a", Quota: routingQuotaSnapshotForTest(90, 80)}
	authB := &coreauth.Auth{ID: "auth-b", Quota: routingQuotaSnapshotForTest(80, 80)}
	headers := http.Header{}
	headers.Set("X-Session-ID", "forced-sticky")
	opts := cliproxyexecutor.Options{
		Headers: headers,
	}

	first, err := selector.Pick(context.Background(), "gemini", "model", opts, []*coreauth.Auth{authA, authB})
	if err != nil {
		t.Fatalf("first Pick() error = %v", err)
	}
	if first == nil || first.ID != "auth-a" {
		t.Fatalf("first Pick() auth = %v, want auth-a", first)
	}

	authA.Quota = routingQuotaSnapshotForTest(1, 80)
	second, err := selector.Pick(context.Background(), "gemini", "model", opts, []*coreauth.Auth{authA, authB})
	if err != nil {
		t.Fatalf("second Pick() error = %v", err)
	}
	if second == nil || second.ID != "auth-a" {
		t.Fatalf("second Pick() auth = %v, want sticky auth-a", second)
	}
}

func routingQuotaSnapshotForTest(fiveHour, sevenDay float64) coreauth.QuotaState {
	return coreauth.QuotaState{
		FiveHourRemainingKnown:   true,
		FiveHourRemainingPercent: fiveHour,
		SevenDayRemainingKnown:   true,
		SevenDayRemainingPercent: sevenDay,
	}
}
