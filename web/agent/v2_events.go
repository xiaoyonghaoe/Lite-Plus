package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

const (
	v2EventQueueLimit = 128
	v2EventTTL        = 5 * time.Minute
	v2PingEventTTL    = 3 * time.Second
)

type v2EventQueue struct {
	events []v2.Event
}

var (
	v2EventMu     sync.Mutex
	v2EventQueues = make(map[string]*v2EventQueue)
)

func getV2EventQueueLocked(uuid string) *v2EventQueue {
	q := v2EventQueues[uuid]
	if q == nil {
		q = &v2EventQueue{}
		v2EventQueues[uuid] = q
	}
	return q
}

func DispatchV2Event(uuid, method string, params any) bool {
	event := EnqueueV2Event(uuid, method, params)
	return event.ID != ""
}

func DispatchV2Config(uuid string, params v2.ConfigParams) (v2.Event, bool, bool) {
	event := EnqueueV2Event(uuid, v2.MethodAgentConfig, params)
	if event.ID == "" {
		return event, false, true
	}
	return event, false, true
}

func DispatchPing(uuid string, params v2.PingParams) bool {
	return DispatchV2Event(uuid, v2.MethodAgentPing, params)
}

func IsAgentOnline(uuid string) bool {
	return IsPresent(uuid)
}

func EnqueueV2Event(uuid, method string, params any) v2.Event {
	if metricstore.EntityWritesBlocked(uuid) {
		return v2.Event{}
	}
	now := time.Now().UTC()
	ttl := eventTTL(method)
	event := v2.Event{
		ID:        newV2EventID(),
		Method:    method,
		Params:    params,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}

	v2EventMu.Lock()
	q := getV2EventQueueLocked(uuid)
	pruneExpiredV2EventsLocked(q)
	coalesceV2EventLocked(q, event)
	if len(q.events) >= v2EventQueueLimit {
		v2EventMu.Unlock()
		return v2.Event{}
	}
	q.events = append(q.events, event)
	v2EventMu.Unlock()

	return event
}

func eventTTL(method string) time.Duration {
	switch method {
	case v2.MethodAgentPing:
		return v2PingEventTTL
	case v2.MethodAgentRoute:
		return 2 * time.Minute
	default:
		return v2EventTTL
	}
}

func newV2EventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func coalesceV2EventLocked(q *v2EventQueue, event v2.Event) {
	key := v2EventCoalesceKey(event)
	if key == "" {
		return
	}
	filtered := q.events[:0]
	for _, existing := range q.events {
		if v2EventCoalesceKey(existing) != key {
			filtered = append(filtered, existing)
		}
	}
	q.events = filtered
}

func v2EventCoalesceKey(event v2.Event) string {
	if event.Method == v2.MethodAgentConfig {
		return v2.MethodAgentConfig
	}
	if event.Method == v2.MethodAgentFirewallApply || event.Method == v2.MethodAgentFirewallSnapshot || event.Method == v2.MethodAgentFirewallAdopt {
		return event.Method
	}
	if event.Method == v2.MethodAgentProtectionApplyConfig || event.Method == v2.MethodAgentProtectionPrecheck || event.Method == v2.MethodAgentProtectionUpgrade {
		return event.Method
	}
	if event.Method == v2.MethodAgentProtectionGroupSnapshot {
		switch p := event.Params.(type) {
		case v2.ProtectionGroupSnapshotParams:
			if strings.TrimSpace(p.Snapshot.Group) == "" {
				return ""
			}
			return event.Method + ":" + p.Snapshot.Group
		case *v2.ProtectionGroupSnapshotParams:
			if p == nil || strings.TrimSpace(p.Snapshot.Group) == "" {
				return ""
			}
			return event.Method + ":" + p.Snapshot.Group
		}
		var params v2.ProtectionGroupSnapshotParams
		if err := bindV2EventParams(event.Params, &params); err != nil || strings.TrimSpace(params.Snapshot.Group) == "" {
			return ""
		}
		return event.Method + ":" + params.Snapshot.Group
	}
	if event.Method != v2.MethodAgentPing && event.Method != v2.MethodAgentRoute {
		return ""
	}
	if event.Method == v2.MethodAgentRoute {
		switch p := event.Params.(type) {
		case v2.RouteParams:
			if p.TaskID == 0 {
				return ""
			}
			return fmt.Sprintf("%s:%d", event.Method, p.TaskID)
		case *v2.RouteParams:
			if p == nil || p.TaskID == 0 {
				return ""
			}
			return fmt.Sprintf("%s:%d", event.Method, p.TaskID)
		case map[string]any:
			if id, ok := anyToUint(p["task_id"]); ok && id != 0 {
				return fmt.Sprintf("%s:%d", event.Method, id)
			}
			return ""
		}
		var params v2.RouteParams
		if err := bindV2EventParams(event.Params, &params); err != nil || params.TaskID == 0 {
			return ""
		}
		return fmt.Sprintf("%s:%d", event.Method, params.TaskID)
	}
	switch p := event.Params.(type) {
	case v2.PingParams:
		if p.TaskID == 0 {
			return ""
		}
		return fmt.Sprintf("%s:%d", event.Method, p.TaskID)
	case *v2.PingParams:
		if p == nil || p.TaskID == 0 {
			return ""
		}
		return fmt.Sprintf("%s:%d", event.Method, p.TaskID)
	case map[string]any:
		if id, ok := anyToUint(p["ping_task_id"]); ok && id != 0 {
			return fmt.Sprintf("%s:%d", event.Method, id)
		}
		return ""
	}
	var params v2.PingParams
	if err := bindV2EventParams(event.Params, &params); err != nil || params.TaskID == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", event.Method, params.TaskID)
}

