package auth

import (
	"context"
	"sort"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	stickyQuotaFiveHourHighThreshold   = 60.0
	stickyQuotaFiveHourMediumThreshold = 30.0
	stickyQuotaFiveHourMinThreshold    = 5.0
	stickyQuotaSevenDayHealthy         = 50.0
	stickyQuotaSevenDayLow             = 20.0
	stickyQuotaSevenDayMin             = 10.0

	stickyQuotaMaxNewSessionsPerAccount   = 3
	stickyQuotaOverflowEnabled            = true
	stickyQuotaOverflowExtraSessions      = 1
	stickyQuotaOverflowMinFiveHourPercent = 60.0
	stickyQuotaOverflowMinSevenDayPercent = 50.0
)

// StickyQuotaProtectSelector protects short-window quota for new session
// bindings while leaving existing session affinity to SessionAffinitySelector.
type StickyQuotaProtectSelector struct {
	activeSessions *ActiveSessionTracker
}

type stickyQuotaCandidate struct {
	auth            *Auth
	freshness       int
	fiveHourBucket  int
	fiveHourPercent float64
	sevenDayPenalty int
	sevenDayPercent float64
	activeSessions  int
	priority        int
}

func NewStickyQuotaProtectSelector(activeSessions *ActiveSessionTracker) *StickyQuotaProtectSelector {
	return &StickyQuotaProtectSelector{activeSessions: activeSessions}
}

func (s *StickyQuotaProtectSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, errAvailable := getAvailableAuthsWithoutPriority(auths, provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	candidates := make([]stickyQuotaCandidate, 0, len(available))
	for _, auth := range available {
		candidate, ok := s.newStickyQuotaCandidate(auth, model, now)
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available after quota filters"}
	}
	candidates = stickyQuotaCapacityCandidates(candidates)
	if len(candidates) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available after quota capacity filters"}
	}

	sortStickyQuotaCandidates(candidates)
	return candidates[0].auth, nil
}

func sortStickyQuotaCandidates(candidates []stickyQuotaCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.freshness != right.freshness {
			return left.freshness < right.freshness
		}
		if left.fiveHourBucket != right.fiveHourBucket {
			return left.fiveHourBucket < right.fiveHourBucket
		}
		if left.sevenDayPenalty != right.sevenDayPenalty {
			return left.sevenDayPenalty < right.sevenDayPenalty
		}
		if left.activeSessions != right.activeSessions {
			return left.activeSessions < right.activeSessions
		}
		if left.fiveHourPercent != right.fiveHourPercent {
			return left.fiveHourPercent > right.fiveHourPercent
		}
		if left.sevenDayPercent != right.sevenDayPercent {
			return left.sevenDayPercent > right.sevenDayPercent
		}
		if left.priority != right.priority {
			return left.priority > right.priority
		}
		return left.auth.ID < right.auth.ID
	})
}

func stickyQuotaCapacityCandidates(candidates []stickyQuotaCandidate) []stickyQuotaCandidate {
	if len(candidates) == 0 {
		return candidates
	}

	normal := make([]stickyQuotaCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.activeSessions < stickyQuotaMaxNewSessionsPerAccount {
			normal = append(normal, candidate)
		}
	}
	if len(normal) > 0 {
		return normal
	}
	if !stickyQuotaOverflowEnabled || stickyQuotaOverflowExtraSessions <= 0 {
		return nil
	}

	overflowMaxSessions := stickyQuotaMaxNewSessionsPerAccount + stickyQuotaOverflowExtraSessions
	overflow := make([]stickyQuotaCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.activeSessions >= overflowMaxSessions {
			continue
		}
		if !stickyQuotaOverflowEligible(candidate) {
			continue
		}
		overflow = append(overflow, candidate)
	}
	return overflow
}

func stickyQuotaOverflowEligible(candidate stickyQuotaCandidate) bool {
	return candidate.fiveHourPercent >= stickyQuotaOverflowMinFiveHourPercent &&
		candidate.sevenDayPercent >= stickyQuotaOverflowMinSevenDayPercent
}

func selectorNeedsAllPriorityCandidates(selector Selector) bool {
	switch selected := selector.(type) {
	case *StickyQuotaProtectSelector:
		return true
	case *SessionAffinitySelector:
		if selected == nil {
			return false
		}
		return selectorNeedsAllPriorityCandidates(selected.fallback)
	default:
		return false
	}
}

