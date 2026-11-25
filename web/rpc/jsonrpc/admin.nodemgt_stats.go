package jsonrpc

import (
	"context"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/nodemgt/inboundcfg"
	"github.com/nuomiiiii/lite/nodemgt/store"
	"github.com/nuomiiiii/lite/pkg/rpc"
)

func init() {
	reg("nodemgt.stats.config.get", nodemgtStatsConfigGet, "Get nodemgt traffic stats toggle")
	reg("nodemgt.stats.config.set", nodemgtStatsConfigSet, "Enable or disable nodemgt traffic stats")
	reg("nodemgt.stats.summary", nodemgtStatsSummary, "Query user node usage and multi-tier top-20 domain traffic stats")
}

func nodemgtStatsConfigGet(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return map[string]any{
		"enabled":        nodemgtST().StatsEnabled(),
		"top_n":          20,
		"retention_days": 7,
	}, nil
}

func nodemgtStatsConfigSet(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var p struct {
		Enabled bool `json:"enabled"`
	}
	if err := req.BindParams(&p); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	st := nodemgtST()
	if err := st.SetStatsEnabled(p.Enabled); err != nil {
		return nil, nodemgtErr(err)
	}
	stateText := "disabled"
	if p.Enabled {
		stateText = "enabled"
	}
	_ = nodemgtAudit(ctx, st, "nodemgt.stats.config.set", "traffic_stats", stateText)

	// Push updated snapshot to all nodes that have inbounds so the tracker toggles immediately.
	if inbounds, err := st.ListInbounds(""); err == nil {
		seen := map[string]struct{}{}
		nodes := make([]string, 0, len(inbounds))
		for _, in := range inbounds {
			if id := strings.TrimSpace(in.NodeUUID); id != "" {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					nodes = append(nodes, id)
				}
			}
		}
		nodemgtPushNodes(st, nodes...)
	}
	return map[string]any{
		"enabled":        p.Enabled,
		"top_n":          20,
		"retention_days": 7,
	}, nil
}

