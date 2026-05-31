package cliproxy

import (
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	routingStrategyRoundRobin         = "round-robin"
	routingStrategyFillFirst          = "fill-first"
	routingStrategyStickyQuotaProtect = "sticky-quota-protect"
)

func normalizeRoutingStrategyName(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "fill-first", "fillfirst", "ff":
		return routingStrategyFillFirst
	case "sticky-quota-protect", "sticky_quota_protect", "stickyquotaprotect", "sqp":
		return routingStrategyStickyQuotaProtect
	default:
		return routingStrategyRoundRobin
	}
}

func newRoutingSelector(strategy string, sessionAffinity bool, sessionAffinityTTL string) coreauth.Selector {
	normalized := normalizeRoutingStrategyName(strategy)
	var selector coreauth.Selector
	var activeSessions *coreauth.ActiveSessionTracker
	switch normalized {
	case routingStrategyFillFirst:
		selector = &coreauth.FillFirstSelector{}
	case routingStrategyStickyQuotaProtect:
		activeSessions = coreauth.NewActiveSessionTracker(coreauth.ActiveSessionConfig{})
		selector = coreauth.NewStickyQuotaProtectSelector(activeSessions)
		sessionAffinity = true
	default:
		selector = &coreauth.RoundRobinSelector{}
	}

	if sessionAffinity {
		selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
			Fallback:       selector,
			TTL:            parseSessionAffinityTTL(sessionAffinityTTL),
			ActiveSessions: activeSessions,
		})
	}
	return selector
}

func effectiveSessionAffinity(strategy string, sessionAffinity bool) bool {
	return sessionAffinity || normalizeRoutingStrategyName(strategy) == routingStrategyStickyQuotaProtect
}

func parseSessionAffinityTTL(ttl string) time.Duration {
	parsed, err := time.ParseDuration(strings.TrimSpace(ttl))
	if err != nil || parsed <= 0 {
		return time.Hour
	}
	return parsed
}
