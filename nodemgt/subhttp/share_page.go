package subhttp

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/nodemgt/models"
	"github.com/nuomiiiii/lite/nodemgt/store"
	"github.com/nuomiiiii/lite/nodemgt/sub"
	"github.com/nuomiiiii/lite/nodemgt/subtpl"
)

type sharePageNode struct {
	Name      string
	Protocol  string
	ProtoKey  string
	Transport string
	Security  string
	Address   string
	ShareURI  string
}

type shareProtoCount struct {
	Protocol string
	ProtoKey string
	Count    int
}

type shareClientLink struct {
	Label string
	URL   string
}

type sharePageData struct {
	Title         string
	Username      string
	PlanName      string
	StatusLabel   string
	StatusOK      bool
	AutoDetect    bool
	DefaultFormat string
	UsedText      string
	QuotaText     string
	RemainText    string
	Unlimited     bool
	Percent       int
	PercentBar    string
	BarClass      string
	ExpiresText   string
	ResetText     string
	BaseSubURL    string
	RawSubURL     string
	ClientLinks   []shareClientLink
	NodeCount     int
	ProtoCounts   []shareProtoCount
	Nodes         []sharePageNode
}

func wantsSharePage(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	view := strings.ToLower(strings.TrimSpace(c.Query("view")))
	if view == "share" || view == "html" || view == "info" {
		return true
	}
	if strings.TrimSpace(c.Query("target")) != "" ||
		strings.TrimSpace(c.Query("format")) != "" ||
		strings.TrimSpace(c.Query("raw")) != "" {
		return false
	}
	if subtpl.DetectClient(c.Request.UserAgent()) != "" {
		return false
	}
	accept := strings.ToLower(c.GetHeader("Accept"))
	return strings.Contains(accept, "text/html")
}

