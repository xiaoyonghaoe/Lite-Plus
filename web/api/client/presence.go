package client

import (
	"sync"
	"time"

	"github.com/nuomiiiii/lite/utils/notifier"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/connection"
)

const (
	postPresenceTTL = 35 * time.Second
)

// postPresenceEntry 保存单个客户端的 POST 上报会话状态
type postPresenceEntry struct {
	connID     int64
	timer      *time.Timer
	generation uint64 // 每次 Reset 递增，用于回调中判断是否为过期的旧回调
}

var (
	postPresenceMu     sync.Mutex
	postPresenceStates = make(map[string]*postPresenceEntry)
)

// refreshPostPresence 使用默认最小 TTL 刷新 HTTP POST 上报者的在线/离线状态。
func refreshPostPresence(uuid string) {
	refreshPostPresenceWithTTL(uuid, postPresenceTTL)
}

// refreshPostPresenceWithTTL 管理 HTTP POST 上报者的在线/离线状态。
// 每次 POST 按当前下发的上报周期动态刷新 TTL 定时器，防止闲置慢周期下误报离线。
func refreshPostPresenceWithTTL(uuid string, ttl time.Duration) {
	if ttl < postPresenceTTL {
		ttl = postPresenceTTL
	}
	postPresenceMu.Lock()
	defer postPresenceMu.Unlock()

	if entry, exists := postPresenceStates[uuid]; exists {
		entry.generation++
		entry.timer.Stop()
		gen := entry.generation
		entry.timer = time.AfterFunc(ttl, func() {
			postPresenceExpired(uuid, entry.connID, gen)
		})
		agent_runtime.KeepAlivePresence(uuid, entry.connID, ttl)
		return
	}

	connID := connection.NextID()
	agent_runtime.KeepAlivePresence(uuid, connID, ttl)
	notifier.NotifyOnlineAsync(uuid, connID)

	defaultGeneration := uint64(0)
	entry := &postPresenceEntry{
		connID:     connID,
		generation: defaultGeneration,
	}
	entry.timer = time.AfterFunc(ttl, func() {
		postPresenceExpired(uuid, connID, defaultGeneration)
	})

	postPresenceStates[uuid] = entry
}

// postPresenceExpired 是定时器到期的回调。
// 只有当 connID 和 generation 都与当前 entry 匹配时才执行离线清理，
// 避免 timer.Reset 竞态导致过期回调错误地清除仍活跃的会话。
func postPresenceExpired(uuid string, connID int64, gen uint64) {
	postPresenceMu.Lock()
	e, ok := postPresenceStates[uuid]
	if !ok || e.connID != connID || e.generation != gen {
		postPresenceMu.Unlock()
		return
	}
	delete(postPresenceStates, uuid)
	postPresenceMu.Unlock()

	agent_runtime.SetPresence(uuid, connID, false)
	if liveID, live := agent_runtime.ActiveConnectionID(uuid); live {
		notifier.RebindOnlineConnection(uuid, connID, liveID)
		return
	}
	notifier.OfflineNotification(uuid, connID)
}

func httpSessionGoneShouldNotify(uuid string) bool {
	_, live := agent_runtime.ActiveConnectionID(uuid)
	return !live
}
