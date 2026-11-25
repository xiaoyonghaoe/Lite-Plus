package subhttp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeCFKVClient struct {
	puts    []cfKVBulkItem
	deletes []string
}

func (f *fakeCFKVClient) BulkPut(items []cfKVBulkItem) error {
	f.puts = append(f.puts, items...)
	return nil
}

func (f *fakeCFKVClient) BulkDelete(keys []string) error {
	f.deletes = append(f.deletes, keys...)
	return nil
}

func TestCFKVSyncPushDiffAndDelete(t *testing.T) {
	st, row := subscriptionFixture(t)
	user, err := st.GetUser(row.UserID)
	require.NoError(t, err)
	user.UsedBytes = 5 * 1024 * 1024
	user.QuotaBytes = 100 * 1024 * 1024
	require.NoError(t, st.UpsertUser(user))

	cfg := CFKVConfig{
		Enabled:            true,
		AccountID:          "acc1",
		NamespaceID:        "ns1",
		APIToken:           "tok1",
		PublicDomain:       "https://sub.example.com",
		SubPrefix:          "sub",
		TrafficThresholdMB: 10,
		MaxStaleMinutes:    60,
		DeleteRemoved:      true,
	}

	fake := &fakeCFKVClient{}
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	// First sync pushes the subscription with 3xui-cf-pages compatible payload.
	res, err := syncCFKVWithClient(st, cfg, fake, false, t0)
	require.NoError(t, err)
	require.Equal(t, 1, res.Total)
	require.Equal(t, 1, res.Pushed)
	require.Equal(t, 0, res.Skipped)
	require.Len(t, fake.puts, 1)
	require.Equal(t, "sub:"+row.Token, fake.puts[0].Key)

	var snap cfKVSnapshot
	require.NoError(t, json.Unmarshal([]byte(fake.puts[0].Value), &snap))
	require.Equal(t, row.Token, snap.SubID)
	require.True(t, snap.Enable)
	require.Equal(t, int64(5*1024*1024), snap.Down)
	require.Equal(t, int64(100*1024*1024), snap.Total)
	require.NotEmpty(t, snap.Raw)
	require.NotEmpty(t, snap.Clash)
	require.NotEmpty(t, snap.JSON)
	require.NotEmpty(t, snap.Nodes)
	require.Equal(t, "https://sub.example.com", snap.PublicDomain)
	require.Equal(t, "sub", snap.SubPrefix)

	// Small traffic bump (below 10MB threshold) skips push.
	user.UsedBytes = 8 * 1024 * 1024
	require.NoError(t, st.UpsertUser(user))
	fake.puts = nil
	res2, err := syncCFKVWithClient(st, cfg, fake, false, t0.Add(5*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 0, res2.Pushed)
	require.Equal(t, 1, res2.Skipped)
	require.Empty(t, fake.puts)

	// Traffic bump >= 10MB pushes update.
	user.UsedBytes = 20 * 1024 * 1024
	require.NoError(t, st.UpsertUser(user))
	res3, err := syncCFKVWithClient(st, cfg, fake, false, t0.Add(10*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, res3.Pushed)
	require.Len(t, fake.puts, 1)

	// Deleting subscription removes its KV key on next sync.
	require.NoError(t, st.DeleteSubscription(row.ID))
	fake.puts = nil
	res4, err := syncCFKVWithClient(st, cfg, fake, false, t0.Add(15*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 0, res4.Total)
	require.Equal(t, 1, res4.Deleted)
	require.Equal(t, []string{"sub:" + row.Token}, fake.deletes)
}