func WriteSharePage(c *gin.Context, st *store.Store, row models.Subscription) {
	now := time.Now().UTC()
	user, err := st.GetUser(row.UserID)
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	plan, _ := activeSubscriptionPlan(st, row.UserID, now)
	nodes, _ := sub.ForUser(st, *user)
	extras := ExpandedExtras(st, row.UserID, row.ExtraURLs)

	title := strings.TrimSpace(user.DisplayName)
	if title == "" {
		title = strings.TrimSpace(user.Username)
	}
	if title == "" {
		title = "订阅分享"
	}
	planName := ""
	if plan != nil {
		planName = strings.TrimSpace(plan.Name)
	}

	statusLabel := "正常"
	statusOK := true
	if user.Status != models.UserActive {
		statusLabel = "已停用"
		statusOK = false
	} else if user.ExpiresAt != nil && !user.ExpiresAt.After(now) {
		statusLabel = "已过期"
		statusOK = false
	}

	used := max(user.UsedBytes, 0)
	quota := max(user.QuotaBytes, 0)
	unlimited := quota <= 0
	pct := 0
	remainText := "不限"
	quotaText := "不限流量"
	barClass := "ok"
	if !unlimited {
		quotaText = formatBytesIEC(quota)
		remain := quota - used
		if remain < 0 {
			remain = 0
		}
		remainText = formatBytesIEC(remain)
		pct = int((used * 100) / quota)
		if pct > 100 {
			pct = 100
		}
		if pct >= 95 {
			barClass = "danger"
		} else if pct >= 80 {
			barClass = "warn"
		}
	}

	expiresText := "长期有效"
	if user.ExpiresAt != nil {
		expiresText = user.ExpiresAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")
	}
	resetText := ""
	if user.TrafficResetDay > 0 {
		resetText = fmt.Sprintf("每月 %d 日重置", user.TrafficResetDay)
	}

	baseURL := requestBaseSubURL(c)
	clientLinks := []shareClientLink{
		{Label: "Mihomo / Clash Meta", URL: appendQuery(baseURL, "target", "mihomo")},
		{Label: "Clash", URL: appendQuery(baseURL, "target", "clash")},
		{Label: "sing-box", URL: appendQuery(baseURL, "target", "sing-box")},
		{Label: "v2rayN / Shadowrocket", URL: appendQuery(baseURL, "target", "v2ray")},
		{Label: "Loon", URL: appendQuery(baseURL, "target", "loon")},
		{Label: "Surge", URL: appendQuery(baseURL, "target", "surge")},
		{Label: "Stash", URL: appendQuery(baseURL, "target", "stash")},
	}

	protoCountMap := map[string]*shareProtoCount{}
	pageNodes := make([]sharePageNode, 0, len(nodes)+len(extras))
	for _, n := range nodes {
		protoLabel, protoKey := prettyProtocol(n.Protocol)
		if protoCountMap[protoKey] == nil {
			protoCountMap[protoKey] = &shareProtoCount{Protocol: protoLabel, ProtoKey: protoKey}
		}
		protoCountMap[protoKey].Count++

		shareURI := ""
		if encoded := sub.RenderV2RayBase64([]sub.Node{n}, nil); encoded != "" {
			if links := sub.DecodeShareLinks(encoded); len(links) > 0 {
				shareURI = links[0]
			}
		}
		addr := ""
		if n.Host != "" && n.Port > 0 {
			addr = fmt.Sprintf("%s:%d", n.Host, n.Port)
		} else if n.Host != "" {
			addr = n.Host
		}
		pageNodes = append(pageNodes, sharePageNode{
			Name:      n.Name,
			Protocol:  protoLabel,
			ProtoKey:  protoKey,
			Transport: prettyTransport(n.Type),
			Security:  prettySecurity(n.Security),
			Address:   addr,
			ShareURI:  shareURI,
		})
	}
	for _, rawLink := range extras {
		scheme, name, addr := parseExtraNodeSummary(rawLink)
		protoLabel, protoKey := prettyProtocol(scheme)
		if protoCountMap[protoKey] == nil {
			protoCountMap[protoKey] = &shareProtoCount{Protocol: protoLabel, ProtoKey: protoKey}
		}
		protoCountMap[protoKey].Count++
		pageNodes = append(pageNodes, sharePageNode{
			Name:     name,
			Protocol: protoLabel,
			ProtoKey: protoKey,
			Address:  addr,
			ShareURI: rawLink,
		})
	}

	protoCounts := make([]shareProtoCount, 0, len(protoCountMap))
	for _, item := range protoCountMap {
		protoCounts = append(protoCounts, *item)
	}
	sort.Slice(protoCounts, func(i, j int) bool {
		if protoCounts[i].Count != protoCounts[j].Count {
			return protoCounts[i].Count > protoCounts[j].Count
		}
		return protoCounts[i].Protocol < protoCounts[j].Protocol
	})

	data := sharePageData{
		Title:         title,
		Username:      user.Username,
		PlanName:      planName,
		StatusLabel:   statusLabel,
		StatusOK:      statusOK,
		AutoDetect:    row.AutoDetect,
		DefaultFormat: strings.ToUpper(strings.TrimSpace(row.Format)),
		UsedText:      formatBytesIEC(used),
		QuotaText:     quotaText,
		RemainText:    remainText,
		Unlimited:     unlimited,
		Percent:       pct,
		PercentBar:    fmt.Sprintf("%d%%", pct),
		BarClass:      barClass,
		ExpiresText:   expiresText,
		ResetText:     resetText,
		BaseSubURL:    baseURL,
		RawSubURL:     appendQuery(baseURL, "raw", "1"),
		ClientLinks:   clientLinks,
		NodeCount:     len(pageNodes),
		ProtoCounts:   protoCounts,
		Nodes:         pageNodes,
	}

	var buf bytes.Buffer
	if err := sharePageTemplate.Execute(&buf, data); err != nil {
		c.String(http.StatusInternalServerError, "render error")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}

func requestBaseSubURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = c.Request.Host
	}
	path := c.Request.URL.Path
	if host == "" {
		return path
	}
	return scheme + "://" + host + path
}

