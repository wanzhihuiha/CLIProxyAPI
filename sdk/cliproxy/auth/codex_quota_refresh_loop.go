package auth

import (
	"container/heap"
	"context"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultCodexQuotaRefreshInterval       = 10 * time.Minute
	defaultCodexQuotaRefreshMaxConcurrency = 2
)

// CodexQuotaRefreshOptions controls the proactive Codex quota snapshot worker.
type CodexQuotaRefreshOptions struct {
	Enabled        bool
	Interval       time.Duration
	MaxConcurrency int
}

type codexQuotaRefreshLoop struct {
	manager     *Manager
	interval    time.Duration
	concurrency int

	mu       sync.Mutex
	queue    refreshMinHeap
	index    map[string]*refreshHeapItem
	dirty    map[string]struct{}
	inFlight map[string]struct{}

	wakeCh chan struct{}
	jobs   chan string
}

func normalizeCodexQuotaRefreshOptions(opts CodexQuotaRefreshOptions) CodexQuotaRefreshOptions {
	if opts.Interval <= 0 {
		opts.Interval = defaultCodexQuotaRefreshInterval
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = defaultCodexQuotaRefreshMaxConcurrency
	}
	return opts
}

// StartCodexQuotaRefresh launches the proactive Codex quota snapshot worker.
func (m *Manager) StartCodexQuotaRefresh(parent context.Context, opts CodexQuotaRefreshOptions) {
	if m == nil {
		return
	}
	opts = normalizeCodexQuotaRefreshOptions(opts)
	if !opts.Enabled {
		m.StopCodexQuotaRefresh()
		return
	}
	if parent == nil {
		parent = context.Background()
	}

	m.mu.Lock()
	cancelPrev := m.codexQuotaRefreshCancel
	m.codexQuotaRefreshCancel = nil
	m.codexQuotaRefreshLoop = nil
	m.mu.Unlock()
	if cancelPrev != nil {
		cancelPrev()
	}

	ctx, cancelCtx := context.WithCancel(parent)
	loop := newCodexQuotaRefreshLoop(m, opts.Interval, opts.MaxConcurrency)

	m.mu.Lock()
	m.codexQuotaRefreshCancel = cancelCtx
	m.codexQuotaRefreshLoop = loop
	m.mu.Unlock()

	loop.rebuild(time.Now().UTC())
	go loop.run(ctx)
}

// StopCodexQuotaRefresh cancels the proactive Codex quota snapshot worker.
func (m *Manager) StopCodexQuotaRefresh() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.codexQuotaRefreshCancel
	m.codexQuotaRefreshCancel = nil
	m.codexQuotaRefreshLoop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// QueueCodexQuotaRefresh asks the background worker to reschedule one auth.
func (m *Manager) QueueCodexQuotaRefresh(authID string) {
	m.queueCodexQuotaRefreshReschedule(authID)
}

func (m *Manager) queueCodexQuotaRefreshReschedule(authID string) {
	if m == nil || strings.TrimSpace(authID) == "" {
		return
	}
	m.mu.RLock()
	loop := m.codexQuotaRefreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return
	}
	loop.queueReschedule(authID)
}

func (m *Manager) queueCodexQuotaRefreshUnschedule(authID string) {
	if m == nil || strings.TrimSpace(authID) == "" {
		return
	}
	m.mu.RLock()
	loop := m.codexQuotaRefreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return
	}
	loop.remove(authID)
}

func newCodexQuotaRefreshLoop(manager *Manager, interval time.Duration, concurrency int) *codexQuotaRefreshLoop {
	if interval <= 0 {
		interval = defaultCodexQuotaRefreshInterval
	}
	if concurrency <= 0 {
		concurrency = defaultCodexQuotaRefreshMaxConcurrency
	}
	jobBuffer := concurrency * 4
	if jobBuffer < 16 {
		jobBuffer = 16
	}
	return &codexQuotaRefreshLoop{
		manager:     manager,
		interval:    interval,
		concurrency: concurrency,
		index:       make(map[string]*refreshHeapItem),
		dirty:       make(map[string]struct{}),
		inFlight:    make(map[string]struct{}),
		wakeCh:      make(chan struct{}, 1),
		jobs:        make(chan string, jobBuffer),
	}
}

