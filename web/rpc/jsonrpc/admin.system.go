package jsonrpc

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"

	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/utils/cloudflared"
	"github.com/nuomiiiii/lite/utils/geoip"
	"github.com/nuomiiiii/lite/utils/messageSender"
	"gorm.io/gorm"
)

// admin.system.go
// 系统/运维类 RPC2 方法（admin 命名空间）：日志、Cloudflare Tunnel、远程执行、测试。

const cloudflaredStopConfirmText = "STOP CLOUDFLARED"

func init() {
	RegisterWithGroupAndMeta("getLogs", rpc.RoleAdmin, adminGetLogs, &rpc.MethodMeta{
		Name:    "admin:getLogs",
		Summary: "Get audit logs (paged, optionally filtered by message type)",
		Params: []rpc.ParamMeta{
			{Name: "limit", Type: "string", Description: "Page size (default 100)"},
			{Name: "page", Type: "string", Description: "One-based page number (default 1)"},
			{Name: "msg_type", Type: "string", Description: "Optional comma-separated message type filter"},
			{Name: "day", Type: "string", Description: "Optional comma-separated UTC date filter (YYYY-MM-DD)"},
			{Name: "q", Type: "string", Description: "Optional fuzzy search against IP and message"},
		},
		Returns: "{ logs: Log[], total: number }",
	})
	reg("getCloudflaredStatus", adminCloudflaredStatus, "Get cloudflared tunnel status")
	reg("startCloudflared", adminStartCloudflared, "Start cloudflared tunnel")
	reg("stopCloudflared", adminStopCloudflared, "Stop cloudflared tunnel")
	reg("removeCloudflaredToken", adminRemoveCloudflaredToken, "Remove cloudflared tunnel token")

	reg("testSendMessage", adminTestSendMessage, "Send a test notification")
	reg("testGeoip", adminTestGeoip, "Test GeoIP lookup")
}

func adminGetLogs(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Limit   string `json:"limit"`
		Page    string `json:"page"`
		MsgType string `json:"msg_type"`
		Day     string `json:"day"`
		Q       string `json:"q"`
	}
	req.BindParams(&params)
	if params.Limit == "" {
		params.Limit = "100"
	}
	if params.Page == "" {
		params.Page = "1"
	}
	limitInt, err := strconv.Atoi(params.Limit)
	if err != nil || limitInt <= 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid limit: "+params.Limit, nil)
	}
	pageInt, err := strconv.Atoi(params.Page)
	if err != nil || pageInt <= 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid page: "+params.Page, nil)
	}
	db := dbcore.GetDBInstance()
	logs, total, facets, err := queryAdminLogs(db, adminLogQuery{
		Limit:  limitInt,
		Page:   pageInt,
		Types:  parseAdminLogCSV(params.MsgType),
		Days:   parseAdminLogCSV(params.Day),
		Search: params.Q,
	})
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to retrieve logs: "+err.Error(), nil)
	}
	return map[string]any{
		"logs":  logs,
		"total": total,
		"types": facets.Types,
		"days":  facets.Days,
	}, nil
}

type adminLogQuery struct {
	Limit  int
	Page   int
	Types  []string
	Days   []string
	Search string
}

type adminLogFilterOption struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type adminLogFacets struct {
	Types []adminLogFilterOption `json:"types"`
	Days  []adminLogFilterOption `json:"days"`
}

func queryAdminLogs(db *gorm.DB, q adminLogQuery) ([]models.Log, int64, adminLogFacets, error) {
	var logs []models.Log
	var total int64
	offset := (q.Page - 1) * q.Limit
	countQuery := filterAdminLogs(db.Model(&models.Log{}), q)
	logsQuery := filterAdminLogs(db.Model(&models.Log{}), q)
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, adminLogFacets{}, err
	}
	if err := logsQuery.Order("time desc").Limit(q.Limit).Offset(offset).Find(&logs).Error; err != nil {
		return nil, 0, adminLogFacets{}, err
	}
	facets, err := listAdminLogFacets(db)
	if err != nil {
		return nil, 0, adminLogFacets{}, err
	}
	return logs, total, facets, nil
}

func listAdminLogFacets(db *gorm.DB) (adminLogFacets, error) {
	var facets adminLogFacets
	if err := db.Model(&models.Log{}).
		Select("msg_type AS value, COUNT(*) AS count").
		Where("msg_type <> ''").
		Group("msg_type").
		Order("count DESC, msg_type").
		Scan(&facets.Types).Error; err != nil {
		return facets, err
	}
	if err := db.Model(&models.Log{}).
		Select("date(time) AS value, COUNT(*) AS count").
		Group("date(time)").
		Order("value DESC").
		Scan(&facets.Days).Error; err != nil {
		return facets, err
	}
	return facets, nil
}

