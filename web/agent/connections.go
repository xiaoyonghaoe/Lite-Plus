package agent

import (
	"sort"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

var (
	latestReport  = make(map[string]*v2.Report)
	recentReports = make(map[string][]v2.Report)
	// presenceOnly stores online state for HTTP-reporting agents.
	// value keeps connectionID and a soft expiration to avoid flicker.
	presenceOnly = make(map[string]struct {
		id     int64
		expire time.Time
	})
	mu = sync.RWMutex{}
)

const recentReportRetention = time.Minute

// IsPresent reports whether the node has a live HTTP presence TTL.
func IsPresent(uuid string) bool {
	_, ok := ActiveConnectionID(uuid)
	return ok
}

// ActiveConnectionID returns the active session ID for a reachable node.
func ActiveConnectionID(uuid string) (int64, bool) {
	mu.RLock()
	defer mu.RUnlock()
	if presence, ok := presenceOnly[uuid]; ok && presence.expire.After(time.Now()) {
		return presence.id, true
	}
	return 0, false
}

func DeleteConnectedClients(uuid string) {
	mu.Lock()
	delete(presenceOnly, uuid)
	delete(latestReport, uuid)
	delete(recentReports, uuid)
	mu.Unlock()
	RemoveV2EventQueue(uuid)
}

// KeepAlivePresence sets presence with TTL for HTTP-reporting agents.
func KeepAlivePresence(uuid string, connectionID int64, ttl time.Duration) {
	if metricstore.EntityWritesBlocked(uuid) {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	presenceOnly[uuid] = struct {
		id     int64
		expire time.Time
	}{id: connectionID, expire: time.Now().Add(ttl)}
}

var defaultPresenceTTL = 35 * time.Second

// SetPresence sets or clears presence for HTTP-reporting agents.
// When present=false, it only clears if the connectionID matches the current one.
func SetPresence(uuid string, connectionID int64, present bool) {
	if present && metricstore.EntityWritesBlocked(uuid) {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if present {
		presenceOnly[uuid] = struct {
			id     int64
			expire time.Time
		}{id: connectionID, expire: time.Now().Add(defaultPresenceTTL)}
		return
	}
	if cur, ok := presenceOnly[uuid]; ok && cur.id == connectionID {
		delete(presenceOnly, uuid)
	}
}

// GetAllOnlineUUIDs returns a list of online UUIDs with active presence TTLs.
func GetAllOnlineUUIDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	now := time.Now()
	res := make([]string, 0, len(presenceOnly))
	for k, v := range presenceOnly {
		if v.expire.After(now) {
			res = append(res, k)
		}
	}
	return res
}

func GetLatestReport() map[string]*v2.Report {
	mu.RLock()
	defer mu.RUnlock()
	reportCopy := make(map[string]*v2.Report)
	for k, v := range latestReport {
		if v == nil {
			continue
		}
		item := *v
		reportCopy[k] = &item
	}
	return reportCopy
}

// RecordReport updates the latest runtime state and keeps only the short raw
// window used by recent-status compatibility endpoints.
func RecordReport(report v2.Report) {
	if report.UUID == "" {
		return
	}
	if metricstore.EntityWritesBlocked(report.UUID) {
		return
	}
	if report.UpdatedAt.IsZero() {
		report.UpdatedAt = time.Now().UTC()
	} else {
		report.UpdatedAt = report.UpdatedAt.UTC()
	}
	mu.Lock()
	defer mu.Unlock()
	if latest := latestReport[report.UUID]; latest == nil || !report.UpdatedAt.Before(latest.UpdatedAt) {
		item := report
		latestReport[report.UUID] = &item
	}
	cutoff := time.Now().UTC().Add(-recentReportRetention)
	reports := trimReportsAfterInPlace(recentReports[report.UUID], cutoff)
	if report.UpdatedAt.Before(cutoff) {
		if len(reports) == 0 {
			delete(recentReports, report.UUID)
		} else {
			recentReports[report.UUID] = reports
		}
		return
	}
	insertAt := sort.Search(len(reports), func(i int) bool {
		return reports[i].UpdatedAt.After(report.UpdatedAt)
	})
	if len(reports) < cap(reports) {
		reports = reports[:len(reports)+1]
		copy(reports[insertAt+1:], reports[insertAt:len(reports)-1])
		reports[insertAt] = report
	} else {
		grown := make([]v2.Report, len(reports)+1, len(reports)+16)
		copy(grown[:insertAt], reports[:insertAt])
		grown[insertAt] = report
		copy(grown[insertAt+1:], reports[insertAt:])
		reports = grown
	}
	recentReports[report.UUID] = reports
}

func GetRecentReports(uuid string) []v2.Report {
	mu.Lock()
	defer mu.Unlock()
	reports := trimReportsAfterInPlace(recentReports[uuid], time.Now().UTC().Add(-recentReportRetention))
	if len(reports) == 0 {
		delete(recentReports, uuid)
		return []v2.Report{}
	}
	recentReports[uuid] = reports
	out := make([]v2.Report, len(reports))
	copy(out, reports)
	return out
}

func trimReportsAfterInPlace(reports []v2.Report, cutoff time.Time) []v2.Report {
	first := 0
	for first < len(reports) && reports[first].UpdatedAt.Before(cutoff) {
		first++
	}
	if first == 0 {
		return reports
	}
	remaining := len(reports) - first
	copy(reports[:remaining], reports[first:])
	clear(reports[remaining:])
	return reports[:remaining]
}

func DeleteLatestReport(uuid string) {
	mu.Lock()
	defer mu.Unlock()
	delete(latestReport, uuid)
	delete(recentReports, uuid)
}