func appendQuery(base, key, val string) string {
	if strings.Contains(base, "?") {
		return base + "&" + url.QueryEscape(key) + "=" + url.QueryEscape(val)
	}
	return base + "?" + url.QueryEscape(key) + "=" + url.QueryEscape(val)
}

func formatBytesIEC(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024.0
	v := float64(n)
	switch {
	case v >= unit*unit*unit*unit:
		return fmt.Sprintf("%.2f TiB", v/(unit*unit*unit*unit))
	case v >= unit*unit*unit:
		return fmt.Sprintf("%.2f GiB", v/(unit*unit*unit))
	case v >= unit*unit:
		return fmt.Sprintf("%.2f MiB", v/(unit*unit))
	case v >= unit:
		return fmt.Sprintf("%.1f KiB", v/unit)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func prettyProtocol(raw string) (label, key string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "anytls":
		return "AnyTLS", "anytls"
	case "vless":
		return "VLESS", "vless"
	case "vmess":
		return "VMess", "vmess"
	case "trojan":
		return "Trojan", "trojan"
	case "hysteria2", "hy2":
		return "Hysteria2", "hysteria2"
	case "tuic":
		return "TUIC", "tuic"
	case "shadowsocks", "ss":
		return "Shadowsocks", "ss"
	case "socks", "socks5":
		return "SOCKS5", "socks"
	case "http", "https":
		return "HTTP", "http"
	default:
		s := strings.TrimSpace(raw)
		if s == "" {
			return "Unknown", "other"
		}
		return strings.ToUpper(s), "other"
	}
}

func prettyTransport(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "tcp":
		return "TCP"
	case "ws":
		return "WebSocket"
	case "grpc":
		return "gRPC"
	case "http", "h2":
		return "HTTP/2"
	case "xhttp", "splithttp":
		return "XHTTP"
	case "quic":
		return "QUIC"
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

func prettySecurity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "reality":
		return "Reality"
	case "tls":
		return "TLS"
	case "", "none":
		return ""
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

func parseExtraNodeSummary(raw string) (scheme, name, addr string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "other", "外部节点", ""
	}
	scheme = u.Scheme
	name = strings.TrimSpace(u.Fragment)
	if decoded, err := url.QueryUnescape(name); err == nil && decoded != "" {
		name = decoded
	}
	if name == "" {
		name = u.Host
	}
	if name == "" {
		name = "外部节点"
	}
	return scheme, name, u.Host
}

var sharePageTemplate = template.Must(template.New("sub_share").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} - 订阅分享</title>
<style>
:root{--bg:#f5f7fb;--card:#ffffff;--border:#e5e7eb;--text:#111827;--muted:#6b7280;--accent:#2563eb;--accent-soft:#eff6ff;--ok:#16a34a;--ok-soft:#f0fdf4;--warn:#d97706;--danger:#dc2626;--chip:#f3f4f6}
@media(prefers-color-scheme:dark){:root{--bg:#0b1120;--card:#111827;--border:#1f2937;--text:#f9fafb;--muted:#9ca3af;--accent:#60a5fa;--accent-soft:#1e3a8a33;--ok:#4ade80;--ok-soft:#14532d44;--warn:#fbbf24;--danger:#f87171;--chip:#1f2937}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif;line-height:1.5}
.wrap{max-width:860px;margin:0 auto;padding:24px 16px 48px}
.card{background:var(--card);border:1px solid var(--border);border-radius:16px;padding:20px;margin-bottom:16px;box-shadow:0 1px 2px rgba(0,0,0,.04)}
.head{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:12px;margin-bottom:16px}
.title{font-size:20px;font-weight:700;margin:0;display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.badge{display:inline-flex;align-items:center;padding:3px 10px;border-radius:999px;font-size:12px;font-weight:600}
.badge-ok{background:var(--ok-soft);color:var(--ok)}
.badge-bad{background:#fee2e2;color:var(--danger)}
.badge-auto{background:var(--accent-soft);color:var(--accent)}
.meta{display:flex;flex-wrap:wrap;gap:14px;font-size:13px;color:var(--muted)}
.stats{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:12px}
@media(max-width:600px){.stats{grid-template-columns:1fr}}
.stat{background:var(--chip);border-radius:12px;padding:12px 14px}
.stat-label{font-size:12px;color:var(--muted);margin-bottom:4px}
.stat-val{font-size:18px;font-weight:700}
.bar-wrap{margin-top:14px}
.bar-bg{height:8px;background:var(--chip);border-radius:999px;overflow:hidden}
.bar-fill{height:100%;border-radius:999px;background:var(--accent);transition:width .3s}
.bar-fill.warn{background:var(--warn)}
.bar-fill.danger{background:var(--danger)}
.bar-note{display:flex;justify-content:space-between;font-size:12px;color:var(--muted);margin-top:6px}
.sec-title{font-size:15px;font-weight:600;margin:0 0 12px;display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap}
.url-row{display:flex;gap:8px;align-items:stretch}
.url-input{flex:1;min-width:0;padding:10px 12px;border-radius:10px;border:1px solid var(--border);background:var(--chip);color:var(--text);font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:13px}
.btn{cursor:pointer;border:1px solid var(--border);background:var(--card);color:var(--text);padding:8px 14px;border-radius:10px;font-size:13px;font-weight:600;white-space:nowrap;text-decoration:none;display:inline-flex;align-items:center;gap:6px}
.btn:hover{border-color:var(--accent);color:var(--accent)}
.btn-primary{background:var(--accent);border-color:var(--accent);color:#fff}
.btn-primary:hover{opacity:.92;color:#fff}
.clients{display:flex;flex-wrap:wrap;gap:8px;margin-top:12px}
.clients .btn{padding:6px 12px;font-size:12px;font-weight:500}
.proto-summary{display:flex;flex-wrap:wrap;gap:6px}
.node-list{display:flex;flex-direction:column;gap:8px}
.node-item{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:12px 14px;border:1px solid var(--border);border-radius:12px;flex-wrap:wrap}
.node-main{display:flex;flex-direction:column;gap:4px;min-width:0}
.node-name{font-size:14px;font-weight:600;display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.node-sub{font-size:12px;color:var(--muted);font-family:ui-monospace,SFMono-Regular,Menlo,monospace;word-break:break-all}
.tag{display:inline-flex;align-items:center;padding:2px 8px;border-radius:6px;font-size:11px;font-weight:600;background:var(--chip);color:var(--text)}
.p-anytls{background:#dbeafe;color:#1d4ed8}
.p-vless{background:#ede9fe;color:#6d28d9}
.p-vmess{background:#e0e7ff;color:#4338ca}
.p-trojan{background:#fef3c7;color:#b45309}
.p-hysteria2,.p-tuic{background:#dcfce7;color:#15803d}
.p-ss{background:#fce7f3;color:#be185d}
.toast{position:fixed;bottom:24px;left:50%;transform:translateX(-50%);background:#111827;color:#fff;padding:10px 18px;border-radius:999px;font-size:13px;opacity:0;pointer-events:none;transition:opacity .2s;z-index:99}
.toast.show{opacity:1}
.empty{color:var(--muted);font-size:13px;padding:12px 0;text-align:center}
</style>
</head>
<body>
<div class="wrap">
  <div class="card">
    <div class="head">
      <h1 class="title">
        <span>{{.Title}}</span>
        {{if .PlanName}}<span class="tag">{{.PlanName}}</span>{{end}}
        <span class="badge {{if .StatusOK}}badge-ok{{else}}badge-bad{{end}}">{{.StatusLabel}}</span>
        {{if .AutoDetect}}<span class="badge badge-auto">客户端自适应</span>{{end}}
      </h1>
      <div class="meta">
        <span>到期时间：{{.ExpiresText}}</span>
        {{if .ResetText}}<span>{{.ResetText}}</span>{{end}}
      </div>
    </div>
    <div class="stats">
      <div class="stat">
        <div class="stat-label">已使用流量</div>
        <div class="stat-val">{{.UsedText}}</div>
      </div>
      <div class="stat">
        <div class="stat-label">流量配额</div>
        <div class="stat-val">{{.QuotaText}}</div>
      </div>
      <div class="stat">
        <div class="stat-label">剩余流量</div>
        <div class="stat-val">{{.RemainText}}</div>
      </div>
    </div>
    {{if not .Unlimited}}
    <div class="bar-wrap">
      <div class="bar-bg"><div class="bar-fill {{.BarClass}}" style="width:{{.PercentBar}}"></div></div>
      <div class="bar-note"><span>已用 {{.Percent}}%</span><span>{{.UsedText}} / {{.QuotaText}}</span></div>
    </div>
    {{end}}
  </div>

  <div class="card">
    <div class="sec-title">
      <span>订阅链接</span>
      <a class="btn" href="{{.RawSubURL}}" target="_blank" rel="noopener">查看原始配置</a>
    </div>
    <div class="url-row">
      <input class="url-input" id="sub-url" readonly value="{{.BaseSubURL}}">
      <button class="btn btn-primary" type="button" onclick="copyText(document.getElementById('sub-url').value,'已复制通用订阅链接')">复制链接</button>
    </div>
    <div class="meta" style="margin-top:8px">
      {{if .AutoDetect}}
      <span>直接将上方链接粘贴到 Clash / Mihomo / v2rayN / Shadowrocket / sing-box / Loon / Surge 即可自动识别并返回对应配置。</span>
      {{else}}
      <span>当前订阅默认格式为 {{.DefaultFormat}}；也可点击下方按钮复制指定客户端专用链接。</span>
      {{end}}
    </div>
    <div class="clients">
      {{range .ClientLinks}}
      <button class="btn" type="button" data-url="{{.URL}}" data-label="{{.Label}}" onclick="copyText(this.dataset.url,'已复制 '+this.dataset.label+' 订阅链接')">复制 {{.Label}}</button>
      {{end}}
    </div>
  </div>

  <div class="card">
    <div class="sec-title">
      <span>可用节点（共 {{.NodeCount}} 个）</span>
      <div class="proto-summary">
        {{range .ProtoCounts}}
        <span class="tag p-{{.ProtoKey}}">{{.Protocol}} × {{.Count}}</span>
        {{end}}
      </div>
    </div>
    {{if .Nodes}}
    <div class="node-list">
      {{range .Nodes}}
      <div class="node-item">
        <div class="node-main">
          <div class="node-name">
            <span>{{.Name}}</span>
            <span class="tag p-{{.ProtoKey}}">{{.Protocol}}</span>
            {{if .Security}}<span class="tag">{{.Security}}</span>{{end}}
            {{if .Transport}}<span class="tag">{{.Transport}}</span>{{end}}
          </div>
          {{if .Address}}<div class="node-sub">{{.Address}}</div>{{end}}
        </div>
        {{if .ShareURI}}
        <button class="btn" type="button" data-uri="{{.ShareURI}}" data-name="{{.Name}}" onclick="copyText(this.dataset.uri,'已复制节点：'+this.dataset.name)">复制节点</button>
        {{end}}
      </div>
      {{end}}
    </div>
    {{else}}
    <div class="empty">当前订阅下暂无可用节点</div>
    {{end}}
  </div>
</div>
<div class="toast" id="toast"></div>
<script>
let toastTimer=null;
function showToast(msg){const el=document.getElementById('toast');el.textContent=msg;el.classList.add('show');clearTimeout(toastTimer);toastTimer=setTimeout(()=>el.classList.remove('show'),2000);}
function copyText(text,msg){if(!text)return;if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(text).then(()=>showToast(msg),()=>fallbackCopy(text,msg));}else{fallbackCopy(text,msg);}}
function fallbackCopy(text,msg){const ta=document.createElement('textarea');ta.value=text;ta.style.position='fixed';ta.style.opacity='0';document.body.appendChild(ta);ta.select();try{document.execCommand('copy');showToast(msg);}catch(e){showToast('复制失败，请手动复制');}document.body.removeChild(ta);}
</script>
</body>
</html>
`))
