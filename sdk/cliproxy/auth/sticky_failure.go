package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

const (
	defaultStickyFailureUnbindThreshold = 2
	stickySessionLeaseMetadataKey       = "__cliproxy_sticky_session_lease"
)

type stickySessionLease struct {
	cache      *SessionCache
	sessionKey string
	authID     string
	threshold  int
	once       sync.Once
}

type stickyFailureAction int

const (
	stickyFailureNoop stickyFailureAction = iota
	stickyFailureClear
	stickyFailureCount
	stickyFailureUnbind
)

func newStickySessionLease(cache *SessionCache, sessionKey, authID string, threshold int) *stickySessionLease {
	if cache == nil || sessionKey == "" || authID == "" {
		return nil
	}
	if threshold <= 0 {
		threshold = defaultStickyFailureUnbindThreshold
	}
	return &stickySessionLease{
		cache:      cache,
		sessionKey: sessionKey,
		authID:     authID,
		threshold:  threshold,
	}
}

func (l *stickySessionLease) RecordResult(result Result) {
	if l == nil || l.cache == nil {
		return
	}
	l.once.Do(func() {
		switch stickyFailureActionForResult(result) {
		case stickyFailureClear:
			l.cache.RecordSuccess(l.sessionKey, l.authID)
		case stickyFailureCount:
			l.cache.RecordFailure(l.sessionKey, l.authID, l.threshold)
		case stickyFailureUnbind:
			l.cache.InvalidateIfAuth(l.sessionKey, l.authID)
		}
	})
}

func (l *stickySessionLease) RecordError(err error) {
	if l == nil || l.cache == nil {
		return
	}
	l.once.Do(func() {
		switch stickyFailureActionForError(err) {
		case stickyFailureCount:
			l.cache.RecordFailure(l.sessionKey, l.authID, l.threshold)
		case stickyFailureUnbind:
			l.cache.InvalidateIfAuth(l.sessionKey, l.authID)
		}
	})
}

func stickyFailureActionForResult(result Result) stickyFailureAction {
	if result.Success {
		return stickyFailureClear
	}
	if result.Error == nil {
		return stickyFailureCount
	}
	return stickyFailureActionForError(result.Error)
}

func stickyFailureActionForError(err error) stickyFailureAction {
	if err == nil {
		return stickyFailureNoop
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return stickyFailureNoop
	}
	if isRequestInvalidError(err) {
		return stickyFailureNoop
	}
	if isModelSupportError(err) {
		return stickyFailureUnbind
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
		return stickyFailureUnbind
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return stickyFailureCount
	default:
		return stickyFailureCount
	}
}

func attachStickySessionLease(meta map[string]any, lease *stickySessionLease) {
	if meta == nil || lease == nil {
		return
	}
	meta[stickySessionLeaseMetadataKey] = lease
}

func takeStickySessionLeaseFromMetadata(meta map[string]any) *stickySessionLease {
	if meta == nil {
		return nil
	}
	raw := meta[stickySessionLeaseMetadataKey]
	delete(meta, stickySessionLeaseMetadataKey)
	lease, _ := raw.(*stickySessionLease)
	return lease
}

func recordStickySessionResult(lease *stickySessionLease, result Result) {
	if lease != nil {
		lease.RecordResult(result)
	}
}

func recordStickySessionError(lease *stickySessionLease, err error) {
	if lease != nil {
		lease.RecordError(err)
	}
}
