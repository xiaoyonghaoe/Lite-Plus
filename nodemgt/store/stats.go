package store

import (
	"sort"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/nodemgt/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SettingStatsEnabledKey = "traffic_stats_enabled"
	defaultStatsTopN       = 15
	defaultStatsRetention  = 30
)

type InboundUsageDelta struct {
	InboundID string `json:"inbound_id"`
	UserID    string `json:"user_id"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
}

type DomainUsageDelta struct {
	InboundID string `json:"inbound_id"`
	Domain    string `json:"domain"`
	Bytes     int64  `json:"bytes"`
}

type StatsBatch struct {
	InboundUsage []InboundUsageDelta `json:"inbound_usage"`
	DomainTop    []DomainUsageDelta  `json:"domain_top"`
}

type InboundStatSummary struct {
	NodeUUID  string `json:"node_uuid"`
	InboundID string `json:"inbound_id"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
	Total     int64  `json:"total_bytes"`
}

type UserInboundStatSummary struct {
	NodeUUID  string `json:"node_uuid"`
	InboundID string `json:"inbound_id"`
	UserID    string `json:"user_id"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
	Total     int64  `json:"total_bytes"`
}

type DomainStatSummary struct {
	NodeUUID  string `json:"node_uuid"`
	InboundID string `json:"inbound_id"`
	Domain    string `json:"domain"`
	Bytes     int64  `json:"bytes"`
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

func statsDayUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func (s *Store) RecordStatsBatch(nodeUUID string, batch StatsBatch) error {
	nodeUUID = strings.TrimSpace(nodeUUID)
	if nodeUUID == "" {
		return nil
	}
	if len(batch.InboundUsage) == 0 && len(batch.DomainTop) == 0 {
		return nil
	}
	ts := now().UTC()
	day := statsDayUTC(ts)

	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, item := range batch.InboundUsage {
			inbID := strings.TrimSpace(item.InboundID)
			uid := strings.TrimSpace(item.UserID)
			if inbID == "" || (item.UpBytes <= 0 && item.DownBytes <= 0) {
				continue
			}
			up := item.UpBytes
			if up < 0 {
				up = 0
			}
			down := item.DownBytes
			if down < 0 {
				down = 0
			}
			row := models.NodeInboundStatDaily{
				Day:       day,
				NodeUUID:  nodeUUID,
				InboundID: inbID,
				UserID:    uid,
				UpBytes:   up,
				DownBytes: down,
				UpdatedAt: ts,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "day"}, {Name: "node_uuid"}, {Name: "inbound_id"}, {Name: "user_id"}},
				DoUpdates: clause.Assignments(map[string]any{
					"up_bytes":   gorm.Expr("up_bytes + ?", up),
					"down_bytes": gorm.Expr("down_bytes + ?", down),
					"updated_at": ts,
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}

		touchedInbounds := map[string]struct{}{}
		for _, item := range batch.DomainTop {
			inbID := strings.TrimSpace(item.InboundID)
			domain := strings.ToLower(strings.TrimSpace(item.Domain))
			if inbID == "" || domain == "" || item.Bytes <= 0 {
				continue
			}
			if len(domain) > 191 {
				domain = domain[:191]
			}
			row := models.NodeDomainStatDaily{
				Day:       day,
				NodeUUID:  nodeUUID,
				InboundID: inbID,
				Domain:    domain,
				Bytes:     item.Bytes,
				UpdatedAt: ts,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "day"}, {Name: "node_uuid"}, {Name: "inbound_id"}, {Name: "domain"}},
				DoUpdates: clause.Assignments(map[string]any{
					"bytes":      gorm.Expr("bytes + ?", item.Bytes),
					"updated_at": ts,
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
			touchedInbounds[inbID] = struct{}{}
		}

		// Keep at most Top-15 domains per (day, node_uuid, inbound_id) to bound SQLite size.
		for inbID := range touchedInbounds {
			var rows []models.NodeDomainStatDaily
			if err := tx.Where("day = ? AND node_uuid = ? AND inbound_id = ?", day, nodeUUID, inbID).
				Order("bytes DESC, domain ASC").Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) > defaultStatsTopN {
				dropDomains := make([]string, 0, len(rows)-defaultStatsTopN)
				for _, r := range rows[defaultStatsTopN:] {
					dropDomains = append(dropDomains, r.Domain)
				}
				if err := tx.Where("day = ? AND node_uuid = ? AND inbound_id = ? AND domain IN ?", day, nodeUUID, inbID, dropDomains).
					Delete(&models.NodeDomainStatDaily{}).Error; err != nil {
					return err
				}
			}
		}

		// Opportunistic retention prune (keep last 30 days).
		cutoff := statsDayUTC(ts.AddDate(0, 0, -defaultStatsRetention))
		_ = tx.Where("day < ?", cutoff).Delete(&models.NodeInboundStatDaily{}).Error
		_ = tx.Where("day < ?", cutoff).Delete(&models.NodeDomainStatDaily{}).Error
		return nil
	})
}

func (s *Store) QueryInboundStats(days int, nodeUUID, userID string) ([]InboundStatSummary, []UserInboundStatSummary, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	since := statsDayUTC(now().UTC().AddDate(0, 0, -(days - 1)))
	q := s.db.Model(&models.NodeInboundStatDaily{}).Where("day >= ?", since)
	if nodeUUID = strings.TrimSpace(nodeUUID); nodeUUID != "" {
		q = q.Where("node_uuid = ?", nodeUUID)
	}
	if userID = strings.TrimSpace(userID); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	var rows []models.NodeInboundStatDaily
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}

	type inbKey struct{ node, inb string }
	type usrKey struct{ node, inb, user string }
	inbMap := map[inbKey]*InboundStatSummary{}
	usrMap := map[usrKey]*UserInboundStatSummary{}
	for _, r := range rows {
		ik := inbKey{node: r.NodeUUID, inb: r.InboundID}
		is, ok := inbMap[ik]
		if !ok {
			is = &InboundStatSummary{NodeUUID: r.NodeUUID, InboundID: r.InboundID}
			inbMap[ik] = is
		}
		is.UpBytes += r.UpBytes
		is.DownBytes += r.DownBytes
		is.Total += r.UpBytes + r.DownBytes

		uk := usrKey{node: r.NodeUUID, inb: r.InboundID, user: r.UserID}
		us, ok := usrMap[uk]
		if !ok {
			us = &UserInboundStatSummary{NodeUUID: r.NodeUUID, InboundID: r.InboundID, UserID: r.UserID}
			usrMap[uk] = us
		}
		us.UpBytes += r.UpBytes
		us.DownBytes += r.DownBytes
		us.Total += r.UpBytes + r.DownBytes
	}

	inbList := make([]InboundStatSummary, 0, len(inbMap))
	for _, v := range inbMap {
		inbList = append(inbList, *v)
	}
	sort.Slice(inbList, func(i, j int) bool {
		if inbList[i].Total != inbList[j].Total {
			return inbList[i].Total > inbList[j].Total
		}
		return inbList[i].InboundID < inbList[j].InboundID
	})

	usrList := make([]UserInboundStatSummary, 0, len(usrMap))
	for _, v := range usrMap {
		usrList = append(usrList, *v)
	}
	sort.Slice(usrList, func(i, j int) bool {
		if usrList[i].Total != usrList[j].Total {
			return usrList[i].Total > usrList[j].Total
		}
		return usrList[i].UserID < usrList[j].UserID
	})
	return inbList, usrList, nil
}

func (s *Store) QueryTopDomains(days int, nodeUUID, inboundID string, limit int) ([]DomainStatSummary, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	if limit <= 0 || limit > defaultStatsTopN {
		limit = defaultStatsTopN
	}
	since := statsDayUTC(now().UTC().AddDate(0, 0, -(days - 1)))
	q := s.db.Model(&models.NodeDomainStatDaily{}).Where("day >= ?", since)
	if nodeUUID = strings.TrimSpace(nodeUUID); nodeUUID != "" {
		q = q.Where("node_uuid = ?", nodeUUID)
	}
	if inboundID = strings.TrimSpace(inboundID); inboundID != "" {
		q = q.Where("inbound_id = ?", inboundID)
	}
	var rows []models.NodeDomainStatDaily
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	type domKey struct{ node, inb, domain string }
	agg := map[domKey]*DomainStatSummary{}
	for _, r := range rows {
		k := domKey{node: r.NodeUUID, inb: r.InboundID, domain: r.Domain}
		item, ok := agg[k]
		if !ok {
			item = &DomainStatSummary{NodeUUID: r.NodeUUID, InboundID: r.InboundID, Domain: r.Domain}
			agg[k] = item
		}
		item.Bytes += r.Bytes
	}
	out := make([]DomainStatSummary, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Domain < out[j].Domain
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
