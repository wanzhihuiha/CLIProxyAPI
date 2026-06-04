package auth

import (
	"strings"
	"sync"
	"time"
)

const (
	defaultActiveSessionIdleTimeout = 15 * time.Minute
	defaultActiveSessionHardTTL     = 2 * time.Hour

	activeSessionLeaseMetadataKey = "__cliproxy_active_session_lease"
)

// ActiveSessionConfig controls active sticky session accounting.
type ActiveSessionConfig struct {
	IdleTimeout time.Duration
	HardTTL     time.Duration
}

// ActiveSessionSnapshot exposes active session counters for tests and observability.
type ActiveSessionSnapshot struct {
	ActiveSessions int
	InFlight       int
}

type activeSessionEntry struct {
	sessionKey            string
	authID                string
	createdAt             time.Time
	lastSeenAt            time.Time
	hardExpiresAt         time.Time
	inFlight              int
	expiredPendingRelease bool
}

// ActiveSessionTracker tracks active sticky session occupancy independently
// from the longer-lived session affinity binding cache.
type ActiveSessionTracker struct {
	mu      sync.Mutex
	cfg     ActiveSessionConfig
	entries map[string]*activeSessionEntry
	counts  map[string]int
	expired map[string]time.Time
}

// ActiveSessionLease represents one in-flight request using a sticky binding.
type ActiveSessionLease struct {
	tracker    *ActiveSessionTracker
	sessionKey string
	authID     string
	once       sync.Once
}

func NewActiveSessionTracker(cfg ActiveSessionConfig) *ActiveSessionTracker {
	cfg = normalizeActiveSessionConfig(cfg)
	return &ActiveSessionTracker{
		cfg:     cfg,
		entries: make(map[string]*activeSessionEntry),
		counts:  make(map[string]int),
		expired: make(map[string]time.Time),
	}
}

func normalizeActiveSessionConfig(cfg ActiveSessionConfig) ActiveSessionConfig {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultActiveSessionIdleTimeout
	}
	if cfg.HardTTL <= 0 {
		cfg.HardTTL = defaultActiveSessionHardTTL
	}
	return cfg
}

func (t *ActiveSessionTracker) Begin(sessionKey, authID string, now time.Time) *ActiveSessionLease {
	if t == nil || sessionKey == "" || authID == "" {
		return nil
	}
	now = normalizeActiveSessionTime(now)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.pruneLocked(now)
	key := activeSessionEntryKey(sessionKey, authID)
	delete(t.expired, key)

	entry := t.entries[key]
	if entry == nil {
		entry = &activeSessionEntry{
			sessionKey:    sessionKey,
			authID:        authID,
			createdAt:     now,
			lastSeenAt:    now,
			hardExpiresAt: now.Add(t.cfg.HardTTL),
			inFlight:      1,
		}
		t.entries[key] = entry
		t.counts[authID]++
	} else {
		if t.entryHardExpiredLocked(entry, now) {
			entry.expiredPendingRelease = true
		}
		entry.inFlight++
		entry.lastSeenAt = now
	}

	return &ActiveSessionLease{tracker: t, sessionKey: sessionKey, authID: authID}
}

func (t *ActiveSessionTracker) ShouldRebind(sessionKey, authID string, now time.Time) bool {
	if t == nil || sessionKey == "" || authID == "" {
		return false
	}
	now = normalizeActiveSessionTime(now)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.pruneLocked(now)
	key := activeSessionEntryKey(sessionKey, authID)
	if _, ok := t.expired[key]; ok {
		delete(t.expired, key)
		return true
	}
	entry := t.entries[key]
	if entry == nil {
		return false
	}
	if t.entryHardExpiredLocked(entry, now) && entry.inFlight == 0 {
		t.removeEntryLocked(key, now, true)
		return true
	}
	return false
}

func (t *ActiveSessionTracker) MoveSession(oldSessionKey, newSessionKey, authID string, now time.Time) {
	if t == nil || oldSessionKey == "" || newSessionKey == "" || authID == "" || oldSessionKey == newSessionKey {
		return
	}
	now = normalizeActiveSessionTime(now)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.pruneLocked(now)
	oldKey := activeSessionEntryKey(oldSessionKey, authID)
	newKey := activeSessionEntryKey(newSessionKey, authID)
	if expiredAt, ok := t.expired[oldKey]; ok {
		delete(t.expired, oldKey)
		t.expired[newKey] = expiredAt
	}
	oldEntry := t.entries[oldKey]
	if oldEntry == nil {
		return
	}
	newEntry := t.entries[newKey]
	if newEntry == nil {
		delete(t.entries, oldKey)
		oldEntry.sessionKey = newSessionKey
		t.entries[newKey] = oldEntry
		return
	}

	newEntry.inFlight += oldEntry.inFlight
	if oldEntry.createdAt.Before(newEntry.createdAt) {
		newEntry.createdAt = oldEntry.createdAt
	}
	if oldEntry.lastSeenAt.After(newEntry.lastSeenAt) {
		newEntry.lastSeenAt = oldEntry.lastSeenAt
	}
	if newEntry.hardExpiresAt.IsZero() || (!oldEntry.hardExpiresAt.IsZero() && oldEntry.hardExpiresAt.Before(newEntry.hardExpiresAt)) {
		newEntry.hardExpiresAt = oldEntry.hardExpiresAt
	}
	newEntry.expiredPendingRelease = newEntry.expiredPendingRelease || oldEntry.expiredPendingRelease
	delete(t.entries, oldKey)
	t.decrementCountLocked(authID)
}

