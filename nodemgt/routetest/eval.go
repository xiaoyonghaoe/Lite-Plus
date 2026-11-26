package routetest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/nodemgt/models"
	"github.com/nuomiiiii/lite/nodemgt/store"
)

type Request struct {
	NodeUUID  string `json:"node_uuid"`
	InboundID string `json:"inbound_id"`
	UserID    string `json:"user_id"`
	Target    string `json:"target"`
	Port      int    `json:"port"`
	Network   string `json:"network"`
	Protocol  string `json:"protocol"`
}

type TraceItem struct {
	RouteID      string `json:"route_id"`
	RouteName    string `json:"route_name"`
	Priority     int    `json:"priority"`
	OutboundID   string `json:"outbound_id"`
	OutboundName string `json:"outbound_name"`
	Matched      bool   `json:"matched"`
	Reason       string `json:"reason"`
}

type Result struct {
	Target          string      `json:"target"`
	Domain          string      `json:"domain,omitempty"`
	InputIP         string      `json:"input_ip,omitempty"`
	ResolvedIPs     []string    `json:"resolved_ips,omitempty"`
	Port            int         `json:"port"`
	Network         string      `json:"network"`
	UserAuthorized  *bool       `json:"user_authorized,omitempty"`
	UserNote        string      `json:"user_note,omitempty"`
	Matched         bool        `json:"matched"`
	MatchedRouteID  string      `json:"matched_route_id,omitempty"`
	MatchedRoute    string      `json:"matched_route,omitempty"`
	MatchedReason   string      `json:"matched_reason"`
	OutboundID      string      `json:"outbound_id"`
	OutboundName    string      `json:"outbound_name"`
	Trace           []TraceItem `json:"trace"`
	Warnings        []string    `json:"warnings,omitempty"`
}

type routeRuleSpec struct {
	InboundID  string `json:"inbound_id"`
	OutboundID string `json:"outbound_id"`
	Domain     string `json:"domain"`
	IP         string `json:"ip"`
	Network    string `json:"network"`
	Protocol   string `json:"protocol"`
	Port       string `json:"port"`
	Source     string `json:"source"`
}

func parseTarget(raw string, fallbackPort int) (domain string, ip netip.Addr, port int) {
	port = fallbackPort
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", netip.Addr{}, port
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			if p := u.Port(); p != "" {
				if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
					port = n
				}
			}
			s = u.Hostname()
		}
	} else {
		if idx := strings.IndexByte(s, '/'); idx >= 0 {
			s = s[:idx]
		}
		if host, p, err := net.SplitHostPort(s); err == nil {
			s = host
			if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
				port = n
			}
		}
	}
	s = strings.Trim(strings.TrimSpace(s), "[]")
	if addr, err := netip.ParseAddr(s); err == nil {
		return "", addr.Unmap(), port
	}
	if port <= 0 {
		port = 443
	}
	return strings.ToLower(strings.TrimSuffix(s, ".")), netip.Addr{}, port
}

