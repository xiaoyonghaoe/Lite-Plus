package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nuomiiiii/lite/nodemgt/inboundcfg"
	"github.com/nuomiiiii/lite/nodemgt/models"
	"github.com/stretchr/testify/require"
)

func TestStatsConfigAndTop15Pruning(t *testing.T) {
	st := testDB(t)

	require.False(t, st.StatsEnabled())
	require.NoError(t, st.SetStatsEnabled(true))
	require.True(t, st.StatsEnabled())

	domains := make([]DomainUsageDelta, 0, 25)
	for i := 1; i <= 25; i++ {
		domains = append(domains, DomainUsageDelta{
			InboundID: "inb_1#home",
			Domain:    fmt.Sprintf("example%02d.com", i),
			Bytes:     int64(i * 1000),
		})
	}
	err := st.RecordStatsBatch("node-1", StatsBatch{
		InboundUsage: []InboundUsageDelta{
			{InboundID: "inb_1", UserID: "u1", UpBytes: 100, DownBytes: 200},
			{InboundID: "inb_1#home", UserID: "u1", UpBytes: 500, DownBytes: 1500},
		},
		DomainTop: domains,
	})
	require.NoError(t, err)

	inbStats, userStats, err := st.QueryInboundStats(7, "node-1", "")
	require.NoError(t, err)
	require.Len(t, inbStats, 2)
	require.Equal(t, "inb_1#home", inbStats[0].InboundID)
	require.Equal(t, int64(2000), inbStats[0].Total)
	require.Len(t, userStats, 2)

	topDomains, err := st.QueryTopDomains(7, "node-1", "inb_1#home", 15)
	require.NoError(t, err)
	require.Len(t, topDomains, 15)
	require.Equal(t, "example25.com", topDomains[0].Domain)
	require.Equal(t, "example11.com", topDomains[14].Domain)

	// Verify DB actually pruned rows beyond Top-15
	var count int64
	require.NoError(t, st.db.Model(&models.NodeDomainStatDaily{}).Count(&count).Error)
	require.Equal(t, int64(15), count)
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