func (t *ActiveSessionTracker) InvalidateAuth(authID string) {
	if t == nil || authID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, entry := range t.entries {
		if entry != nil && entry.authID == authID {
			delete(t.entries, key)
		}
	}
	for key := range t.expired {
		if activeSessionEntryKeyAuthID(key) == authID {
			delete(t.expired, key)
		}
	}
	delete(t.counts, authID)
}

func (t *ActiveSessionTracker) Count(authID string, now time.Time) int {
	if t == nil || authID == "" {
		return 0
	}
	now = normalizeActiveSessionTime(now)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.pruneLocked(now)
	return t.counts[authID]
}

func (t *ActiveSessionTracker) Snapshot(authID string, now time.Time) ActiveSessionSnapshot {
	if t == nil || authID == "" {
		return ActiveSessionSnapshot{}
	}
	now = normalizeActiveSessionTime(now)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.pruneLocked(now)
	snapshot := ActiveSessionSnapshot{ActiveSessions: t.counts[authID]}
	for _, entry := range t.entries {
		if entry != nil && entry.authID == authID {
			snapshot.InFlight += entry.inFlight
		}
	}
	return snapshot
}

func (l *ActiveSessionLease) Release() {
	if l == nil || l.tracker == nil {
		return
	}
	l.once.Do(func() {
		l.tracker.end(l.sessionKey, l.authID, time.Now())
	})
}

func (t *ActiveSessionTracker) end(sessionKey, authID string, now time.Time) {
	if t == nil || sessionKey == "" || authID == "" {
		return
	}
	now = normalizeActiveSessionTime(now)

	t.mu.Lock()
	defer t.mu.Unlock()

	key := activeSessionEntryKey(sessionKey, authID)
	entry := t.entries[key]
	if entry == nil {
		return
	}
	if entry.inFlight > 0 {
		entry.inFlight--
	}
	entry.lastSeenAt = now
	if entry.inFlight == 0 && (entry.expiredPendingRelease || t.entryHardExpiredLocked(entry, now)) {
		t.removeEntryLocked(key, now, true)
	}
}

func (t *ActiveSessionTracker) pruneLocked(now time.Time) {
	for key, entry := range t.entries {
		if entry == nil {
			delete(t.entries, key)
			continue
		}
		if t.entryHardExpiredLocked(entry, now) {
			if entry.inFlight > 0 {
				entry.expiredPendingRelease = true
				continue
			}
			t.removeEntryLocked(key, now, true)
			continue
		}
		if entry.inFlight == 0 && t.entryIdleExpiredLocked(entry, now) {
			t.removeEntryLocked(key, now, false)
		}
	}
	for key, expiredAt := range t.expired {
		if expiredAt.IsZero() || now.Sub(expiredAt) > t.cfg.HardTTL {
			delete(t.expired, key)
		}
	}
}

func (t *ActiveSessionTracker) entryIdleExpiredLocked(entry *activeSessionEntry, now time.Time) bool {
	if t == nil || entry == nil || t.cfg.IdleTimeout <= 0 {
		return false
	}
	lastSeen := entry.lastSeenAt
	if lastSeen.IsZero() {
		lastSeen = entry.createdAt
	}
	return !lastSeen.IsZero() && now.Sub(lastSeen) > t.cfg.IdleTimeout
}

func (t *ActiveSessionTracker) entryHardExpiredLocked(entry *activeSessionEntry, now time.Time) bool {
	if t == nil || entry == nil || t.cfg.HardTTL <= 0 || entry.hardExpiresAt.IsZero() {
		return false
	}
	return !now.Before(entry.hardExpiresAt)
}

func (t *ActiveSessionTracker) removeEntryLocked(key string, now time.Time, rememberHardExpiry bool) {
	entry := t.entries[key]
	if entry == nil {
		return
	}
	delete(t.entries, key)
	t.decrementCountLocked(entry.authID)
	if rememberHardExpiry {
		t.expired[key] = now
	}
}

func (t *ActiveSessionTracker) decrementCountLocked(authID string) {
	if authID == "" {
		return
	}
	count := t.counts[authID]
	if count <= 1 {
		delete(t.counts, authID)
		return
	}
	t.counts[authID] = count - 1
}

func activeSessionEntryKey(sessionKey, authID string) string {
	return sessionKey + "\x00" + authID
}

func activeSessionEntryKeyAuthID(key string) string {
	idx := strings.LastIndexByte(key, 0)
	if idx < 0 || idx+1 >= len(key) {
		return ""
	}
	return key[idx+1:]
}

func normalizeActiveSessionTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
}

func attachActiveSessionLease(meta map[string]any, lease *ActiveSessionLease) {
	if meta == nil || lease == nil {
		return
	}
	meta[activeSessionLeaseMetadataKey] = lease
}

func takeActiveSessionLeaseFromMetadata(meta map[string]any) *ActiveSessionLease {
	if meta == nil {
		return nil
	}
	raw := meta[activeSessionLeaseMetadataKey]
	delete(meta, activeSessionLeaseMetadataKey)
	lease, _ := raw.(*ActiveSessionLease)
	return lease
}

func releaseActiveSessionLease(lease *ActiveSessionLease) {
	if lease != nil {
		lease.Release()
	}
}