func Evaluate(ctx context.Context, st *store.Store, req Request) (Result, error) {
	nodeUUID := strings.TrimSpace(req.NodeUUID)
	inboundID := strings.TrimSpace(req.InboundID)
	userID := strings.TrimSpace(req.UserID)

	if nodeUUID == "" && inboundID != "" {
		baseID, _, _ := strings.Cut(inboundID, "#")
		if inb, err := st.GetInbound(baseID); err == nil && inb != nil {
			nodeUUID = inb.NodeUUID
		}
	}
	if nodeUUID == "" {
		return Result{}, fmt.Errorf("node_uuid required")
	}

	domain, inputIP, port := parseTarget(req.Target, req.Port)
	if domain == "" && !inputIP.IsValid() {
		return Result{}, fmt.Errorf("valid domain or IP required")
	}
	if port <= 0 {
		port = 443
	}
	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = "tcp"
	}
	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))

	res := Result{
		Target:       strings.TrimSpace(req.Target),
		Domain:       domain,
		Port:         port,
		Network:      network,
		OutboundID:   "direct",
		OutboundName: "direct (直连)",
		MatchedReason: "未命中任何自定义路由，走默认直连 (direct)",
	}

	var candidateIPs []netip.Addr
	if inputIP.IsValid() {
		res.InputIP = inputIP.String()
		candidateIPs = append(candidateIPs, inputIP)
	} else if domain != "" {
		dnsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		addrs, err := net.DefaultResolver.LookupNetIP(dnsCtx, "ip", domain)
		cancel()
		if err == nil {
			for _, a := range addrs {
				a = a.Unmap()
				candidateIPs = append(candidateIPs, a)
				res.ResolvedIPs = append(res.ResolvedIPs, a.String())
			}
		}
	}

	// Check user grant if both user_id and inbound_id are selected
	if userID != "" {
		user, err := st.GetUser(userID)
		if err != nil || user == nil {
			ok := false
			res.UserAuthorized = &ok
			res.UserNote = "用户不存在"
		} else if !strings.EqualFold(user.Status, models.UserActive) {
			ok := false
			res.UserAuthorized = &ok
			res.UserNote = fmt.Sprintf("用户当前状态为 %s（非激活）", user.Status)
		} else if inboundID != "" {
			grants, _ := st.ListGrants(userID)
			allowed := false
			for _, g := range grants {
				if g.InboundID == inboundID {
					allowed = true
					break
				}
			}
			res.UserAuthorized = &allowed
			if allowed {
				res.UserNote = fmt.Sprintf("用户 %s 拥有该入站访问权限", user.Username)
			} else {
				res.UserNote = fmt.Sprintf("用户 %s 未被授权访问所选入站（仅模拟路由规则）", user.Username)
			}
		} else {
			ok := true
			res.UserAuthorized = &ok
			res.UserNote = fmt.Sprintf("用户 %s 状态正常", user.Username)
		}
	}

	outbounds, _ := st.ListOutbounds(nodeUUID)
	outName := func(id string) string {
		switch strings.ToLower(strings.TrimSpace(id)) {
		case "", "direct":
			return "direct (直连)"
		case "block":
			return "block (拦截)"
		}
		for _, o := range outbounds {
			if o.ID == id {
				return o.Name
			}
		}
		return id
	}

	routes, err := st.ListRoutes(nodeUUID)
	if err != nil {
		return Result{}, err
	}
	var enabled []models.Route
	for _, r := range routes {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		if enabled[i].Priority != enabled[j].Priority {
			return enabled[i].Priority < enabled[j].Priority
		}
		return enabled[i].CreatedAt.Before(enabled[j].CreatedAt)
	})

	for _, r := range enabled {
		var spec routeRuleSpec
		_ = json.Unmarshal([]byte(r.RuleJSON), &spec)
		oName := outName(spec.OutboundID)

		matched, reason, warns := matchRouteSpec(ctx, st, spec, inboundID, domain, candidateIPs, port, network, protocol)
		for _, w := range warns {
			res.Warnings = appendUnique(res.Warnings, w)
		}
		res.Trace = append(res.Trace, TraceItem{
			RouteID:      r.ID,
			RouteName:    r.Name,
			Priority:     r.Priority,
			OutboundID:   spec.OutboundID,
			OutboundName: oName,
			Matched:      matched,
			Reason:       reason,
		})
		if matched && !res.Matched {
			res.Matched = true
			res.MatchedRouteID = r.ID
			res.MatchedRoute = r.Name
			res.MatchedReason = reason
			res.OutboundID = spec.OutboundID
			res.OutboundName = oName
		}
	}
	return res, nil
}

func matchRouteSpec(
	ctx context.Context,
	st *store.Store,
	spec routeRuleSpec,
	inboundID string,
	domain string,
	ips []netip.Addr,
	port int,
	network string,
	protocol string,
) (bool, string, []string) {
	var reasons []string
	var warns []string

	// 1. Inbound filter (exact match including virtual inbound id#tag)
	ruleInb := strings.TrimSpace(spec.InboundID)
	if ruleInb != "" {
		if inboundID == "" {
			return false, fmt.Sprintf("规则限定入站，测试未选择对应入站"), nil
		}
		if ruleInb != inboundID {
			return false, "入站不匹配", nil
		}
		reasons = append(reasons, "入站匹配")
	}

	// 2. Network filter
	ruleNet := strings.ToLower(strings.TrimSpace(spec.Network))
	if ruleNet != "" && ruleNet != "tcp,udp" && ruleNet != "udp,tcp" {
		if !csvContains(ruleNet, network) {
			return false, fmt.Sprintf("网络不匹配（规则要求 %s）", ruleNet), nil
		}
		reasons = append(reasons, "网络="+network)
	}

	// 3. Protocol filter
	ruleProto := strings.ToLower(strings.TrimSpace(spec.Protocol))
	if ruleProto != "" {
		if protocol == "" || !csvContains(ruleProto, protocol) {
			return false, fmt.Sprintf("协议不匹配（规则要求 %s）", ruleProto), nil
		}
		reasons = append(reasons, "协议="+protocol)
	}

	// 4. Port filter
	rulePort := strings.TrimSpace(spec.Port)
	if rulePort != "" {
		if !matchPortList(rulePort, port) {
			return false, fmt.Sprintf("端口 %d 不在 %s 范围内", port, rulePort), nil
		}
		reasons = append(reasons, fmt.Sprintf("端口=%d", port))
	}

	// 5. Domain filter
	ruleDomain := strings.TrimSpace(spec.Domain)
	if ruleDomain != "" {
		if domain == "" {
			return false, "规则要求匹配域名，当前测试目标为纯 IP", nil
		}
		ok, hit, w := matchDomainTokens(ctx, st, ruleDomain, domain)
		warns = append(warns, w...)
		if !ok {
			return false, fmt.Sprintf("域名 %s 未命中 %s", domain, ruleDomain), warns
		}
		reasons = append(reasons, "命中域名规则 "+hit)
	}

	// 6. IP filter
	ruleIP := strings.TrimSpace(spec.IP)
	if ruleIP != "" {
		if len(ips) == 0 {
			return false, "规则要求匹配 IP，但当前目标无法解析到 IP", warns
		}
		ok, hit, w := matchIPTokens(ctx, st, ruleIP, ips)
		warns = append(warns, w...)
		if !ok {
			return false, fmt.Sprintf("IP 未命中 %s", ruleIP), warns
		}
		reasons = append(reasons, "命中 IP 规则 "+hit)
	}

	if len(reasons) == 0 {
		return true, "通配规则（匹配全部流量）", warns
	}
	return true, strings.Join(reasons, " + "), warns
}

