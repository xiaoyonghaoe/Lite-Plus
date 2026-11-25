package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/nodemgt/inboundcfg"
	"github.com/nuomiiiii/lite/nodemgt/models"
	"github.com/stretchr/testify/require"
)

func TestStatsConfigAndTieredTop20Compaction(t *testing.T) {
	st := testDB(t)
	origNow := statsNow
	defer func() { statsNow = origNow }()

	base := time.Date(2026, 11, 25, 10, 27, 0, 0, time.Local)
	statsNow = func() time.Time { return base }

	require.False(t, st.StatsEnabled())
	require.NoError(t, st.SetStatsEnabled(true))
	require.True(t, st.StatsEnabled())

	// 1. Insert 25 domains at 10:10 (closed 5m bucket inside 30m window)
	domains := make([]DomainUsageDelta, 0, 25)
	for i := 1; i <= 25; i++ {
		domains = append(domains, DomainUsageDelta{
			UserID:    "u1",
			Domain:    fmt.Sprintf("example%02d.com", i),
			UpBytes:   int64(i * 100),
			DownBytes: int64(i * 900),
			Bytes:     int64(i * 1000),
		})
	}
	err := st.RecordStatsBatches([]QueuedStatsBatch{{
		NodeUUID:   "node-1",
		ReceivedAt: base.Add(-17 * time.Minute), // 10:10 bucket
		Batch: StatsBatch{
			InboundUsage: []InboundUsageDelta{
				{InboundID: "inb_1", UserID: "u1", UpBytes: 100, DownBytes: 200},
				{InboundID: "inb_1#home", UserID: "u1", UpBytes: 500, DownBytes: 1500},
			},
			DomainTop: domains,
		},
	}})
	require.NoError(t, err)

	// 2. Insert an older 5m record (2 hours ago) -> should roll up to 30m tier on compact
	err = st.RecordStatsBatches([]QueuedStatsBatch{{
		NodeUUID:   "node-1",
		ReceivedAt: base.Add(-2 * time.Hour),
		Batch: StatsBatch{
			DomainTop: []DomainUsageDelta{
				{UserID: "u1", Domain: "youtube.com", UpBytes: 1000, DownBytes: 9000, Bytes: 10000},
			},
		},
	}})
	require.NoError(t, err)

	// 3. Insert a 2-day-old record -> should roll up 5m -> 30m -> 1d on compact
	err = st.RecordStatsBatches([]QueuedStatsBatch{{
		NodeUUID:   "node-2",
		ReceivedAt: base.AddDate(0, 0, -2),
		Batch: StatsBatch{
			InboundUsage: []InboundUsageDelta{
				{InboundID: "inb_2", UserID: "u1", UpBytes: 300, DownBytes: 700},
			},
			DomainTop: []DomainUsageDelta{
				{UserID: "u1", Domain: "github.com", UpBytes: 200, DownBytes: 800, Bytes: 1000},
			},
		},
	}})
	require.NoError(t, err)

	// 4. Insert an 8-day-old record -> should be purged on compact
	err = st.RecordStatsBatches([]QueuedStatsBatch{{
		NodeUUID:   "node-2",
		ReceivedAt: base.AddDate(0, 0, -8),
		Batch: StatsBatch{
			InboundUsage: []InboundUsageDelta{
				{InboundID: "inb_old", UserID: "u1", UpBytes: 999, DownBytes: 999},
			},
			DomainTop: []DomainUsageDelta{
				{UserID: "u1", Domain: "expired.com", UpBytes: 100, DownBytes: 100, Bytes: 200},
			},
		},
	}})
	require.NoError(t, err)

	// Run compaction at base (10:27)
	require.NoError(t, st.CompactAndPruneStats(base))

	// Closed 5m bucket at 10:10 should be pruned strictly to Top-20
	var count5m int64
	require.NoError(t, st.db.Model(&models.NodeUserDomainStat{}).
		Where("granularity = ?", models.StatsGranularity5m).Count(&count5m).Error)
	require.Equal(t, int64(20), count5m)

	// 2h-old record should now live in 30m tier
	var count30m int64
	require.NoError(t, st.db.Model(&models.NodeUserDomainStat{}).
		Where("granularity = ? AND domain = ?", models.StatsGranularity30m, "youtube.com").Count(&count30m).Error)
	require.Equal(t, int64(1), count30m)

	// 2d-old record should now live in 1d tier (and 8d-old expired.com must be gone)
	var count1d int64
	require.NoError(t, st.db.Model(&models.NodeUserDomainStat{}).
		Where("granularity = ? AND domain = ?", models.StatsGranularity1d, "github.com").Count(&count1d).Error)
	require.Equal(t, int64(1), count1d)
	var countExpired int64
	require.NoError(t, st.db.Model(&models.NodeUserDomainStat{}).
		Where("domain = ?", "expired.com").Count(&countExpired).Error)
	require.Equal(t, int64(0), countExpired)

	// Node stats query should aggregate by node over 7d (excluding 8d-old row)
	nodeStats, inbStats, userStats, _, _, err := st.QueryNodeAndInboundStats(7, "", "u1")
	require.NoError(t, err)
	require.Len(t, nodeStats, 2)
	require.Equal(t, "node-1", nodeStats[0].NodeUUID)
	require.Equal(t, int64(2300), nodeStats[0].Total)
	require.Equal(t, "node-2", nodeStats[1].NodeUUID)
	require.Equal(t, int64(1000), nodeStats[1].Total)
	require.Len(t, inbStats, 3)
	require.Len(t, userStats, 3)

	// Domain analysis: 30m window -> 6 buckets (5m), Top-20
	res30m, err := st.QueryDomainAnalysis(DomainQueryParams{Window: "30m", UserID: "u1", TopN: 20})
	require.NoError(t, err)
	require.Equal(t, models.StatsGranularity5m, res30m.GranularitySec)
	require.Len(t, res30m.Buckets, 6)
	require.Len(t, res30m.Domains, 20)
	require.Equal(t, "example25.com", res30m.Domains[0].Domain)
	require.Equal(t, "example06.com", res30m.Domains[19].Domain)

	// Domain analysis: 24h window -> 48 buckets (30m), includes both youtube.com (30m) and hot 5m data
	res24h, err := st.QueryDomainAnalysis(DomainQueryParams{Window: "24h", UserID: "u1", TopN: 20})
	require.NoError(t, err)
	require.Equal(t, models.StatsGranularity30m, res24h.GranularitySec)
	require.Len(t, res24h.Buckets, 48)
	foundYT := false
	for _, d := range res24h.Domains {
		if d.Domain == "youtube.com" {
			foundYT = true
			require.Equal(t, int64(10000), d.TotalBytes)
		}
	}
	require.True(t, foundYT)

	// Domain analysis: 7d window filtered by node-2 -> only github.com
	res7dNode2, err := st.QueryDomainAnalysis(DomainQueryParams{Window: "7d", UserID: "u1", NodeUUID: "node-2", TopN: 20})
	require.NoError(t, err)
	require.Equal(t, models.StatsGranularity1d, res7dNode2.GranularitySec)
	require.Len(t, res7dNode2.Buckets, 7)
	require.Len(t, res7dNode2.Domains, 1)
	require.Equal(t, "github.com", res7dNode2.Domains[0].Domain)

	// StatsWriter async queue + graceful stop flush
	w := StartStatsWriter(context.Background(), st.db)
	require.NotNil(t, w)
	require.NoError(t, EnqueueStatsBatch(st.db, "node-q", StatsBatch{
		DomainTop: []DomainUsageDelta{{UserID: "u-q", Domain: "openai.com", UpBytes: 50, DownBytes: 450, Bytes: 500}},
	}))
	StopStatsWriter()
	resQ, err := st.QueryDomainAnalysis(DomainQueryParams{Window: "30m", UserID: "u-q", TopN: 20})
	require.NoError(t, err)
	require.Len(t, resQ.Domains, 1)
	require.Equal(t, "openai.com", resQ.Domains[0].Domain)
}