func nodemgtStatsSummary(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var p struct {
		Days           int    `json:"days"`
		NodeUUID       string `json:"node_uuid"`
		DomainNodeUUID string `json:"domain_node_uuid"`
		UserID         string `json:"user_id"`
		Window         string `json:"window"`
		StartTS        int64  `json:"start_ts"`
		EndTS          int64  `json:"end_ts"`
		Search         string `json:"search"`
	}
	_ = req.BindParams(&p)
	if p.Days <= 0 {
		p.Days = 7
	}
	if p.Days > 7 {
		p.Days = 7
	}
	st := nodemgtST()
	nodeStats, inbStats, usrInbStats, startDay, endDay, err := st.QueryNodeAndInboundStats(p.Days, p.NodeUUID, p.UserID)
	if err != nil {
		return nil, nodemgtErr(err)
	}

	domNodeUUID := strings.TrimSpace(p.DomainNodeUUID)
	if domNodeUUID == "" {
		domNodeUUID = strings.TrimSpace(p.NodeUUID)
	}
	domRes, err := st.QueryDomainAnalysis(store.DomainQueryParams{
		Window:   p.Window,
		StartTS:  p.StartTS,
		EndTS:    p.EndTS,
		UserID:   p.UserID,
		NodeUUID: domNodeUUID,
		Search:   p.Search,
		TopN:     20,
	})
	if err != nil {
		return nil, nodemgtErr(err)
	}

	// Build display name maps for nodes, inbounds (including virtual), and users.
	nodeNames := map[string]string{}
	if clis, err := clients.GetAllClientBasicInfo(); err == nil {
		for _, c := range clis {
			nodeNames[c.UUID] = c.Name
		}
	}
	inboundNames := map[string]string{}
	if inbs, err := st.ListInbounds(""); err == nil {
		for _, in := range inbs {
			baseName := strings.TrimSpace(in.Name)
			if baseName == "" {
				baseName = in.ID
			}
			inboundNames[in.ID] = baseName
			for _, tag := range inboundcfg.NormalizeVirtualTags(inboundcfg.Parse(in.SettingsJSON).VirtualTags) {
				inboundNames[inboundcfg.FormatVirtualInboundID(in.ID, tag)] = baseName + " [" + tag + "]"
			}
		}
	}
	userNames := map[string]string{}
	if users, err := st.ListUsers(""); err == nil {
		for _, u := range users {
			label := strings.TrimSpace(u.DisplayName)
			if label == "" {
				label = strings.TrimSpace(u.Username)
			}
			if label == "" {
				label = u.ID
			}
			userNames[u.ID] = label
		}
	}
	resolveInboundName := func(id string) string {
		if n, ok := inboundNames[id]; ok {
			return n
		}
		base, tag := inboundcfg.SplitVirtualInboundID(id)
		if tag != "" {
			if n, ok := inboundNames[base]; ok {
				return n + " [" + tag + "]"
			}
		}
		return id
	}

	var totalUp, totalDown int64
	for _, row := range nodeStats {
		totalUp += row.UpBytes
		totalDown += row.DownBytes
	}
	totalBytes := totalUp + totalDown

	nodeOut := make([]map[string]any, 0, len(nodeStats))
	for _, row := range nodeStats {
		share := 0.0
		if totalBytes > 0 {
			share = float64(row.Total) * 100 / float64(totalBytes)
		}
		nodeOut = append(nodeOut, map[string]any{
			"node_uuid":   row.NodeUUID,
			"node_name":   nodeNames[row.NodeUUID],
			"up_bytes":    row.UpBytes,
			"down_bytes":  row.DownBytes,
			"total_bytes": row.Total,
			"share_pct":   share,
		})
	}

	inbOut := make([]map[string]any, 0, len(inbStats))
	for _, row := range inbStats {
		inbOut = append(inbOut, map[string]any{
			"node_uuid":    row.NodeUUID,
			"node_name":    nodeNames[row.NodeUUID],
			"inbound_id":   row.InboundID,
			"inbound_name": resolveInboundName(row.InboundID),
			"up_bytes":     row.UpBytes,
			"down_bytes":   row.DownBytes,
			"total_bytes":  row.Total,
		})
	}

	usrOut := make([]map[string]any, 0, len(usrInbStats))
	for _, row := range usrInbStats {
		usrOut = append(usrOut, map[string]any{
			"node_uuid":    row.NodeUUID,
			"node_name":    nodeNames[row.NodeUUID],
			"inbound_id":   row.InboundID,
			"inbound_name": resolveInboundName(row.InboundID),
			"user_id":      row.UserID,
			"user_name":    userNames[row.UserID],
			"up_bytes":     row.UpBytes,
			"down_bytes":   row.DownBytes,
			"total_bytes":  row.Total,
		})
	}

	_, tzOffsetSec := time.Now().In(time.Local).Zone()
	return map[string]any{
		"enabled":                st.StatsEnabled(),
		"days":                   p.Days,
		"start_day":              startDay,
		"end_day":                endDay,
		"top_n":                  20,
		"retention_days":         7,
		"updated_at":             time.Now().Unix(),
		"tz_offset_sec":          tzOffsetSec,
		"total_up_bytes":         totalUp,
		"total_down_bytes":       totalDown,
		"total_bytes":            totalBytes,
		"active_node_count":      len(nodeOut),
		"nodes":                  nodeOut,
		"inbounds":               inbOut,
		"user_inbounds":          usrOut,
		"domain_window":          domRes.Window,
		"domain_granularity_sec": domRes.GranularitySec,
		"domain_start_ts":        domRes.StartTS,
		"domain_end_ts":          domRes.EndTS,
		"domain_buckets":         domRes.Buckets,
		"top_domains":            domRes.Domains,
	}, nil
}