func getAvailableAuthsWithoutPriority(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	available := make([]*Auth, 0, len(auths))
	cooldownCount := 0
	earliest := time.Time{}
	for _, candidate := range auths {
		blocked, reason, next := isAuthBlockedForModel(candidate, model, now)
		if !blocked {
			available = append(available, candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
			if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}
	if len(available) > 0 {
		return available, nil
	}
	if cooldownCount == len(auths) && !earliest.IsZero() {
		providerForError := provider
		if providerForError == "mixed" {
			providerForError = ""
		}
		resetIn := earliest.Sub(now)
		if resetIn < 0 {
			resetIn = 0
		}
		return nil, newModelCooldownError(model, providerForError, resetIn)
	}
	return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
}

func (s *StickyQuotaProtectSelector) newStickyQuotaCandidate(auth *Auth, model string, now time.Time) (stickyQuotaCandidate, bool) {
	quota := quotaStateForSelection(auth, model)
	freshness, okFreshness := quotaSnapshotFreshnessPenalty(quota, now)
	if !okFreshness {
		return stickyQuotaCandidate{}, false
	}
	fiveBucket, fivePercent, okFive := stickyQuotaFiveHourRank(quota)
	if !okFive {
		return stickyQuotaCandidate{}, false
	}
	sevenPenalty, sevenPercent, okSeven := stickyQuotaSevenDayRank(quota)
	if !okSeven {
		return stickyQuotaCandidate{}, false
	}
	return stickyQuotaCandidate{
		auth:            auth,
		freshness:       freshness,
		fiveHourBucket:  fiveBucket,
		fiveHourPercent: fivePercent,
		sevenDayPenalty: sevenPenalty,
		sevenDayPercent: sevenPercent,
		activeSessions:  s.activeSessionCount(auth.ID, now),
		priority:        authPriority(auth),
	}, true
}

func (s *StickyQuotaProtectSelector) activeSessionCount(authID string, now time.Time) int {
	if s == nil || s.activeSessions == nil {
		return 0
	}
	return s.activeSessions.Count(authID, now)
}

func quotaStateForSelection(auth *Auth, model string) QuotaState {
	if auth == nil {
		return QuotaState{}
	}
	quota := auth.Quota
	if model != "" && len(auth.ModelStates) > 0 {
		if state := modelStateForSelection(auth, model); state != nil {
			quota = mergeQuotaSnapshotForSelection(quota, state.Quota)
		}
	}
	return quota
}

func modelStateForSelection(auth *Auth, model string) *ModelState {
	if auth == nil || len(auth.ModelStates) == 0 {
		return nil
	}
	if state := auth.ModelStates[model]; state != nil {
		return state
	}
	baseModel := canonicalModelKey(model)
	if baseModel != "" && baseModel != model {
		return auth.ModelStates[baseModel]
	}
	return nil
}

func quotaHasSnapshot(quota QuotaState) bool {
	return quota.FiveHourRemainingKnown || quota.SevenDayRemainingKnown
}

func mergeQuotaSnapshotForSelection(base, override QuotaState) QuotaState {
	if !quotaHasSnapshot(override) {
		return base
	}
	merged := base
	if override.FiveHourRemainingKnown {
		merged.FiveHourRemainingKnown = true
		merged.FiveHourRemainingPercent = override.FiveHourRemainingPercent
	}
	if override.SevenDayRemainingKnown {
		merged.SevenDayRemainingKnown = true
		merged.SevenDayRemainingPercent = override.SevenDayRemainingPercent
	}
	if !override.SnapshotUpdatedAt.IsZero() {
		merged.SnapshotUpdatedAt = override.SnapshotUpdatedAt
	}
	return merged
}

func stickyQuotaFiveHourRank(quota QuotaState) (bucket int, percent float64, ok bool) {
	if !quota.FiveHourRemainingKnown {
		return 3, -1, true
	}
	percent = normalizedQuotaPercent(quota.FiveHourRemainingPercent)
	switch {
	case percent < stickyQuotaFiveHourMinThreshold:
		return 0, percent, false
	case percent >= stickyQuotaFiveHourHighThreshold:
		return 0, percent, true
	case percent >= stickyQuotaFiveHourMediumThreshold:
		return 1, percent, true
	default:
		return 2, percent, true
	}
}

func stickyQuotaSevenDayRank(quota QuotaState) (penalty int, percent float64, ok bool) {
	if !quota.SevenDayRemainingKnown {
		return 3, -1, true
	}
	percent = normalizedQuotaPercent(quota.SevenDayRemainingPercent)
	switch {
	case percent < stickyQuotaSevenDayMin:
		return 0, percent, false
	case percent >= stickyQuotaSevenDayHealthy:
		return 0, percent, true
	case percent >= stickyQuotaSevenDayLow:
		return 1, percent, true
	default:
		return 2, percent, true
	}
}

func normalizedQuotaPercent(percent float64) float64 {
	switch {
	case percent < 0:
		return 0
	case percent > 100:
		return 100
	default:
		return percent
	}
}
