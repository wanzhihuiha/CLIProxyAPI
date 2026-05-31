package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type quotaHeaderTestExecutor struct {
	provider string
	headers  http.Header
}

func (e quotaHeaderTestExecutor) Identifier() string { return e.provider }

func (e quotaHeaderTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`), Headers: e.headers.Clone()}, nil
}

func (e quotaHeaderTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"ok":true}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Headers: e.headers.Clone(), Chunks: chunks}, nil
}

func (e quotaHeaderTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e quotaHeaderTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens":1}`), Headers: e.headers.Clone()}, nil
}

func (e quotaHeaderTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestParseQuotaSnapshotFromHeaders_ExplicitPercentAndClamp(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"x-quota-5h-remaining-percent": {"150"},
		"X-Quota-7D-Remaining-Percent": {"-10"},
	}

	update, ok := parseQuotaSnapshotFromHeaders("test", "model", headers)
	if !ok {
		t.Fatal("expected quota snapshot update")
	}
	if !update.FiveHourRemainingKnown || update.FiveHourRemainingPercent != 100 {
		t.Fatalf("5h update = (%v, %v), want known 100", update.FiveHourRemainingKnown, update.FiveHourRemainingPercent)
	}
	if !update.SevenDayRemainingKnown || update.SevenDayRemainingPercent != 0 {
		t.Fatalf("7d update = (%v, %v), want known 0", update.SevenDayRemainingKnown, update.SevenDayRemainingPercent)
	}
}

func TestParseQuotaSnapshotFromHeaders_RemainingLimitRatio(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Quota-5h-Remaining": {"25"},
		"X-Quota-5h-Limit":     {"100"},
		"X-Quota-7d-Remaining": {"3"},
		"X-Quota-7d-Limit":     {"10"},
	}

	update, ok := parseQuotaSnapshotFromHeaders("test", "model", headers)
	if !ok {
		t.Fatal("expected quota snapshot update")
	}
	if update.FiveHourRemainingPercent != 25 {
		t.Fatalf("5h percent = %v, want 25", update.FiveHourRemainingPercent)
	}
	if update.SevenDayRemainingPercent != 30 {
		t.Fatalf("7d percent = %v, want 30", update.SevenDayRemainingPercent)
	}
}

func TestParseQuotaSnapshotFromHeaders_IgnoresOpaqueRateLimitHeaders(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-RateLimit-Remaining-Requests": {"10"},
		"X-RateLimit-Limit-Requests":     {"100"},
	}

	if update, ok := parseQuotaSnapshotFromHeaders("test", "model", headers); ok {
		t.Fatalf("parseQuotaSnapshotFromHeaders() = %#v, want no update", update)
	}
}

func TestParseCodexWhamUsageQuotaSnapshot_PrimaryAndSecondary(t *testing.T) {
	t.Parallel()

	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":12.5},"secondary_window":{"used_percent":44}}}`)

	update, ok := ParseCodexWhamUsageQuotaSnapshot(body)
	if !ok {
		t.Fatal("expected quota snapshot update")
	}
	if update.Source != codexWhamUsageQuotaSource {
		t.Fatalf("source = %q, want %q", update.Source, codexWhamUsageQuotaSource)
	}
	if update.ObservedAt.IsZero() {
		t.Fatal("expected observed time")
	}
	if !update.FiveHourRemainingKnown || update.FiveHourRemainingPercent != 87.5 {
		t.Fatalf("5h update = (%v, %v), want known 87.5", update.FiveHourRemainingKnown, update.FiveHourRemainingPercent)
	}
	if !update.SevenDayRemainingKnown || update.SevenDayRemainingPercent != 56 {
		t.Fatalf("7d update = (%v, %v), want known 56", update.SevenDayRemainingKnown, update.SevenDayRemainingPercent)
	}
}

func TestParseCodexWhamUsageQuotaSnapshot_PrimaryOnly(t *testing.T) {
	t.Parallel()

	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":25}}}`)

	update, ok := ParseCodexWhamUsageQuotaSnapshot(body)
	if !ok {
		t.Fatal("expected quota snapshot update")
	}
	if !update.FiveHourRemainingKnown || update.FiveHourRemainingPercent != 75 {
		t.Fatalf("5h update = (%v, %v), want known 75", update.FiveHourRemainingKnown, update.FiveHourRemainingPercent)
	}
	if update.SevenDayRemainingKnown {
		t.Fatal("did not expect 7d update")
	}
}

func TestParseCodexWhamUsageQuotaSnapshot_SecondaryOnly(t *testing.T) {
	t.Parallel()

	body := []byte(`{"rate_limit":{"secondary_window":{"used_percent":10}}}`)

	update, ok := ParseCodexWhamUsageQuotaSnapshot(body)
	if !ok {
		t.Fatal("expected quota snapshot update")
	}
	if update.FiveHourRemainingKnown {
		t.Fatal("did not expect 5h update")
	}
	if !update.SevenDayRemainingKnown || update.SevenDayRemainingPercent != 90 {
		t.Fatalf("7d update = (%v, %v), want known 90", update.SevenDayRemainingKnown, update.SevenDayRemainingPercent)
	}
}

