package subhttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/nodemgt/models"
	"github.com/nuomiiiii/lite/nodemgt/store"
	"github.com/nuomiiiii/lite/nodemgt/sub"
	logger "github.com/nuomiiiii/lite/utils/log"
)

const (
	SettingCFKVEnabled            = "cf_kv_enabled"
	SettingCFKVAccountID          = "cf_kv_account_id"
	SettingCFKVNamespaceID        = "cf_kv_namespace_id"
	SettingCFKVAPIToken           = "cf_kv_api_token"
	SettingCFKVPublicDomain       = "cf_kv_public_domain"
	SettingCFKVSubPrefix          = "cf_kv_sub_prefix"
	SettingCFKVTrafficThresholdMB = "cf_kv_traffic_threshold_mb"
	SettingCFKVMaxStaleMinutes    = "cf_kv_max_stale_minutes"
	SettingCFKVDeleteRemoved      = "cf_kv_delete_removed"
	SettingCFKVSyncState          = "cf_kv_sync_state"

	defaultCFKVTrafficThresholdMB = 10
	defaultCFKVMaxStaleMinutes    = 60
	cfKVBulkChunkSize             = 1000
	cfKVAutoSyncInterval          = 2 * time.Minute
	cfKVTriggerDebounce           = 2 * time.Second
)

type CFKVConfig struct {
	Enabled            bool
	AccountID          string
	NamespaceID        string
	APIToken           string
	PublicDomain       string
	SubPrefix          string
	TrafficThresholdMB int
	MaxStaleMinutes    int
	DeleteRemoved      bool
}

type cfKVSnapshot struct {
	SubID        string   `json:"subId"`
	Email        string   `json:"email"`
	Title        string   `json:"title"`
	Enable       bool     `json:"enable"`
	Up           int64    `json:"up"`
	Down         int64    `json:"down"`
	Total        int64    `json:"total"`
	ExpiryTime   int64    `json:"expiryTime"`
	Nodes        []string `json:"nodes"`
	Raw          string   `json:"raw"`
	Clash        string   `json:"clash"`
	JSON         string   `json:"json"`
	PublicDomain string   `json:"publicDomain"`
	SubPrefix    string   `json:"subPrefix"`
	SyncedAt     string   `json:"syncedAt,omitempty"`
}

type cfKVUserState struct {
	FullHash    string `json:"full_hash"`
	ContentHash string `json:"content_hash"`
	UsedBytes   int64  `json:"used_bytes"`
	PushedAt    int64  `json:"pushed_at"`
	Reason      string `json:"reason,omitempty"`
}

type CFKVSyncResult struct {
	Total    int    `json:"total"`
	Pushed   int    `json:"pushed"`
	Skipped  int    `json:"skipped"`
	Deleted  int    `json:"deleted"`
	SyncedAt string `json:"synced_at"`
}

type CFKVSyncState struct {
	Users      map[string]cfKVUserState `json:"users"`
	LastSyncAt string                   `json:"last_sync_at,omitempty"`
	LastError  string                   `json:"last_error,omitempty"`
	LastResult *CFKVSyncResult          `json:"last_result,omitempty"`
}

type cfKVBulkItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type cfKVClient interface {
	BulkPut(items []cfKVBulkItem) error
	BulkDelete(keys []string) error
}

type httpCFKVClient struct {
	baseURL  string
	apiToken string
	client   *http.Client
}

