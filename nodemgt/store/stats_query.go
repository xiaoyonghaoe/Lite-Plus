package store

import (
	"sort"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/nodemgt/models"
)

type NodeStatSummary struct {
	NodeUUID  string `json:"node_uuid"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
	Total     int64  `json:"total_bytes"`
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

type DomainQueryParams struct {
	Window   string
	StartTS  int64
	EndTS    int64
	UserID   string
	NodeUUID string
	Search   string
	TopN     int
}

type DomainBucketTotal struct {
	BucketTS   int64 `json:"bucket_ts"`
	UpBytes    int64 `json:"up_bytes"`
	DownBytes  int64 `json:"down_bytes"`
	TotalBytes int64 `json:"total_bytes"`
}

type DomainMatrixRow struct {
	Domain      string  `json:"domain"`
	UpBytes     int64   `json:"up_bytes"`
	DownBytes   int64   `json:"down_bytes"`
	TotalBytes  int64   `json:"total_bytes"`
	BucketBytes []int64 `json:"bucket_bytes"`
}

type DomainAnalysisResult struct {
	Window         string              `json:"window"`
	GranularitySec int                 `json:"granularity_sec"`
	StartTS        int64               `json:"start_ts"`
	EndTS          int64               `json:"end_ts"`
	Buckets        []DomainBucketTotal `json:"buckets"`
	Domains        []DomainMatrixRow   `json:"domains"`
}

func (s *Store) QueryNodeAndInboundStats(days int, nodeUUID, userID string) (
	nodes []NodeStatSummary,
	inbounds []InboundStatSummary,
	userInbounds []UserInboundStatSummary,
	startDay string,
	endDay string,
	err error,
) {
	if days <= 0 {
		days = defaultStatsRetention
	}
	if days > defaultStatsRetention {
		days = defaultStatsRetention
	}
	today := statsNow().In(time.Local)
	endDay = statsDayLocal(today)
	startDay = statsDayLocal(today.AddDate(0, 0, -(days - 1)))

	q := s.db.Model(&models.NodeInboundStatDaily{}).Where("day >= ? AND day <= ?", startDay, endDay)
	if nodeUUID = strings.TrimSpace(nodeUUID); nodeUUID != "" {
		q = q.Where("node_uuid = ?", nodeUUID)
	}
	if userID = strings.TrimSpace(userID); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	var rows []models.NodeInboundStatDaily
	if err = q.Find(&rows).Error; err != nil {
		return nil, nil, nil, startDay, endDay, err
	}

	type inbKey struct{ node, inb string }
	type usrKey struct{ node, inb, user string }
	nodeMap := map[string]*NodeStatSummary{}
	inbMap := map[inbKey]*InboundStatSummary{}
	usrMap := map[usrKey]*UserInboundStatSummary{}

	for _, r := range rows {
		ns := nodeMap[r.NodeUUID]
		if ns == nil {
			ns = &NodeStatSummary{NodeUUID: r.NodeUUID}
			nodeMap[r.NodeUUID] = ns
		}
		ns.UpBytes += r.UpBytes
		ns.DownBytes += r.DownBytes
		ns.Total += r.UpBytes + r.DownBytes

		ik := inbKey{node: r.NodeUUID, inb: r.InboundID}
		is := inbMap[ik]
		if is == nil {
			is = &InboundStatSummary{NodeUUID: r.NodeUUID, InboundID: r.InboundID}
			inbMap[ik] = is
		}
		is.UpBytes += r.UpBytes
		is.DownBytes += r.DownBytes
		is.Total += r.UpBytes + r.DownBytes

		uk := usrKey{node: r.NodeUUID, inb: r.InboundID, user: r.UserID}
		us := usrMap[uk]
		if us == nil {
			us = &UserInboundStatSummary{NodeUUID: r.NodeUUID, InboundID: r.InboundID, UserID: r.UserID}
			usrMap[uk] = us
		}
		us.UpBytes += r.UpBytes
		us.DownBytes += r.DownBytes
		us.Total += r.UpBytes + r.DownBytes
	}

	nodes = make([]NodeStatSummary, 0, len(nodeMap))
	for _, v := range nodeMap {
		nodes = append(nodes, *v)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Total != nodes[j].Total {
			return nodes[i].Total > nodes[j].Total
		}
		return nodes[i].NodeUUID < nodes[j].NodeUUID
	})

	inbounds = make([]InboundStatSummary, 0, len(inbMap))
	for _, v := range inbMap {
		inbounds = append(inbounds, *v)
	}
	sort.Slice(inbounds, func(i, j int) bool {
		if inbounds[i].Total != inbounds[j].Total {
			return inbounds[i].Total > inbounds[j].Total
		}
		return inbounds[i].InboundID < inbounds[j].InboundID
	})

	userInbounds = make([]UserInboundStatSummary, 0, len(usrMap))
	for _, v := range usrMap {
		userInbounds = append(userInbounds, *v)
	}
	sort.Slice(userInbounds, func(i, j int) bool {
		if userInbounds[i].Total != userInbounds[j].Total {
			return userInbounds[i].Total > userInbounds[j].Total
		}
		return userInbounds[i].UserID < userInbounds[j].UserID
	})
	return nodes, inbounds, userInbounds, startDay, endDay, nil
}

// QueryInboundStats preserves compatibility with existing callers.
func (s *Store) QueryInboundStats(days int, nodeUUID, userID string) ([]InboundStatSummary, []UserInboundStatSummary, error) {
	_, inbs, usrs, _, _, err := s.QueryNodeAndInboundStats(days, nodeUUID, userID)
	return inbs, usrs, err
}

func (s *Store) QueryDomainAnalysis(p DomainQueryParams) (DomainAnalysisResult, error) {
	topN := p.TopN
	if topN <= 0 || topN > defaultStatsTopN {
		topN = defaultStatsTopN
	}
	tsNow := statsNow()
	nowUnix := tsNow.Unix()
	cur5m := bucketFloor(nowUnix, models.StatsGranularity5m)
	cur30m := bucketFloor(nowUnix, models.StatsGranularity30m)
	todayStart := dayBucketStartLocal(tsNow)
	minAllowedTS := dayBucketStartLocal(tsNow.In(time.Local).AddDate(0, 0, -(defaultStatsRetention - 1)))

	window := strings.ToLower(strings.TrimSpace(p.Window))
	if window == "" {
		window = "30m"
	}

	var (
		gran       int
		bucketList []int64
		startTS    int64
		endTS      int64
		queryGrans []int
	)

	switch window {
	case "24h":
		gran = models.StatsGranularity30m
		startTS = cur30m - 47*models.StatsGranularity30m
		endTS = cur30m + models.StatsGranularity30m
		bucketList = make([]int64, 0, 48)
		for b := startTS; b <= cur30m; b += models.StatsGranularity30m {
			bucketList = append(bucketList, b)
		}
		queryGrans = []int{models.StatsGranularity30m, models.StatsGranularity5m}
	case "7d":
		gran = models.StatsGranularity1d
		startTS = minAllowedTS
		endTS = todayStart + models.StatsGranularity1d
		bucketList = make([]int64, 0, defaultStatsRetention)
		baseLocal := tsNow.In(time.Local)
		for d := -(defaultStatsRetention - 1); d <= 0; d++ {
			bucketList = append(bucketList, dayBucketStartLocal(baseLocal.AddDate(0, 0, d)))
		}
		queryGrans = []int{models.StatsGranularity1d, models.StatsGranularity30m, models.StatsGranularity5m}
	case "custom":
		reqStart := p.StartTS
		reqEnd := p.EndTS
		if reqEnd <= 0 || reqEnd > nowUnix {
			reqEnd = nowUnix
		}
		if reqStart <= 0 || reqStart < minAllowedTS {
			reqStart = minAllowedTS
		}
		if reqStart > reqEnd {
			reqStart = reqEnd
		}
		if reqStart >= cur5m-stats5mRetentionSec {
			gran = models.StatsGranularity5m
			bStart := bucketFloor(reqStart, models.StatsGranularity5m)
			bEnd := bucketFloor(reqEnd, models.StatsGranularity5m)
			startTS = bStart
			endTS = bEnd + models.StatsGranularity5m
			for b := bStart; b <= bEnd; b += models.StatsGranularity5m {
				bucketList = append(bucketList, b)
			}
			queryGrans = []int{models.StatsGranularity5m}
		} else if reqStart >= cur30m-stats30mRetentionSec {
			gran = models.StatsGranularity30m
			bStart := bucketFloor(reqStart, models.StatsGranularity30m)
			bEnd := bucketFloor(reqEnd, models.StatsGranularity30m)
			startTS = bStart
			endTS = bEnd + models.StatsGranularity30m
			for b := bStart; b <= bEnd; b += models.StatsGranularity30m {
				bucketList = append(bucketList, b)
			}
			queryGrans = []int{models.StatsGranularity30m, models.StatsGranularity5m}
		} else {
			gran = models.StatsGranularity1d
			sLocal := time.Unix(reqStart, 0).In(time.Local)
			eLocal := time.Unix(reqEnd, 0).In(time.Local)
			cur := time.Date(sLocal.Year(), sLocal.Month(), sLocal.Day(), 0, 0, 0, 0, time.Local)
			endDay := time.Date(eLocal.Year(), eLocal.Month(), eLocal.Day(), 0, 0, 0, 0, time.Local)
			startTS = cur.Unix()
			for !cur.After(endDay) {
				bucketList = append(bucketList, cur.Unix())
				cur = cur.AddDate(0, 0, 1)
			}
			endTS = endDay.AddDate(0, 0, 1).Unix()
			queryGrans = []int{models.StatsGranularity1d, models.StatsGranularity30m, models.StatsGranularity5m}
		}
	default:
		window = "30m"
		gran = models.StatsGranularity5m
		startTS = cur5m - 5*models.StatsGranularity5m
		endTS = cur5m + models.StatsGranularity5m
		bucketList = make([]int64, 0, 6)
		for b := startTS; b <= cur5m; b += models.StatsGranularity5m {
			bucketList = append(bucketList, b)
		}
		queryGrans = []int{models.StatsGranularity5m}
	}

	bucketIdx := make(map[int64]int, len(bucketList))
	for i, b := range bucketList {
		bucketIdx[b] = i
	}

	q := s.db.Model(&models.NodeUserDomainStat{}).
		Where("granularity IN ? AND bucket_ts >= ? AND bucket_ts < ?", queryGrans, startTS, endTS)
	if uid := strings.TrimSpace(p.UserID); uid != "" {
		q = q.Where("user_id = ?", uid)
	}
	if nid := strings.TrimSpace(p.NodeUUID); nid != "" {
		q = q.Where("node_uuid = ?", nid)
	}
	if search := strings.ToLower(strings.TrimSpace(p.Search)); search != "" {
		q = q.Where("domain LIKE ?", "%"+search+"%")
	}

	var rows []models.NodeUserDomainStat
	if err := q.Find(&rows).Error; err != nil {
		return DomainAnalysisResult{}, err
	}

	type domAgg struct {
		domain    string
		up        int64
		down      int64
		total     int64
		bucketUp  []int64
		bucketDn  []int64
		bucketTot []int64
	}
	byDomain := map[string]*domAgg{}
	for _, r := range rows {
		var targetBucket int64
		switch gran {
		case models.StatsGranularity5m:
			targetBucket = bucketFloor(r.BucketTS, models.StatsGranularity5m)
		case models.StatsGranularity30m:
			targetBucket = bucketFloor(r.BucketTS, models.StatsGranularity30m)
		default:
			targetBucket = dayBucketStartLocal(time.Unix(r.BucketTS, 0))
		}
		idx, ok := bucketIdx[targetBucket]
		if !ok {
			continue
		}
		d := byDomain[r.Domain]
		if d == nil {
			d = &domAgg{
				domain:    r.Domain,
				bucketUp:  make([]int64, len(bucketList)),
				bucketDn:  make([]int64, len(bucketList)),
				bucketTot: make([]int64, len(bucketList)),
			}
			byDomain[r.Domain] = d
		}
		d.up += r.UpBytes
		d.down += r.DownBytes
		d.total += r.Bytes
		d.bucketUp[idx] += r.UpBytes
		d.bucketDn[idx] += r.DownBytes
		d.bucketTot[idx] += r.Bytes
	}

	sorted := make([]*domAgg, 0, len(byDomain))
	for _, d := range byDomain {
		sorted = append(sorted, d)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].total != sorted[j].total {
			return sorted[i].total > sorted[j].total
		}
		return sorted[i].domain < sorted[j].domain
	})
	if len(sorted) > topN {
		sorted = sorted[:topN]
	}

	buckets := make([]DomainBucketTotal, len(bucketList))
	for i, ts := range bucketList {
		buckets[i] = DomainBucketTotal{BucketTS: ts}
	}
	domainsOut := make([]DomainMatrixRow, 0, len(sorted))
	for _, d := range sorted {
		domainsOut = append(domainsOut, DomainMatrixRow{
			Domain:      d.domain,
			UpBytes:     d.up,
			DownBytes:   d.down,
			TotalBytes:  d.total,
			BucketBytes: d.bucketTot,
		})
		for i := range bucketList {
			buckets[i].UpBytes += d.bucketUp[i]
			buckets[i].DownBytes += d.bucketDn[i]
			buckets[i].TotalBytes += d.bucketTot[i]
		}
	}

	return DomainAnalysisResult{
		Window:         window,
		GranularitySec: gran,
		StartTS:        startTS,
		EndTS:          endTS,
		Buckets:        buckets,
		Domains:        domainsOut,
	}, nil
}
