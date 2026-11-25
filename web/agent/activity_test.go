package agent

import (
	"testing"
	"time"
)

func TestComputeNextReportIntervalAdaptive(t *testing.T) {
	ResetAdminActivityForTest()
	origNow := activityNowFn
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	activityNowFn = func() time.Time { return now }
	t.Cleanup(func() {
		activityNowFn = origNow
		ResetAdminActivityForTest()
	})

	// Idle: no admin, no pending events -> 15s default idle
	if got := ComputeNextReportInterval(3.0, false); got != 15*time.Second {
		t.Fatalf("idle interval = %v, want 15s", got)
	}

	// Pending events -> fast interval even when no admin is active
	if got := ComputeNextReportInterval(2.0, true); got != 2*time.Second {
		t.Fatalf("pending-events interval = %v, want 2s", got)
	}

	// Admin active -> fast interval
	TouchAdminActivity()
	if got := ComputeNextReportInterval(3.0, false); got != 3*time.Second {
		t.Fatalf("admin-active interval = %v, want 3s", got)
	}

	// After 61s -> back to idle
	now = now.Add(61 * time.Second)
	if got := ComputeNextReportInterval(3.0, false); got != 15*time.Second {
		t.Fatalf("expired admin window interval = %v, want 15s", got)
	}

	// Active dashboard connection keeps fast interval even after window passes
	DashboardAdminConnected()
	now = now.Add(5 * time.Minute)
	if got := ComputeNextReportInterval(2.5, false); got != 2500*time.Millisecond {
		t.Fatalf("dashboard-connected interval = %v, want 2.5s", got)
	}
	DashboardAdminDisconnected()
	now = now.Add(61 * time.Second)
	if got := ComputeNextReportInterval(2.5, false); got != 15*time.Second {
		t.Fatalf("after dashboard disconnect interval = %v, want 15s", got)
	}
}

func TestPresenceTTLForInterval(t *testing.T) {
	if got := PresenceTTLForInterval(3 * time.Second); got != 35*time.Second {
		t.Fatalf("TTL(3s) = %v, want 35s", got)
	}
	if got := PresenceTTLForInterval(15 * time.Second); got != 55*time.Second {
		t.Fatalf("TTL(15s) = %v, want 55s", got)
	}
	if got := PresenceTTLForInterval(30 * time.Second); got != 100*time.Second {
		t.Fatalf("TTL(30s) = %v, want 100s", got)
	}
}

func TestComputeNextStatsInterval(t *testing.T) {
	ResetAdminActivityForTest()
	origNow := activityNowFn
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	activityNowFn = func() time.Time { return now }
	t.Cleanup(func() {
		activityNowFn = origNow
		ResetAdminActivityForTest()
	})

	if got := ComputeNextStatsInterval(); got != 60*time.Second {
		t.Fatalf("idle stats interval = %v, want 60s", got)
	}
	TouchAdminActivity()
	if got := ComputeNextStatsInterval(); got != 15*time.Second {
		t.Fatalf("active stats interval = %v, want 15s", got)
	}
	now = now.Add(61 * time.Second)
	if got := ComputeNextStatsInterval(); got != 60*time.Second {
		t.Fatalf("expired stats interval = %v, want 60s", got)
	}
}
