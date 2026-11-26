package store

import (
	"encoding/json"
	"strings"
)

const SettingGeoRuleSources = "geo_rule_sources"

type GeoRuleSource struct {
	Kind    string `json:"kind"` // "geosite" or "geoip"
	Code    string `json:"code"` // e.g. "china-list", "cn", "private"
	Label   string `json:"label"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

func DefaultGeoRuleSources() []GeoRuleSource {
	return []GeoRuleSource{
		{
			Kind:    "geosite",
			Code:    "china-list",
			Label:   "中国域名 (dnsmasq-china-list)",
			URL:     "https://raw.githubusercontent.com/OriginVorfeed/singbox-china-list/main/geosite-cn.srs",
			Enabled: true,
		},
		{
			Kind:    "geosite",
			Code:    "cn",
			Label:   "中国域名 (SagerNet)",
			URL:     "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
			Enabled: true,
		},
		{
			Kind:    "geosite",
			Code:    "category-ads-all",
			Label:   "常见广告域名",
			URL:     "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs",
			Enabled: true,
		},
		{
			Kind:    "geosite",
			Code:    "geolocation-!cn",
			Label:   "非中国域名",
			URL:     "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-!cn.srs",
			Enabled: false,
		},
		{
			Kind:    "geosite",
			Code:    "openai",
			Label:   "OpenAI / ChatGPT",
			URL:     "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-openai.srs",
			Enabled: false,
		},
		{
			Kind:    "geosite",
			Code:    "google",
			Label:   "Google",
			URL:     "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-google.srs",
			Enabled: false,
		},
		{
			Kind:    "geosite",
			Code:    "netflix",
			Label:   "Netflix",
			URL:     "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-netflix.srs",
			Enabled: false,
		},
		{
			Kind:    "geosite",
			Code:    "telegram",
			Label:   "Telegram",
			URL:     "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-telegram.srs",
			Enabled: false,
		},
		{
			Kind:    "geoip",
			Code:    "cn",
			Label:   "中国 IP (Loyalsoldier/BGP 同源)",
			URL:     "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geoip/cn.srs",
			Enabled: true,
		},
		{
			Kind:    "geoip",
			Code:    "private",
			Label:   "内网私有 IP (内置)",
			URL:     "",
			Enabled: true,
		},
	}
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
		out = append(out, GeoRuleSource{
			Kind:    kind,
			Code:    code,
			Label:   label,
			URL:     strings.TrimSpace(r.URL),
			Enabled: r.Enabled,
		})
	}
	return out
}

func (s *Store) GeoRuleSources() []GeoRuleSource {
	raw, err := s.GetSetting(SettingGeoRuleSources)
	if err != nil || strings.TrimSpace(raw) == "" {
		return DefaultGeoRuleSources()
	}
	var rows []GeoRuleSource
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return DefaultGeoRuleSources()
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
		if r.Enabled && r.URL != "" {
			out = append(out, r)
		}
	}
	return out
}