func filterAdminLogs(query *gorm.DB, q adminLogQuery) *gorm.DB {
	query = filterAdminLogsByValues(query, "msg_type", q.Types)
	query = filterAdminLogsByDays(query, q.Days)
	return filterAdminLogsBySearch(query, q.Search)
}

func filterAdminLogsByValues(query *gorm.DB, column string, values []string) *gorm.DB {
	if len(values) == 0 {
		return query
	}
	return query.Where(column+" IN ?", values)
}

func filterAdminLogsByDays(query *gorm.DB, days []string) *gorm.DB {
	if len(days) == 0 {
		return query
	}
	return query.Where("date(time) IN ?", days)
}

func filterAdminLogsBySearch(query *gorm.DB, search string) *gorm.DB {
	search = strings.TrimSpace(search)
	if search == "" {
		return query
	}
	like := "%" + escapeAdminLogLike(search) + "%"
	return query.Where(`ip LIKE ? ESCAPE '\' OR message LIKE ? ESCAPE '\'`, like, like)
}

func parseAdminLogCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func escapeAdminLogLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func adminCloudflaredStatus(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return cloudflared.Status(), nil
}

func adminStartCloudflared(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	var params struct {
		Token string `json:"token"`
	}
	req.BindParams(&params)
	token := strings.TrimSpace(params.Token)
	if token != "" {
		if err := cloudflared.SaveToken(token); err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to save Cloudflare Tunnel token: "+err.Error(), nil)
		}
	}
	if err := cloudflared.Start(token); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "warn", "audit.cloudflare_start", nil)
	return cloudflared.Status(), nil
}

func adminStopCloudflared(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	var params struct {
		CurrentPassword string `json:"current_password"`
		ConfirmText     string `json:"confirm_text"`
	}
	req.BindParams(&params)

	disablePasswordLogin, _ := config.GetAs[bool](config.DisablePasswordLoginKey, false)
	if !disablePasswordLogin {
		actor, _ := auditActor(ctx)
		if actor == "" {
			return nil, rpc.MakeError(rpc.Unauthenticated, "Unauthorized.", nil)
		}
		user, err := accounts.GetUserByUUID(actor)
		if err != nil {
			return nil, rpc.MakeError(rpc.Unauthenticated, "Failed to verify current user", nil)
		}
		if strings.TrimSpace(params.CurrentPassword) == "" {
			return nil, rpc.MakeError(rpc.InvalidParams, "Current password is required", nil)
		}
		if _, ok := accounts.CheckPassword(user.Username, params.CurrentPassword); !ok {
			return nil, rpc.MakeError(rpc.Unauthenticated, "Current password is incorrect", nil)
		}
	} else if strings.TrimSpace(params.ConfirmText) != cloudflaredStopConfirmText {
		return nil, rpc.MakeError(rpc.InvalidParams, "Type STOP CLOUDFLARED to confirm stopping cloudflared", nil)
	}

	if err := cloudflared.Stop(); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to stop cloudflared: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "warn", "audit.cloudflare_stop", nil)
	return cloudflared.Status(), nil
}

func adminRemoveCloudflaredToken(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	if err := cloudflared.RemoveToken(); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Failed to remove Cloudflare Tunnel token: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "warn", "audit.cloudflare_token_remove", nil)
	return cloudflared.Status(), nil
}

func adminTestSendMessage(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Provider string `json:"provider"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid request: "+err.Error(), nil)
	}
	err := messageSender.SendTestEventTo(params.Provider, models.EventMessage{
		Event:   "Test",
		Time:    time.Now().UTC(),
		Message: "This is a test message from Lite.",
	})
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to send message: "+err.Error(), nil)
	}
	return nil, nil
}

func adminTestGeoip(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		IP string `json:"ip"`
	}
	req.BindParams(&params)
	ip := params.IP
	if ip == "" {
		if meta := rpc.MetaFromContext(ctx); meta != nil {
			ip = meta.RemoteIP
		}
	}
	cfg, err := config.GetAs[bool](config.GeoIpEnabledKey, false)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get configuration: "+err.Error(), nil)
	}
	if !cfg {
		return nil, rpc.MakeError(rpc.InvalidParams, "GeoIP is not enabled in the configuration.", nil)
	}
	record, err := geoip.GetGeoInfo(net.ParseIP(ip))
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get GeoIP record: "+err.Error(), nil)
	}
	return record, nil
}
