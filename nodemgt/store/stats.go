package store

import (
	"strings"
	"time"

	"github.com/nuomiiiii/lite/nodemgt/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SettingStatsEnabledKey = "traffic_stats_enabled"
	defaultStatsTopN       = 20
	defaultStatsRetention  = 7

	stats5mRetentionSec  = 30 * 60
	stats30mRetentionSec = 24 * 3600
)

var statsNow = func() time.Time { return now() }

type InboundUsageDelta struct {
	InboundID string `json:"inbound_id"`
	UserID    string `json:"user_id"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
}

type DomainUsageDelta struct {
	UserID    string `json:"user_id"`
	InboundID string `json:"inbound_id,omitempty"`
	Domain    string `json:"domain"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
	Bytes     int64  `json:"bytes"`
}

type StatsBatch struct {
	InboundUsage []InboundUsageDelta `json:"inbound_usage"`
	DomainTop    []DomainUsageDelta  `json:"domain_top"`
}

type QueuedStatsBatch struct {
	NodeUUID   string
	Batch      StatsBatch
	ReceivedAt time.Time
}

func (s *Store) StatsEnabled() bool {
	v, err := s.GetSetting(SettingStatsEnabledKey)
	if err != nil {
		return false
	}
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes"
}

func (s *Store) SetStatsEnabled(enabled bool) error {
	val := "0"
	if enabled {
		val = "1"
	}
	return s.SetSetting(SettingStatsEnabledKey, val)
}

func statsDayLocal(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02")
}

func dayBucketStartLocal(t time.Time) int64 {
	lt := t.In(time.Local)
	return time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.Local).Unix()
}

func bucketFloor(ts int64, gran int64) int64 {
	if gran <= 0 {
		return ts
	}
	if ts >= 0 {
		return (ts / gran) * gran
	}
	return ((ts - gran + 1) / gran) * gran
}

func (s *Store) RecordStatsBatch(nodeUUID string, batch StatsBatch) error {
	return s.RecordStatsBatches([]QueuedStatsBatch{{
		NodeUUID:   nodeUUID,
		Batch:      batch,
		ReceivedAt: statsNow(),
	}})
}