func newHTTPCFKVClient(accountID, namespaceID, apiToken string) *httpCFKVClient {
	return &httpCFKVClient{
		baseURL: fmt.Sprintf(
			"https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s",
			strings.TrimSpace(accountID),
			strings.TrimSpace(namespaceID),
		),
		apiToken: strings.TrimSpace(apiToken),
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *httpCFKVClient) do(method, suffix string, bodyObj any) error {
	raw, err := json.Marshal(bodyObj)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, c.baseURL+suffix, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare kv api http %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Success bool `json:"success"`
		Errors  any  `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil && !parsed.Success {
		return fmt.Errorf("cloudflare kv api error: %v", parsed.Errors)
	}
	return nil
}

func (c *httpCFKVClient) BulkPut(items []cfKVBulkItem) error {
	for i := 0; i < len(items); i += cfKVBulkChunkSize {
		end := min(i+cfKVBulkChunkSize, len(items))
		if err := c.do(http.MethodPut, "/bulk", items[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (c *httpCFKVClient) BulkDelete(keys []string) error {
	for i := 0; i < len(keys); i += cfKVBulkChunkSize {
		end := min(i+cfKVBulkChunkSize, len(keys))
		if err := c.do(http.MethodDelete, "/bulk", keys[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func NormalizeCFPublicDomain(raw string) string {
	d := strings.TrimRight(strings.TrimSpace(raw), "/")
	if d == "" {
		return ""
	}
	low := strings.ToLower(d)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		d = "https://" + d
	}
	return d
}

func NormalizeCFSubPrefix(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "/")
}

func LoadCFKVConfig(st *store.Store) CFKVConfig {
	cfg := CFKVConfig{
		TrafficThresholdMB: defaultCFKVTrafficThresholdMB,
		MaxStaleMinutes:    defaultCFKVMaxStaleMinutes,
		DeleteRemoved:      true,
	}
	if st == nil {
		return cfg
	}
	get := func(k string) string {
		v, _ := st.GetSetting(k)
		return strings.TrimSpace(v)
	}
	cfg.Enabled = settingOn(get(SettingCFKVEnabled))
	cfg.AccountID = get(SettingCFKVAccountID)
	cfg.NamespaceID = get(SettingCFKVNamespaceID)
	cfg.APIToken = get(SettingCFKVAPIToken)
	cfg.PublicDomain = NormalizeCFPublicDomain(get(SettingCFKVPublicDomain))
	cfg.SubPrefix = NormalizeCFSubPrefix(get(SettingCFKVSubPrefix))
	if raw := get(SettingCFKVTrafficThresholdMB); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			cfg.TrafficThresholdMB = n
		}
	}
	if raw := get(SettingCFKVMaxStaleMinutes); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			cfg.MaxStaleMinutes = n
		}
	}
	if raw := get(SettingCFKVDeleteRemoved); raw != "" {
		cfg.DeleteRemoved = settingOn(raw)
	}
	return cfg
}

func LoadCFKVSyncState(st *store.Store) CFKVSyncState {
	state := CFKVSyncState{Users: map[string]cfKVUserState{}}
	if st == nil {
		return state
	}
	raw, err := st.GetSetting(SettingCFKVSyncState)
	if err != nil || strings.TrimSpace(raw) == "" {
		return state
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return CFKVSyncState{Users: map[string]cfKVUserState{}}
	}
	if state.Users == nil {
		state.Users = map[string]cfKVUserState{}
	}
	return state
}

func saveCFKVSyncState(st *store.Store, state CFKVSyncState) {
	if st == nil {
		return
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = st.SetSetting(SettingCFKVSyncState, string(raw))
}

func buildCFKVSnapshot(st *store.Store, row models.Subscription, user models.NodeUser, cfg CFKVConfig, now time.Time) cfKVSnapshot {
	plan, _ := activeSubscriptionPlan(st, row.UserID, now)
	title := strings.TrimSpace(user.DisplayName)
	if title == "" {
		title = strings.TrimSpace(user.Username)
	}
	if plan != nil {
		if pn := strings.TrimSpace(plan.Name); pn != "" {
			if title != "" {
				title += " · " + pn
			} else {
				title = pn
			}
		}
	}
	if title == "" {
		title = row.Token
	}

	active := row.Enabled && user.Status == models.UserActive && (user.ExpiresAt == nil || user.ExpiresAt.After(now))
	var rawBody, clashBody, jsonBody string
	var nodeLinks []string
	if active {
		if doc, err := Generate(st, row, GenerateRequest{Target: "v2ray"}); err == nil {
			rawBody = strings.TrimSpace(doc.Body)
			nodeLinks = sub.DecodeShareLinks(rawBody)
		}
		if doc, err := Generate(st, row, GenerateRequest{Target: "mihomo"}); err == nil {
			clashBody = strings.TrimSpace(doc.Body)
		}
		if doc, err := Generate(st, row, GenerateRequest{Target: "sing-box"}); err == nil {
			trimmed := strings.TrimSpace(doc.Body)
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				jsonBody = trimmed
			}
		}
	}
	if nodeLinks == nil {
		nodeLinks = []string{}
	}

	var expiryMs int64
	if user.ExpiresAt != nil {
		expiryMs = user.ExpiresAt.UnixMilli()
	}
	return cfKVSnapshot{
		SubID:        row.Token,
		Email:        user.Username,
		Title:        title,
		Enable:       active,
		Up:           0,
		Down:         max(user.UsedBytes, 0),
		Total:        max(user.QuotaBytes, 0),
		ExpiryTime:   max(expiryMs, 0),
		Nodes:        nodeLinks,
		Raw:          rawBody,
		Clash:        clashBody,
		JSON:         jsonBody,
		PublicDomain: cfg.PublicDomain,
		SubPrefix:    cfg.SubPrefix,
	}
}

func hashCFKVSnapshot(s cfKVSnapshot, includeTraffic bool) string {
	copySnap := s
	copySnap.SyncedAt = ""
	if !includeTraffic {
		copySnap.Up = 0
		copySnap.Down = 0
	}
	raw, _ := json.Marshal(copySnap)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func shouldPushCFKVSnapshot(snap cfKVSnapshot, prev *cfKVUserState, thresholdBytes int64, maxStaleSec int64, nowUnix int64, force bool) (bool, string) {
	if force || prev == nil {
		return true, "new_or_forced"
	}
	fullHash := hashCFKVSnapshot(snap, true)
	if prev.FullHash == fullHash {
		return false, "unchanged"
	}
	contentHash := hashCFKVSnapshot(snap, false)
	if prev.ContentHash != contentHash {
		return true, "config_or_status_changed"
	}
	currUsed := snap.Up + snap.Down
	diff := currUsed - prev.UsedBytes
	if diff < 0 {
		diff = -diff
	}
	if diff >= thresholdBytes {
		return true, "traffic_threshold_reached"
	}
	if maxStaleSec > 0 && (nowUnix-prev.PushedAt) >= maxStaleSec {
		return true, "stale_refresh"
	}
	return false, "below_traffic_threshold"
}

var cfKVSyncMu sync.Mutex

func SyncCFKV(st *store.Store, force bool) (CFKVSyncResult, error) {
	cfg := LoadCFKVConfig(st)
	if !force && !cfg.Enabled {
		return CFKVSyncResult{}, nil
	}
	if cfg.AccountID == "" || cfg.NamespaceID == "" || cfg.APIToken == "" {
		return CFKVSyncResult{}, fmt.Errorf("cloudflare kv account_id, namespace_id, and api_token are required")
	}
	client := newHTTPCFKVClient(cfg.AccountID, cfg.NamespaceID, cfg.APIToken)
	return syncCFKVWithClient(st, cfg, client, force, time.Now().UTC())
}

func syncCFKVWithClient(st *store.Store, cfg CFKVConfig, kv cfKVClient, force bool, now time.Time) (CFKVSyncResult, error) {
	cfKVSyncMu.Lock()
	defer cfKVSyncMu.Unlock()

	state := LoadCFKVSyncState(st)
	subs, err := st.ListSubscriptions()
	if err != nil {
		state.LastError = err.Error()
		saveCFKVSyncState(st, state)
		return CFKVSyncResult{}, err
	}

	nowUnix := now.Unix()
	syncedISO := now.Format(time.RFC3339)
	thresholdBytes := int64(cfg.TrafficThresholdMB) * 1024 * 1024
	maxStaleSec := int64(cfg.MaxStaleMinutes) * 60

	toPut := make([]cfKVBulkItem, 0, len(subs))
	nextUsers := make(map[string]cfKVUserState, len(subs))
	seenTokens := make(map[string]struct{}, len(subs))
	result := CFKVSyncResult{SyncedAt: syncedISO}

	for _, row := range subs {
		token := strings.TrimSpace(row.Token)
		if token == "" {
			continue
		}
		user, err := st.GetUser(row.UserID)
		if err != nil || models.IsRelayUser(user.Kind, user.Username) {
			continue
		}
		seenTokens[token] = struct{}{}
		result.Total++

		snap := buildCFKVSnapshot(st, row, *user, cfg, now)
		var prevPtr *cfKVUserState
		if p, ok := state.Users[token]; ok {
			pCopy := p
			prevPtr = &pCopy
		}
		push, reason := shouldPushCFKVSnapshot(snap, prevPtr, thresholdBytes, maxStaleSec, nowUnix, force)
		if push {
			snap.SyncedAt = syncedISO
			valBytes, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			toPut = append(toPut, cfKVBulkItem{
				Key:   "sub:" + token,
				Value: string(valBytes),
			})
			nextUsers[token] = cfKVUserState{
				FullHash:    hashCFKVSnapshot(snap, true),
				ContentHash: hashCFKVSnapshot(snap, false),
				UsedBytes:   snap.Up + snap.Down,
				PushedAt:    nowUnix,
				Reason:      reason,
			}
			result.Pushed++
		} else if prevPtr != nil {
			nextUsers[token] = *prevPtr
			result.Skipped++
		}
	}

	var toDelete []string
	if cfg.DeleteRemoved {
		for oldToken := range state.Users {
			if _, ok := seenTokens[oldToken]; !ok {
				toDelete = append(toDelete, "sub:"+oldToken)
			}
		}
		sort.Strings(toDelete)
		result.Deleted = len(toDelete)
	}

	if len(toPut) > 0 {
		if err := kv.BulkPut(toPut); err != nil {
			state.LastError = err.Error()
			saveCFKVSyncState(st, state)
			return result, err
		}
	}
	if len(toDelete) > 0 {
		if err := kv.BulkDelete(toDelete); err != nil {
			state.LastError = err.Error()
			saveCFKVSyncState(st, state)
			return result, err
		}
	}

	state.Users = nextUsers
	state.LastSyncAt = syncedISO
	state.LastError = ""
	resCopy := result
	state.LastResult = &resCopy
	saveCFKVSyncState(st, state)
	return result, nil
}

var (
	cfKVLoopOnce  sync.Once
	cfKVTriggerCh = make(chan struct{}, 1)
)

// TriggerCFKVSync requests an asynchronous, debounced Cloudflare KV sync.
// It never blocks the caller and coalesces bursts of edits (e.g. user create +
// subscription create) into a single KV sync pass.
func TriggerCFKVSync() {
	select {
	case cfKVTriggerCh <- struct{}{}:
	default:
	}
}

func runCFKVAutoSyncPass() {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Errorf("nodemgt", "cf kv auto sync panic: %v", rec)
		}
	}()
	db := dbcore.GetDBInstance()
	if db == nil {
		return
	}
	st := store.New(db)
	cfg := LoadCFKVConfig(st)
	if !cfg.Enabled || cfg.AccountID == "" || cfg.NamespaceID == "" || cfg.APIToken == "" {
		return
	}
	if _, err := SyncCFKV(st, false); err != nil {
		logger.Warnf("nodemgt", "cf kv auto sync failed: %v", err)
	}
}

func StartCFKVSyncLoop() {
	cfKVLoopOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(cfKVAutoSyncInterval)
			defer ticker.Stop()

			var debounceTimer *time.Timer
			var debounceC <-chan time.Time
			stopDebounce := func() {
				if debounceTimer != nil {
					if !debounceTimer.Stop() {
						select {
						case <-debounceTimer.C:
						default:
						}
					}
					debounceTimer = nil
					debounceC = nil
				}
			}

			for {
				select {
				case <-ticker.C:
					stopDebounce()
					runCFKVAutoSyncPass()
				case <-cfKVTriggerCh:
					if debounceTimer == nil {
						debounceTimer = time.NewTimer(cfKVTriggerDebounce)
						debounceC = debounceTimer.C
					} else {
						if !debounceTimer.Stop() {
							select {
							case <-debounceTimer.C:
							default:
							}
						}
						debounceTimer.Reset(cfKVTriggerDebounce)
					}
				case <-debounceC:
					debounceTimer = nil
					debounceC = nil
					runCFKVAutoSyncPass()
				}
			}
		}()
	})
}
