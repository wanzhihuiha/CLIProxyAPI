package auth

import (
	"context"
	"testing"
	"time"
)

type runtimeMetadataStore struct {
	saved *Auth
	items []*Auth
}

func (s *runtimeMetadataStore) List(context.Context) ([]*Auth, error) {
	out := make([]*Auth, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item.Clone())
	}
	return out, nil
}

func (s *runtimeMetadataStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saved = auth.Clone()
	return "", nil
}

func (s *runtimeMetadataStore) Delete(context.Context, string) error { return nil }

func TestManagerUpdateAccountQuotaSnapshot_PersistsQuotaMetadata(t *testing.T) {
	ctx := context.Background()
	store := &runtimeMetadataStore{}
	manager := NewManager(store, nil, nil)
	if _, err := manager.Register(WithSkipPersist(ctx), &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	observedAt := time.Date(2026, 5, 31, 8, 30, 0, 123, time.UTC)
	_, err := manager.UpdateAccountQuotaSnapshotWithMetadata(ctx, "codex-auth", QuotaSnapshotUpdate{
		FiveHourRemainingKnown:   true,
		FiveHourRemainingPercent: 77,
		SevenDayRemainingKnown:   true,
		SevenDayRemainingPercent: 92,
		ObservedAt:               observedAt,
		Source:                   codexWhamUsageQuotaSource,
	}, map[string]string{"codex_quota_last_status": "success"})
	if err != nil {
		t.Fatalf("UpdateAccountQuotaSnapshotWithMetadata() error = %v", err)
	}
	if store.saved == nil {
		t.Fatal("expected auth to be persisted")
	}
	quota, ok := store.saved.Metadata["quota"].(map[string]any)
	if !ok {
		t.Fatalf("persisted quota = %#v, want object", store.saved.Metadata["quota"])
	}
	if got := quota["five_hour_remaining_known"]; got != true {
		t.Fatalf("five_hour_remaining_known = %#v, want true", got)
	}
	if got := quota["five_hour_remaining_percent"]; got != float64(77) {
		t.Fatalf("five_hour_remaining_percent = %#v, want 77", got)
	}
	if got := quota["seven_day_remaining_known"]; got != true {
		t.Fatalf("seven_day_remaining_known = %#v, want true", got)
	}
	if got := quota["seven_day_remaining_percent"]; got != float64(92) {
		t.Fatalf("seven_day_remaining_percent = %#v, want 92", got)
	}
	if got := quota["snapshot_updated_at"]; got == "" || got == nil {
		t.Fatalf("snapshot_updated_at = %#v, want timestamp", got)
	}
}

func TestManagerLoad_AppliesQuotaMetadata(t *testing.T) {
	observedAt := time.Date(2026, 5, 31, 8, 30, 0, 0, time.UTC)
	store := &runtimeMetadataStore{items: []*Auth{{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"type": "codex",
			"quota": map[string]any{
				"five_hour_remaining_known":   true,
				"five_hour_remaining_percent": float64(77),
				"seven_day_remaining_known":   true,
				"seven_day_remaining_percent": float64(92),
				"snapshot_updated_at":         observedAt.Format(time.RFC3339Nano),
			},
		},
	}}}
	manager := NewManager(store, nil, nil)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	auth, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("expected loaded auth")
	}
	if !auth.Quota.FiveHourRemainingKnown || auth.Quota.FiveHourRemainingPercent != 77 {
		t.Fatalf("5h quota = (%v, %v), want known 77", auth.Quota.FiveHourRemainingKnown, auth.Quota.FiveHourRemainingPercent)
	}
	if !auth.Quota.SevenDayRemainingKnown || auth.Quota.SevenDayRemainingPercent != 92 {
		t.Fatalf("7d quota = (%v, %v), want known 92", auth.Quota.SevenDayRemainingKnown, auth.Quota.SevenDayRemainingPercent)
	}
	if !auth.Quota.SnapshotUpdatedAt.Equal(observedAt) {
		t.Fatalf("snapshot_updated_at = %s, want %s", auth.Quota.SnapshotUpdatedAt, observedAt)
	}
}