func TestParseCodexWhamUsageQuotaSnapshot_Clamp(t *testing.T) {
	t.Parallel()

	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":-25},"secondary_window":{"used_percent":180}}}`)

	update, ok := ParseCodexWhamUsageQuotaSnapshot(body)
	if !ok {
		t.Fatal("expected quota snapshot update")
	}
	if update.FiveHourRemainingPercent != 100 {
		t.Fatalf("5h percent = %v, want 100", update.FiveHourRemainingPercent)
	}
	if update.SevenDayRemainingPercent != 0 {
		t.Fatalf("7d percent = %v, want 0", update.SevenDayRemainingPercent)
	}
}

func TestParseCodexWhamUsageQuotaSnapshot_NoUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body []byte
	}{
		{name: "missing fields", body: []byte(`{"rate_limit":{"primary_window":{},"secondary_window":{}}}`)},
		{name: "missing rate limit", body: []byte(`{"ok":true}`)},
		{name: "invalid json", body: []byte(`{"rate_limit":`)},
		{name: "non numeric", body: []byte(`{"rate_limit":{"primary_window":{"used_percent":"10"}}}`)},
		{name: "empty", body: nil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if update, ok := ParseCodexWhamUsageQuotaSnapshot(tt.body); ok {
				t.Fatalf("ParseCodexWhamUsageQuotaSnapshot() = %#v, want no update", update)
			}
		})
	}
}

func TestManagerUpdateQuotaSnapshotFromHeaders_ThrottleAndLowQuotaBypass(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "test"}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if !manager.UpdateQuotaSnapshotFromHeaders(context.Background(), "auth-a", "test", "model", quotaHeadersForTest(80, 80)) {
		t.Fatal("expected first quota update")
	}
	if manager.UpdateQuotaSnapshotFromHeaders(context.Background(), "auth-a", "test", "model", quotaHeadersForTest(70, 70)) {
		t.Fatal("expected high quota update to be throttled")
	}
	updated, ok := manager.GetByID("auth-a")
	if !ok || updated.ModelStates["model"].Quota.FiveHourRemainingPercent != 80 {
		t.Fatalf("throttled 5h percent = %v, want 80", updated.ModelStates["model"].Quota.FiveHourRemainingPercent)
	}

	if !manager.UpdateQuotaSnapshotFromHeaders(context.Background(), "auth-a", "test", "model", quotaHeadersForTest(3, 8)) {
		t.Fatal("expected low quota update to bypass throttle")
	}
	updated, _ = manager.GetByID("auth-a")
	state := updated.ModelStates["model"]
	if state.Quota.FiveHourRemainingPercent != 3 || state.Quota.SevenDayRemainingPercent != 8 {
		t.Fatalf("low quota update = (%v, %v), want (3, 8)", state.Quota.FiveHourRemainingPercent, state.Quota.SevenDayRemainingPercent)
	}
}

