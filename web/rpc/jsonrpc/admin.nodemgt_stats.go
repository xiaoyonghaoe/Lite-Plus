package jsonrpc

import (
	"context"
	"strings"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/nodemgt/inboundcfg"
	"github.com/nuomiiiii/lite/pkg/rpc"
)

func init() {
	reg("nodemgt.stats.config.get", nodemgtStatsConfigGet, "Get nodemgt traffic stats toggle")
	reg("nodemgt.stats.config.set", nodemgtStatsConfigSet, "Enable or disable nodemgt traffic stats")
	reg("nodemgt.stats.summary", nodemgtStatsSummary, "Query inbound and top-15 domain traffic stats")
}

func nodemgtStatsConfigGet(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return map[string]any{
		"enabled": nodemgtST().StatsEnabled(),
		"top_n":   15,
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
		"enabled": p.Enabled,
		"top_n":   15,
	}, nil
}

func nodemgtStatsSummary(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var p struct {
		Days      int    `json:"days"`
		NodeUUID  string `json:"node_uuid"`
		InboundID string `json:"inbound_id"`
		UserID    string `json:"user_id"`
	}
	_ = req.BindParams(&p)
	if p.Days <= 0 {
		p.Days = 7
	}
	st := nodemgtST()
	inbStats, usrInbStats, err := st.QueryInboundStats(p.Days, p.NodeUUID, p.UserID)
	if err != nil {
		return nil, nodemgtErr(err)
	}
	domains, err := st.QueryTopDomains(p.Days, p.NodeUUID, p.InboundID, 15)
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

	inbOut := make([]map[string]any, 0, len(inbStats))
	var totalUp, totalDown int64
	for _, row := range inbStats {
		totalUp += row.UpBytes
		totalDown += row.DownBytes
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
	domOut := make([]map[string]any, 0, len(domains))
	for _, row := range domains {
		domOut = append(domOut, map[string]any{
			"node_uuid":    row.NodeUUID,
			"node_name":    nodeNames[row.NodeUUID],
			"inbound_id":   row.InboundID,
			"inbound_name": resolveInboundName(row.InboundID),
			"domain":       row.Domain,
			"bytes":        row.Bytes,
		})
	}
	return map[string]any{
		"enabled":            st.StatsEnabled(),
		"days":               p.Days,
		"top_n":              15,
		"total_up_bytes":     totalUp,
		"total_down_bytes":   totalDown,
		"total_bytes":        totalUp + totalDown,
		"inbounds":           inbOut,
		"user_inbounds":      usrOut,
		"top_domains":        domOut,
	}, nil
}
