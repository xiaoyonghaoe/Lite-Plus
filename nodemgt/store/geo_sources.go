package store

import (
	"encoding/json"
	"strings"
)

const SettingGeoRuleSources = "geo_rule_sources"

type GeoRuleSource struct {
	Kind    string `json:"kind"`              // "geosite" or "geoip"
	Code    string `json:"code"`              // e.g. "china-list", "cn", "private"
	Label   string `json:"label"`             // display name
	URL     string `json:"url,omitempty"`     // legacy alias for srs_url
	SRSURL  string `json:"srs_url,omitempty"` // sing-box .srs download URL
	DatURL  string `json:"dat_url,omitempty"` // xray .dat download URL
	Enabled bool   `json:"enabled"`
	Builtin bool   `json:"builtin,omitempty"`
}

// BuiltinGeoRuleSources returns the built-in upstream rules shown in the route
// condition dropdown. Agents already know how to fetch these by default.
func BuiltinGeoRuleSources() []GeoRuleSource {
	return []GeoRuleSource{
		{
			Kind:    "geosite",
			Code:    "china-list",
			Label:   "中国域名 (dnsmasq-china-list)",
			SRSURL:  "https://raw.githubusercontent.com/OriginVorfeed/singbox-china-list/main/geosite-cn.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "cn",
			Label:   "中国域名 (v2fly/SagerNet)",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "geolocation-!cn",
			Label:   "非中国域名",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-!cn.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "category-ads-all",
			Label:   "常见广告域名",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "openai",
			Label:   "OpenAI / ChatGPT",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-openai.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "google",
			Label:   "Google",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-google.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "youtube",
			Label:   "YouTube",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-youtube.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "netflix",
			Label:   "Netflix",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-netflix.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "telegram",
			Label:   "Telegram",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-telegram.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "apple",
			Label:   "Apple",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-apple.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "github",
			Label:   "GitHub",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-github.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geosite",
			Code:    "private",
			Label:   "私有/局域网域名",
			SRSURL:  "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-private.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geoip",
			Code:    "cn",
			Label:   "中国 IP (Loyalsoldier/BGP 同源)",
			SRSURL:  "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geoip/cn.srs",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat",
			Enabled: true,
			Builtin: true,
		},
		{
			Kind:    "geoip",
			Code:    "private",
			Label:   "内网私有 IP (内置)",
			SRSURL:  "",
			DatURL:  "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat",
			Enabled: true,
			Builtin: true,
		},
	}
}

func DefaultGeoRuleSources() []GeoRuleSource {
	return BuiltinGeoRuleSources()
}

func NormalizeGeoRuleSources(rows []GeoRuleSource) []GeoRuleSource {
	out := make([]GeoRuleSource, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		kind := strings.ToLower(strings.TrimSpace(r.Kind))
		if kind != "geosite" && kind != "geoip" {
			continue
		}
		code := strings.ToLower(strings.TrimSpace(r.Code))
		code = strings.TrimPrefix(code, kind+":")
		if code == "" {
			continue
		}
		key := kind + ":" + code
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		label := strings.TrimSpace(r.Label)
		if label == "" {
			label = key
		}
		srsURL := strings.TrimSpace(r.SRSURL)
		if srsURL == "" {
			srsURL = strings.TrimSpace(r.URL)
		}
		datURL := strings.TrimSpace(r.DatURL)
		out = append(out, GeoRuleSource{
			Kind:    kind,
			Code:    code,
			Label:   label,
			URL:     srsURL,
			SRSURL:  srsURL,
			DatURL:  datURL,
			Enabled: r.Enabled,
		})
	}
	return out
}

// GeoRuleSources returns user-configured custom Geodata sources (empty slice by default).
func (s *Store) GeoRuleSources() []GeoRuleSource {
	raw, err := s.GetSetting(SettingGeoRuleSources)
	if err != nil || strings.TrimSpace(raw) == "" {
		return []GeoRuleSource{}
	}
	var rows []GeoRuleSource
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return []GeoRuleSource{}
	}
	return NormalizeGeoRuleSources(rows)
}

func (s *Store) SetGeoRuleSources(rows []GeoRuleSource) error {
	cleaned := NormalizeGeoRuleSources(rows)
	raw, err := json.Marshal(cleaned)
	if err != nil {
		return err
	}
	return s.SetSetting(SettingGeoRuleSources, string(raw))
}

func (s *Store) EnabledGeoSourcesForSnapshot() []GeoRuleSource {
	all := s.GeoRuleSources()
	out := make([]GeoRuleSource, 0, len(all))
	for _, r := range all {
		if r.Enabled && (r.SRSURL != "" || r.DatURL != "") {
			out = append(out, r)
		}
	}
	return out
}