func TestManagerUpdateQuotaSnapshotFromHeaders_PreservesCooldownFields(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	recoverAt := time.Now().Add(time.Minute)
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "auth-a",
		Provider: "test",
		ModelStates: map[string]*ModelState{
			"model": {
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: recoverAt,
					BackoffLevel:  3,
				},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if !manager.UpdateQuotaSnapshotFromHeaders(context.Background(), "auth-a", "test", "model", quotaHeadersForTest(55, 66)) {
		t.Fatal("expected quota update")
	}
	updated, _ := manager.GetByID("auth-a")
	quota := updated.ModelStates["model"].Quota
	if !quota.Exceeded || quota.Reason != "quota" || !quota.NextRecoverAt.Equal(recoverAt) || quota.BackoffLevel != 3 {
		t.Fatalf("cooldown fields changed: %#v", quota)
	}
	if quota.FiveHourRemainingPercent != 55 || quota.SevenDayRemainingPercent != 66 {
		t.Fatalf("snapshot fields = (%v, %v), want (55, 66)", quota.FiveHourRemainingPercent, quota.SevenDayRemainingPercent)
	}
}

func TestManagerUpdateAccountQuotaSnapshot_WritesAuthQuotaOnlyAndPreservesCooldownFields(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	recoverAt := time.Now().Add(time.Minute)
	observedAt := time.Now().UTC()
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "auth-a",
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: recoverAt,
			BackoffLevel:  2,
		},
		ModelStates: map[string]*ModelState{
			"model": {
				Quota: QuotaState{
					FiveHourRemainingKnown:   true,
					FiveHourRemainingPercent: 11,
					SevenDayRemainingKnown:   true,
					SevenDayRemainingPercent: 22,
				},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	changed, err := manager.UpdateAccountQuotaSnapshot(context.Background(), "auth-a", QuotaSnapshotUpdate{
		FiveHourRemainingKnown:   true,
		FiveHourRemainingPercent: 73,
		SevenDayRemainingKnown:   true,
		SevenDayRemainingPercent: 81,
		ObservedAt:               observedAt,
		Source:                   codexWhamUsageQuotaSource,
	})
	if err != nil {
		t.Fatalf("UpdateAccountQuotaSnapshot() error = %v", err)
	}
	if !changed {
		t.Fatal("expected account quota update")
	}

	updated, _ := manager.GetByID("auth-a")
	quota := updated.Quota
	if !quota.Exceeded || quota.Reason != "quota" || !quota.NextRecoverAt.Equal(recoverAt) || quota.BackoffLevel != 2 {
		t.Fatalf("cooldown fields changed: %#v", quota)
	}
	if !quota.FiveHourRemainingKnown || quota.FiveHourRemainingPercent != 73 {
		t.Fatalf("5h quota = (%v, %v), want known 73", quota.FiveHourRemainingKnown, quota.FiveHourRemainingPercent)
	}
	if !quota.SevenDayRemainingKnown || quota.SevenDayRemainingPercent != 81 {
		t.Fatalf("7d quota = (%v, %v), want known 81", quota.SevenDayRemainingKnown, quota.SevenDayRemainingPercent)
	}
	if !quota.SnapshotUpdatedAt.Equal(observedAt) {
		t.Fatalf("snapshot time = %v, want %v", quota.SnapshotUpdatedAt, observedAt)
	}
	modelQuota := updated.ModelStates["model"].Quota
	if modelQuota.FiveHourRemainingPercent != 11 || modelQuota.SevenDayRemainingPercent != 22 {
		t.Fatalf("model quota changed: %#v", modelQuota)
	}
}

func TestManagerUpdateAccountQuotaSnapshot_NoKnownFields(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "codex"}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	changed, err := manager.UpdateAccountQuotaSnapshot(context.Background(), "auth-a", QuotaSnapshotUpdate{})
	if err != nil {
		t.Fatalf("UpdateAccountQuotaSnapshot() error = %v", err)
	}
	if changed {
		t.Fatal("did not expect account quota update")
	}
}

func TestManagerExecute_PassivelyUpdatesQuotaSnapshotFromResponseHeaders(t *testing.T) {
	t.Parallel()

	const (
		provider = "quota-provider"
		model    = "quota-model"
		authID   = "quota-auth"
	)
	registerSchedulerModels(t, provider, model, authID)

	manager := NewManager(nil, &StickyQuotaProtectSelector{}, nil)
	manager.RegisterExecutor(quotaHeaderTestExecutor{provider: provider, headers: quotaHeadersForTest(44, 88)})
	if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if _, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	updated, _ := manager.GetByID(authID)
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state %q", model)
	}
	if state.Quota.FiveHourRemainingPercent != 44 || state.Quota.SevenDayRemainingPercent != 88 {
		t.Fatalf("quota snapshot = (%v, %v), want (44, 88)", state.Quota.FiveHourRemainingPercent, state.Quota.SevenDayRemainingPercent)
	}
}

func TestManagerExecuteStream_PassivelyUpdatesQuotaSnapshotAtBootstrap(t *testing.T) {
	t.Parallel()

	const (
		provider = "quota-stream-provider"
		model    = "quota-stream-model"
		authID   = "quota-stream-auth"
	)
	registerSchedulerModels(t, provider, model, authID)

	manager := NewManager(nil, &StickyQuotaProtectSelector{}, nil)
	manager.RegisterExecutor(quotaHeaderTestExecutor{provider: provider, headers: quotaHeadersForTest(61, 77)})
	if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result, err := manager.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	updated, _ := manager.GetByID(authID)
	state := updated.ModelStates[model]
	if state == nil || state.Quota.FiveHourRemainingPercent != 61 || state.Quota.SevenDayRemainingPercent != 77 {
		t.Fatalf("bootstrap quota snapshot = %#v, want 61/77", state)
	}
	for range result.Chunks {
	}

	updated, _ = manager.GetByID(authID)
	state = updated.ModelStates[model]
	if state == nil || state.Quota.FiveHourRemainingPercent != 61 || state.Quota.SevenDayRemainingPercent != 77 {
		t.Fatalf("final quota snapshot = %#v, want 61/77", state)
	}
}

func quotaHeadersForTest(fiveHour, sevenDay float64) http.Header {
	headers := http.Header{}
	headers.Set("X-Quota-5h-Remaining-Percent", strconvFormatFloatForTest(fiveHour))
	headers.Set("X-Quota-7d-Remaining-Percent", strconvFormatFloatForTest(sevenDay))
	return headers
}

func strconvFormatFloatForTest(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