func splitTokens(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func csvContains(raw, target string) bool {
	for _, p := range splitTokens(raw) {
		if strings.EqualFold(p, target) {
			return true
		}
	}
	return false
}

func matchPortList(raw string, port int) bool {
	for _, tok := range splitTokens(raw) {
		if a, b, ok := strings.Cut(tok, "-"); ok {
			lo, err1 := strconv.Atoi(strings.TrimSpace(a))
			hi, err2 := strconv.Atoi(strings.TrimSpace(b))
			if err1 == nil && err2 == nil && port >= lo && port <= hi {
				return true
			}
			continue
		}
		if p, err := strconv.Atoi(tok); err == nil && p == port {
			return true
		}
	}
	return false
}

func matchDomainTokens(ctx context.Context, st *store.Store, raw, domain string) (bool, string, []string) {
	var warns []string
	for _, tok := range splitTokens(raw) {
		low := strings.ToLower(tok)
		switch {
		case strings.HasPrefix(low, "geosite:"):
			code := strings.TrimPrefix(low, "geosite:")
			rs, err := loadSRSRuleSet(ctx, st, "geosite", code)
			if err != nil {
				warns = append(warns, fmt.Sprintf("加载 %s 失败: %v", tok, err))
				continue
			}
			if rs.MatchDomain(domain) {
				return true, tok, warns
			}
		case strings.HasPrefix(low, "full:"):
			if domain == strings.TrimPrefix(low, "full:") {
				return true, tok, warns
			}
		case strings.HasPrefix(low, "keyword:"):
			if strings.Contains(domain, strings.TrimPrefix(low, "keyword:")) {
				return true, tok, warns
			}
		case strings.HasPrefix(low, "regexp:"):
			if re, err := regexp.Compile(tok[len("regexp:"):]); err == nil && re.MatchString(domain) {
				return true, tok, warns
			}
		default:
			suffix := strings.TrimPrefix(strings.TrimPrefix(low, "domain:"), ".")
			if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
				return true, tok, warns
			}
		}
	}
	return false, "", warns
}

func isPrivateIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified()
}

func matchIPTokens(ctx context.Context, st *store.Store, raw string, ips []netip.Addr) (bool, string, []string) {
	var warns []string
	for _, tok := range splitTokens(raw) {
		low := strings.ToLower(tok)
		if strings.HasPrefix(low, "geoip:") {
			code := strings.TrimPrefix(low, "geoip:")
			if code == "private" {
				for _, ip := range ips {
					if isPrivateIP(ip) {
						return true, tok + " (" + ip.String() + ")", warns
					}
				}
				continue
			}
			rs, err := loadSRSRuleSet(ctx, st, "geoip", code)
			if err != nil {
				warns = append(warns, fmt.Sprintf("加载 %s 失败: %v", tok, err))
				continue
			}
			for _, ip := range ips {
				if rs.MatchIP(ip) {
					return true, tok + " (" + ip.String() + ")", warns
				}
			}
			continue
		}
		if pfx, err := netip.ParsePrefix(tok); err == nil {
			for _, ip := range ips {
				if pfx.Contains(ip) {
					return true, tok + " (" + ip.String() + ")", warns
				}
			}
			continue
		}
		if addr, err := netip.ParseAddr(tok); err == nil {
			addr = addr.Unmap()
			for _, ip := range ips {
				if ip == addr {
					return true, tok, warns
				}
			}
		}
	}
	return false, "", warns
}

func appendUnique(list []string, item string) []string {
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}