func TestVirtualInboundGrantsAndSnapshot(t *testing.T) {
	st := testDB(t)

	cfgBytes, err := json.Marshal(inboundcfg.Settings{
		Security:    "tls",
		VirtualTags: []string{"home"},
	})
	require.NoError(t, err)
	in := &models.Inbound{
		NodeUUID:     "node-v1",
		Core:         models.CoreSingBox,
		Name:         "HK-AnyTLS",
		Protocol:     "anytls",
		Port:         443,
		Enabled:      true,
		SettingsJSON: string(cfgBytes),
	}
	require.NoError(t, st.UpsertInbound(in))

	u := &models.NodeUser{
		Username: "alice",
		Status:   models.UserActive,
		NodeUUID: "node-v1",
	}
	require.NoError(t, st.SaveUserProfile(u))
	_, _, err = st.IssueCredential(u.ID, "uuid", "", nil)
	require.NoError(t, err)

	virtID := inboundcfg.FormatVirtualInboundID(in.ID, "home")
	require.NoError(t, st.ReplaceGrants(u.ID, []string{in.ID, virtID}))

	// ListUserViews should resolve both primary and virtual inbound names
	views, err := st.ListUserViews("node-v1")
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.ElementsMatch(t, []string{in.ID, virtID}, views[0].InboundIDs)
	require.ElementsMatch(t, []string{"HK-AnyTLS", "HK-AnyTLS [home]"}, views[0].InboundNames)

	// NodeIDsForUser should resolve virtual grant back to node-v1
	nodes, err := st.NodeIDsForUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"node-v1"}, nodes)

	// NodeSnapshot should carry stats_enabled and both inbound IDs
	require.NoError(t, st.SetStatsEnabled(true))
	snap, err := st.NodeSnapshot("node-v1")
	require.NoError(t, err)
	require.Equal(t, true, snap["stats_enabled"])

	// Deleting the inbound should also delete virtual grants
	require.NoError(t, st.DeleteInbound(in.ID))
	grants, err := st.ListGrants(u.ID)
	require.NoError(t, err)
	require.Empty(t, grants)
}