func (s *Store) RecordStatsBatches(items []QueuedStatsBatch) error {
	if len(items) == 0 {
		return nil
	}
	type inbKey struct {
		day, nodeUUID, inboundID, userID string
	}
	type domKey struct {
		userID, nodeUUID, inboundID string
		bucketTS                    int64
		domain                      string
	}
	type counters struct {
		up, down, total int64
		updatedAt       time.Time
	}

	inbAgg := map[inbKey]*counters{}
	domAgg := map[domKey]*counters{}

	for _, q := range items {
		nodeUUID := strings.TrimSpace(q.NodeUUID)
		if nodeUUID == "" {
			continue
		}
		ts := q.ReceivedAt
		if ts.IsZero() {
			ts = statsNow()
		}
		day := statsDayLocal(ts)
		b5m := bucketFloor(ts.Unix(), models.StatsGranularity5m)

		for _, item := range q.Batch.InboundUsage {
			inbID := strings.TrimSpace(item.InboundID)
			uid := strings.TrimSpace(item.UserID)
			up := max(item.UpBytes, 0)
			down := max(item.DownBytes, 0)
			if inbID == "" || (up == 0 && down == 0) {
				continue
			}
			k := inbKey{day: day, nodeUUID: nodeUUID, inboundID: inbID, userID: uid}
			c := inbAgg[k]
			if c == nil {
				c = &counters{}
				inbAgg[k] = c
			}
			c.up += up
			c.down += down
			if ts.After(c.updatedAt) {
				c.updatedAt = ts
			}
		}

		for _, item := range q.Batch.DomainTop {
			uid := strings.TrimSpace(item.UserID)
			inbID := strings.TrimSpace(item.InboundID)
			domain := strings.ToLower(strings.TrimSpace(item.Domain))
			if uid == "" || domain == "" {
				continue
			}
			if len(domain) > 191 {
				domain = domain[:191]
			}
			up := max(item.UpBytes, 0)
			down := max(item.DownBytes, 0)
			total := item.Bytes
			if total <= 0 {
				total = up + down
			} else if up == 0 && down == 0 {
				down = total
			}
			if total <= 0 {
				continue
			}
			k := domKey{userID: uid, nodeUUID: nodeUUID, inboundID: inbID, bucketTS: b5m, domain: domain}
			c := domAgg[k]
			if c == nil {
				c = &counters{}
				domAgg[k] = c
			}
			c.up += up
			c.down += down
			c.total += total
			if ts.After(c.updatedAt) {
				c.updatedAt = ts
			}
		}
	}

	if len(inbAgg) == 0 && len(domAgg) == 0 {
		return nil
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		for k, c := range inbAgg {
			row := models.NodeInboundStatDaily{
				Day:       k.day,
				NodeUUID:  k.nodeUUID,
				InboundID: k.inboundID,
				UserID:    k.userID,
				UpBytes:   c.up,
				DownBytes: c.down,
				UpdatedAt: c.updatedAt.UTC(),
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "day"}, {Name: "node_uuid"}, {Name: "inbound_id"}, {Name: "user_id"}},
				DoUpdates: clause.Assignments(map[string]any{
					"up_bytes":   gorm.Expr("up_bytes + ?", c.up),
					"down_bytes": gorm.Expr("down_bytes + ?", c.down),
					"updated_at": c.updatedAt.UTC(),
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}

		for k, c := range domAgg {
			row := models.NodeUserDomainStat{
				UserID:      k.userID,
				NodeUUID:    k.nodeUUID,
				InboundID:   k.inboundID,
				Granularity: models.StatsGranularity5m,
				BucketTS:    k.bucketTS,
				Domain:      k.domain,
				UpBytes:     c.up,
				DownBytes:   c.down,
				Bytes:       c.total,
				UpdatedAt:   c.updatedAt.UTC(),
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "user_id"},
					{Name: "node_uuid"},
					{Name: "inbound_id"},
					{Name: "granularity"},
					{Name: "bucket_ts"},
					{Name: "domain"},
				},
				DoUpdates: clause.Assignments(map[string]any{
					"up_bytes":   gorm.Expr("up_bytes + ?", c.up),
					"down_bytes": gorm.Expr("down_bytes + ?", c.down),
					"bytes":      gorm.Expr("bytes + ?", c.total),
					"updated_at": c.updatedAt.UTC(),
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// CompactAndPruneStats enforces multi-tier retention and Top-20 caps:
//   - 5m buckets older than 30m roll up into 30m buckets (Top-20 per user+node+bucket).
//   - Closed 5m buckets are pruned to Top-20 per user+node+bucket.
//   - 30m buckets older than 24h roll up into local 1d buckets (Top-20 per user+node+bucket).
//   - 1d domain buckets and inbound daily rows older than 7 days are deleted.
func (s *Store) CompactAndPruneStats(at time.Time) error {
	if at.IsZero() {
		at = statsNow()
	}
	cur5m := bucketFloor(at.Unix(), models.StatsGranularity5m)
	cutoff5m := cur5m - stats5mRetentionSec
	cur30m := bucketFloor(at.Unix(), models.StatsGranularity30m)
	cutoff30m := cur30m - stats30mRetentionSec
	cutoff1dTS := dayBucketStartLocal(at.In(time.Local).AddDate(0, 0, -(defaultStatsRetention - 1)))
	cutoffDay := statsDayLocal(at.In(time.Local).AddDate(0, 0, -(defaultStatsRetention - 1)))

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := rollupDomainTier(tx, models.StatsGranularity5m, cutoff5m, models.StatsGranularity30m, func(ts int64) int64 {
			return bucketFloor(ts, models.StatsGranularity30m)
		}, at.UTC()); err != nil {
			return err
		}
		if err := pruneDomainTierTopN(tx, models.StatsGranularity5m, cur5m, defaultStatsTopN); err != nil {
			return err
		}
		if err := rollupDomainTier(tx, models.StatsGranularity30m, cutoff30m, models.StatsGranularity1d, func(ts int64) int64 {
			return dayBucketStartLocal(time.Unix(ts, 0))
		}, at.UTC()); err != nil {
			return err
		}
		if err := tx.Where("granularity = ? AND bucket_ts < ?", models.StatsGranularity1d, cutoff1dTS).
			Delete(&models.NodeUserDomainStat{}).Error; err != nil {
			return err
		}
		if err := tx.Where("day < ?", cutoffDay).Delete(&models.NodeInboundStatDaily{}).Error; err != nil {
			return err
		}
		_ = tx.Where("day < ?", cutoffDay).Delete(&models.NodeDomainStatDaily{}).Error
		return nil
	})
}

func rollupDomainTier(
	tx *gorm.DB,
	srcGran int,
	cutoffTS int64,
	dstGran int,
	mapBucket func(int64) int64,
	updatedAt time.Time,
) error {
	var oldRows []models.NodeUserDomainStat
	if err := tx.Where("granularity = ? AND bucket_ts < ?", srcGran, cutoffTS).Find(&oldRows).Error; err != nil {
		return err
	}
	if len(oldRows) == 0 {
		return nil
	}

	type dstKey struct {
		userID, nodeUUID, inboundID string
		bucketTS                    int64
		domain                      string
	}
	type scopeKey struct {
		userID, nodeUUID, inboundID string
		bucketTS                    int64
	}
	type aggVal struct {
		up, down, total int64
	}

	merged := map[dstKey]*aggVal{}
	touched := map[scopeKey]struct{}{}
	for _, r := range oldRows {
		dstTS := mapBucket(r.BucketTS)
		k := dstKey{userID: r.UserID, nodeUUID: r.NodeUUID, inboundID: r.InboundID, bucketTS: dstTS, domain: r.Domain}
		v := merged[k]
		if v == nil {
			v = &aggVal{}
			merged[k] = v
		}
		v.up += r.UpBytes
		v.down += r.DownBytes
		v.total += r.Bytes
		touched[scopeKey{userID: r.UserID, nodeUUID: r.NodeUUID, inboundID: r.InboundID, bucketTS: dstTS}] = struct{}{}
	}

	for k, v := range merged {
		row := models.NodeUserDomainStat{
			UserID:      k.userID,
			NodeUUID:    k.nodeUUID,
			InboundID:   k.inboundID,
			Granularity: dstGran,
			BucketTS:    k.bucketTS,
			Domain:      k.domain,
			UpBytes:     v.up,
			DownBytes:   v.down,
			Bytes:       v.total,
			UpdatedAt:   updatedAt,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "user_id"},
				{Name: "node_uuid"},
				{Name: "inbound_id"},
				{Name: "granularity"},
				{Name: "bucket_ts"},
				{Name: "domain"},
			},
			DoUpdates: clause.Assignments(map[string]any{
				"up_bytes":   gorm.Expr("up_bytes + ?", v.up),
				"down_bytes": gorm.Expr("down_bytes + ?", v.down),
				"bytes":      gorm.Expr("bytes + ?", v.total),
				"updated_at": updatedAt,
			}),
		}).Create(&row).Error; err != nil {
			return err
		}
	}

	if err := tx.Where("granularity = ? AND bucket_ts < ?", srcGran, cutoffTS).
		Delete(&models.NodeUserDomainStat{}).Error; err != nil {
		return err
	}

	for sc := range touched {
		if err := pruneDomainScopeTopN(tx, sc.userID, sc.nodeUUID, sc.inboundID, dstGran, sc.bucketTS, defaultStatsTopN); err != nil {
			return err
		}
	}
	return nil
}

