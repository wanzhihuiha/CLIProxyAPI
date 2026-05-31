package management

import (
	"context"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFiles_IncludesQuotaAndCodexIdentityMetadata(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	observedAt := time.Date(2026, 5, 31, 3, 30, 0, 0, time.UTC)
	recoverAt := observedAt.Add(15 * time.Minute)
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"type":             "codex",
			"codex_account_id": "acct-123",
			"codex_user_id":    "user-456",
			"email":            "codex@example.com",
		},
		Quota: coreauth.QuotaState{
			Exceeded:                 true,
			Reason:                   "quota",
			NextRecoverAt:            recoverAt,
			BackoffLevel:             3,
			FiveHourRemainingKnown:   true,
			FiveHourRemainingPercent: 71,
			SevenDayRemainingKnown:   true,
			SevenDayRemainingPercent: 82,
			SnapshotUpdatedAt:        observedAt,
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	quota, ok := entry["quota"].(map[string]any)
	if !ok {
		t.Fatalf("expected quota object, got %#v", entry["quota"])
	}
	if got := quota["five_hour_remaining_known"]; got != true {
		t.Fatalf("five_hour_remaining_known = %#v, want true", got)
	}
	if got := quota["five_hour_remaining_percent"]; got != float64(71) {
		t.Fatalf("five_hour_remaining_percent = %#v, want 71", got)
	}
	if got := quota["seven_day_remaining_known"]; got != true {
		t.Fatalf("seven_day_remaining_known = %#v, want true", got)
	}
	if got := quota["seven_day_remaining_percent"]; got != float64(82) {
		t.Fatalf("seven_day_remaining_percent = %#v, want 82", got)
	}
	if got := quota["exceeded"]; got != true {
		t.Fatalf("exceeded = %#v, want true", got)
	}
	if got := quota["next_recover_at"]; got == nil || got == "" {
		t.Fatalf("next_recover_at = %#v, want timestamp", got)
	}
	if got := quota["snapshot_updated_at"]; got == nil || got == "" {
		t.Fatalf("snapshot_updated_at = %#v, want timestamp", got)
	}
	if got := entry["codex_account_id"]; got != "acct-123" {
		t.Fatalf("codex_account_id = %#v, want acct-123", got)
	}
	if got := entry["codex_user_id"]; got != "user-456" {
		t.Fatalf("codex_user_id = %#v, want user-456", got)
	}
	if got := entry["email"]; got != "codex@example.com" {
		t.Fatalf("email = %#v, want codex@example.com", got)
	}
}