func (l *codexQuotaRefreshLoop) queueReschedule(authID string) {
	if l == nil || strings.TrimSpace(authID) == "" {
		return
	}
	l.mu.Lock()
	l.dirty[authID] = struct{}{}
	l.mu.Unlock()
	select {
	case l.wakeCh <- struct{}{}:
	default:
	}
}

func (l *codexQuotaRefreshLoop) run(ctx context.Context) {
	if l == nil || l.manager == nil {
		return
	}
	workers := l.concurrency
	if workers <= 0 {
		workers = defaultCodexQuotaRefreshMaxConcurrency
	}
	for i := 0; i < workers; i++ {
		go l.worker(ctx)
	}
	l.loop(ctx)
}

func (l *codexQuotaRefreshLoop) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case authID := <-l.jobs:
			if strings.TrimSpace(authID) == "" {
				continue
			}
			result, err := l.manager.RefreshCodexQuota(ctx, authID)
			if err != nil && ctx.Err() == nil && log.IsLevelEnabled(log.DebugLevel) {
				log.WithFields(log.Fields{
					"auth_id":    authID,
					"status":     result.StatusCode,
					"next_retry": result.NextRetryAfter,
				}).Debugf("codex quota refresh failed: %v", err)
			}
			l.finish(authID)
			l.queueReschedule(authID)
		}
	}
}

func (l *codexQuotaRefreshLoop) rebuild(now time.Time) {
	type entry struct {
		id   string
		next time.Time
	}

	entries := make([]entry, 0)
	l.manager.mu.RLock()
	for id, auth := range l.manager.auths {
		next, ok := nextCodexQuotaRefreshAt(now, auth, l.interval)
		if !ok {
			continue
		}
		entries = append(entries, entry{id: id, next: next})
	}
	l.manager.mu.RUnlock()

	l.mu.Lock()
	l.queue = l.queue[:0]
	l.index = make(map[string]*refreshHeapItem, len(entries))
	for _, item := range entries {
		heapItem := &refreshHeapItem{id: item.id, next: item.next}
		heap.Push(&l.queue, heapItem)
		l.index[item.id] = heapItem
	}
	l.mu.Unlock()
}

func (l *codexQuotaRefreshLoop) loop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	var timerCh <-chan time.Time
	l.resetTimer(timer, &timerCh, time.Now().UTC())

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wakeCh:
			now := time.Now().UTC()
			l.applyDirty(now)
			l.resetTimer(timer, &timerCh, now)
		case <-timerCh:
			now := time.Now().UTC()
			l.handleDue(ctx, now)
			l.applyDirty(now)
			l.resetTimer(timer, &timerCh, now)
		}
	}
}

func (l *codexQuotaRefreshLoop) resetTimer(timer *time.Timer, timerCh *<-chan time.Time, now time.Time) {
	next, ok := l.peek()
	if !ok {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		*timerCh = nil
		return
	}
	wait := next.Sub(now)
	if wait < 0 {
		wait = 0
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(wait)
	*timerCh = timer.C
}

func (l *codexQuotaRefreshLoop) peek() (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) == 0 {
		return time.Time{}, false
	}
	return l.queue[0].next, true
}

func (l *codexQuotaRefreshLoop) handleDue(ctx context.Context, now time.Time) {
	due := l.popDue(now)
	for _, authID := range due {
		l.handleDueAuth(ctx, now, authID)
	}
}

func (l *codexQuotaRefreshLoop) popDue(now time.Time) []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var due []string
	for len(l.queue) > 0 {
		item := l.queue[0]
		if item == nil || item.next.After(now) {
			break
		}
		popped := heap.Pop(&l.queue).(*refreshHeapItem)
		if popped == nil {
			continue
		}
		delete(l.index, popped.id)
		due = append(due, popped.id)
	}
	return due
}

