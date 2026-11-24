package agent

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultFastInterval      = 3 * time.Second
	minFastInterval          = 1 * time.Second
	maxFastInterval          = 30 * time.Second
	defaultIdleInterval      = 15 * time.Second
	minIdleInterval          = 5 * time.Second
	maxIdleInterval          = 5 * time.Minute
	defaultAdminActiveWindow = 60 * time.Second
	minAdminActiveWindow     = 10 * time.Second
	maxAdminActiveWindow     = 30 * time.Minute
	minDynamicPresenceTTL    = 35 * time.Second
)

var (
	lastAdminActivityUnixNano atomic.Int64
	activeAdminDashboards     atomic.Int64
	activityNowFn             = time.Now
)

// TouchAdminActivity records a recent authenticated admin action or dashboard interaction.
func TouchAdminActivity() {
	lastAdminActivityUnixNano.Store(activityNowFn().UnixNano())
}

// DashboardAdminConnected tracks an authenticated admin connected to /api/clients.
func DashboardAdminConnected() {
	activeAdminDashboards.Add(1)
	TouchAdminActivity()
}

// DashboardAdminDisconnected decrements the active authenticated admin dashboard count.
func DashboardAdminDisconnected() {
	if activeAdminDashboards.Add(-1) < 0 {
		activeAdminDashboards.Store(0)
	}
	TouchAdminActivity()
}

// ResetAdminActivityForTest clears activity state for unit tests.
func ResetAdminActivityForTest() {
	lastAdminActivityUnixNano.Store(0)
	activeAdminDashboards.Store(0)
}

// IdleInterval returns the slow reporting interval used when no admin is active and no events are pending.
func IdleInterval() time.Duration {
	return envDurationSeconds("LITE_AGENT_IDLE_INTERVAL_SEC", defaultIdleInterval, minIdleInterval, maxIdleInterval)
}

// AdminActiveWindow returns how long after the last admin request the fast interval stays active.
func AdminActiveWindow() time.Duration {
	return envDurationSeconds("LITE_AGENT_ACTIVE_WINDOW_SEC", defaultAdminActiveWindow, minAdminActiveWindow, maxAdminActiveWindow)
}

func envDurationSeconds(key string, fallback, minVal, maxVal time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	secs, err := strconv.ParseFloat(raw, 64)
	if err != nil || secs <= 0 {
		return fallback
	}
	d := time.Duration(secs * float64(time.Second))
	if d < minVal {
		return minVal
	}
	if d > maxVal {
		return maxVal
	}
	return d
}

// IsAdminActive reports whether an admin is currently logged in and active.
func IsAdminActive() bool {
	if activeAdminDashboards.Load() > 0 {
		return true
	}
	last := lastAdminActivityUnixNano.Load()
	if last <= 0 {
		return false
	}
	elapsed := activityNowFn().Sub(time.Unix(0, last))
	return elapsed >= 0 && elapsed < AdminActiveWindow()
}

// ComputeNextReportInterval calculates the adaptive next reporting interval for an agent.
// Fast interval is used when an admin is active or the node has pending events;
// otherwise the slow idle interval is returned.
func ComputeNextReportInterval(configuredIntervalSec float64, hasPendingEvents bool) time.Duration {
	fast := defaultFastInterval
	if configuredIntervalSec > 0 {
		fast = time.Duration(configuredIntervalSec * float64(time.Second))
	}
	if fast < minFastInterval {
		fast = minFastInterval
	}
	if fast > maxFastInterval {
		fast = maxFastInterval
	}
	if hasPendingEvents || IsAdminActive() {
		return fast
	}
	idle := IdleInterval()
	if idle < fast {
		idle = fast
	}
	return idle
}

// PresenceTTLForInterval computes a safe presence expiration window for a given reporting interval,
// guaranteeing max(35s, 3 * nextInterval + 10s) to prevent false offline alerts during idle slow mode.
func PresenceTTLForInterval(nextInterval time.Duration) time.Duration {
	if nextInterval <= 0 {
		return minDynamicPresenceTTL
	}
	ttl := 3*nextInterval + 10*time.Second
	if ttl < minDynamicPresenceTTL {
		return minDynamicPresenceTTL
	}
	return ttl
}