func pruneDomainTierTopN(tx *gorm.DB, gran int, beforeTS int64, topN int) error {
	type scopeRow struct {
		UserID    string
		NodeUUID  string
		InboundID string
		BucketTS  int64
	}
	var scopes []scopeRow
	if err := tx.Model(&models.NodeUserDomainStat{}).
		Select("user_id, node_uuid, inbound_id, bucket_ts").
		Where("granularity = ? AND bucket_ts < ?", gran, beforeTS).
		Group("user_id, node_uuid, inbound_id, bucket_ts").
		Having("COUNT(*) > ?", topN).
		Find(&scopes).Error; err != nil {
		return err
	}
	for _, sc := range scopes {
		if err := pruneDomainScopeTopN(tx, sc.UserID, sc.NodeUUID, sc.InboundID, gran, sc.BucketTS, topN); err != nil {
			return err
		}
	}
	return nil
}

func pruneDomainScopeTopN(tx *gorm.DB, userID, nodeUUID, inboundID string, gran int, bucketTS int64, topN int) error {
	var rows []models.NodeUserDomainStat
	if err := tx.Where("user_id = ? AND node_uuid = ? AND inbound_id = ? AND granularity = ? AND bucket_ts = ?",
		userID, nodeUUID, inboundID, gran, bucketTS).
		Order("bytes DESC, domain ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) <= topN {
		return nil
	}
	drop := make([]string, 0, len(rows)-topN)
	for _, r := range rows[topN:] {
		drop = append(drop, r.Domain)
	}
	return tx.Where("user_id = ? AND node_uuid = ? AND inbound_id = ? AND granularity = ? AND bucket_ts = ? AND domain IN ?",
		userID, nodeUUID, inboundID, gran, bucketTS, drop).
		Delete(&models.NodeUserDomainStat{}).Error
}