func (l *codexQuotaRefreshLoop) handleDueAuth(ctx context.Context, now time.Time, authID string) {
	if strings.TrimSpace(authID) == "" {
		return
	}

	l.manager.mu.RLock()
	auth := l.manager.auths[authID]
	next, shouldSchedule := nextCodexQuotaRefreshAt(now, auth, l.interval)
	l.manager.mu.RUnlock()

	if !shouldSchedule {
		l.remove(authID)
		return
	}
	if next.After(now) {
		l.upsert(authID, next)
		return
	}
	if !l.begin(authID) {
		return
	}

	select {
	case <-ctx.Done():
		l.finish(authID)
	case l.jobs <- authID:
	}
}

func (l *codexQuotaRefreshLoop) applyDirty(now time.Time) {
	dirty := l.drainDirty()
	for _, authID := range dirty {
		if l.isInFlight(authID) {
			continue
		}
		l.manager.mu.RLock()
		auth := l.manager.auths[authID]
		next, ok := nextCodexQuotaRefreshAt(now, auth, l.interval)
		l.manager.mu.RUnlock()
		if !ok {
			l.remove(authID)
			continue
		}
		l.upsert(authID, next)
	}
}

func (l *codexQuotaRefreshLoop) drainDirty() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.dirty))
	for authID := range l.dirty {
		out = append(out, authID)
		delete(l.dirty, authID)
	}
	return out
}

func (l *codexQuotaRefreshLoop) begin(authID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.inFlight[authID]; ok {
		return false
	}
	l.inFlight[authID] = struct{}{}
	return true
}

func (l *codexQuotaRefreshLoop) finish(authID string) {
	l.mu.Lock()
	delete(l.inFlight, authID)
	l.mu.Unlock()
}

func (l *codexQuotaRefreshLoop) isInFlight(authID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.inFlight[authID]
	return ok
}

func (l *codexQuotaRefreshLoop) upsert(authID string, next time.Time) {
	if strings.TrimSpace(authID) == "" || next.IsZero() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.inFlight[authID]; ok {
		return
	}
	if item, ok := l.index[authID]; ok && item != nil {
		item.next = next
		heap.Fix(&l.queue, item.index)
		return
	}
	item := &refreshHeapItem{id: authID, next: next}
	heap.Push(&l.queue, item)
	l.index[authID] = item
}

func (l *codexQuotaRefreshLoop) remove(authID string) {
	if strings.TrimSpace(authID) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item, ok := l.index[authID]
	if !ok || item == nil {
		return
	}
	heap.Remove(&l.queue, item.index)
	delete(l.index, authID)
}

func nextCodexQuotaRefreshAt(now time.Time, auth *Auth, interval time.Duration) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	if interval <= 0 {
		interval = defaultCodexQuotaRefreshInterval
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return time.Time{}, false
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return time.Time{}, false
	}
	if !codexQuotaRefreshAutoEligible(auth) {
		return time.Time{}, false
	}
	if nextRetry, ok := codexQuotaRefreshNextRetryAfter(auth); ok && nextRetry.After(now) {
		return nextRetry, true
	}
	if !quotaHasSnapshot(auth.Quota) || auth.Quota.SnapshotUpdatedAt.IsZero() {
		return now, true
	}

	periodicAt := auth.Quota.SnapshotUpdatedAt.Add(interval)
	expiredAt := auth.Quota.SnapshotUpdatedAt.Add(quotaSnapshotStaleGraceTTL)
	next := periodicAt
	if expiredAt.Before(next) {
		next = expiredAt
	}
	if !next.After(now) {
		return now, true
	}
	return next, true
}

func codexQuotaRefreshNextRetryAfter(auth *Auth) (time.Time, bool) {
	if auth == nil || len(auth.Metadata) == 0 {
		return time.Time{}, false
	}
	return lookupMetadataTime(auth.Metadata, codexQuotaMetadataNextRetryKey)
}

func codexQuotaRefreshAutoEligible(auth *Auth) bool {
	if auth == nil || len(auth.Metadata) == 0 {
		return false
	}
	token, ok := auth.Metadata["access_token"].(string)
	return ok && strings.TrimSpace(token) != ""
}