func anyToUint(v any) (uint, bool) {
	switch n := v.(type) {
	case uint:
		return n, true
	case uint64:
		return uint(n), true
	case uint32:
		return uint(n), true
	case int:
		if n < 0 {
			return 0, false
		}
		return uint(n), true
	case int64:
		if n < 0 {
			return 0, false
		}
		return uint(n), true
	case float64:
		if n < 0 {
			return 0, false
		}
		return uint(n), true
	default:
		return 0, false
	}
}

func bindV2EventParams(raw any, target any) error {
	if raw == nil {
		return json.Unmarshal([]byte("null"), target)
	}
	switch v := raw.(type) {
	case json.RawMessage:
		if len(v) == 0 {
			return json.Unmarshal([]byte("null"), target)
		}
		return json.Unmarshal(v, target)
	case []byte:
		if len(v) == 0 {
			return json.Unmarshal([]byte("null"), target)
		}
		return json.Unmarshal(v, target)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

func ackV2EventsLocked(q *v2EventQueue, ackIDs []string) {
	if len(ackIDs) == 0 || len(q.events) == 0 {
		return
	}
	acked := make(map[string]struct{}, len(ackIDs))
	for _, id := range ackIDs {
		acked[id] = struct{}{}
	}
	filtered := q.events[:0]
	for _, event := range q.events {
		if _, ok := acked[event.ID]; !ok {
			filtered = append(filtered, event)
		}
	}
	q.events = filtered
}

func pruneExpiredV2EventsLocked(q *v2EventQueue) {
	if len(q.events) == 0 {
		return
	}
	now := time.Now().UTC()
	filtered := q.events[:0]
	for _, event := range q.events {
		if event.ExpiresAt.IsZero() || event.ExpiresAt.After(now) {
			filtered = append(filtered, event)
		}
	}
	q.events = filtered
}

func SweepExpiredV2Events() {
	v2EventMu.Lock()
	defer v2EventMu.Unlock()
	for _, q := range v2EventQueues {
		if q == nil {
			continue
		}
		pruneExpiredV2EventsLocked(q)
	}
}

func TakeV2Events(uuid string, ackIDs []string, limit int) []v2.Event {
	events, _ := TakeV2EventsWithMore(uuid, ackIDs, limit)
	return events
}

// TakeV2EventsWithMore ACKs processed event IDs, prunes expired events,
// and returns up to limit events along with whether more events remain in the queue.
func TakeV2EventsWithMore(uuid string, ackIDs []string, limit int) ([]v2.Event, bool) {
	v2EventMu.Lock()
	defer v2EventMu.Unlock()
	q := v2EventQueues[uuid]
	if q == nil {
		return nil, false
	}
	ackV2EventsLocked(q, ackIDs)
	pruneExpiredV2EventsLocked(q)
	total := len(q.events)
	events := takeV2EventsLocked(q, limit)
	hasMore := limit > 0 && total > len(events)
	return events, hasMore
}

// HasPendingV2Events reports whether the node has any unexpired queued events.
func HasPendingV2Events(uuid string) bool {
	v2EventMu.Lock()
	defer v2EventMu.Unlock()
	q := v2EventQueues[uuid]
	if q == nil {
		return false
	}
	pruneExpiredV2EventsLocked(q)
	return len(q.events) > 0
}

func AckV2Events(uuid string, ackIDs []string) {
	if len(ackIDs) == 0 {
		return
	}
	v2EventMu.Lock()
	defer v2EventMu.Unlock()

	q := v2EventQueues[uuid]
	if q == nil {
		return
	}
	ackV2EventsLocked(q, ackIDs)
}

type RemovedV2Event struct {
	UUID  string
	Event v2.Event
}

func RemoveAllV2EventsByMethods(methods ...string) []RemovedV2Event {
	if len(methods) == 0 {
		return nil
	}
	blocked := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		blocked[method] = struct{}{}
	}
	v2EventMu.Lock()
	defer v2EventMu.Unlock()
	var removed []RemovedV2Event
	for uuid, q := range v2EventQueues {
		if q == nil {
			continue
		}
		filtered := q.events[:0]
		for _, event := range q.events {
			if _, remove := blocked[event.Method]; remove {
				removed = append(removed, RemovedV2Event{UUID: uuid, Event: event})
				continue
			}
			filtered = append(filtered, event)
		}
		q.events = filtered
	}
	return removed
}

func RemoveV2EventQueue(uuid string) {
	v2EventMu.Lock()
	delete(v2EventQueues, uuid)
	v2EventMu.Unlock()
}

func takeV2EventsLocked(q *v2EventQueue, limit int) []v2.Event {
	if limit <= 0 || limit > len(q.events) {
		limit = len(q.events)
	}
	for i := 0; i < limit; i++ {
		q.events[i].HandedOff = true
	}
	events := make([]v2.Event, limit)
	copy(events, q.events[:limit])
	return events
}
