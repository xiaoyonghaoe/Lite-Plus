package client

import (
	"sync"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils/notifier"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

var presenceTestDBOnce sync.Once

func ensurePresenceTestDB(t *testing.T) {
	t.Helper()
	presenceTestDBOnce.Do(func() {
		if flags.DatabaseType == "" {
			flags.DatabaseType = "sqlite"
		}
		if flags.DatabaseFile == "" {
			flags.DatabaseFile = "file:v2_presence_test?mode=memory&cache=shared"
		}
		_ = dbcore.GetDBInstance()
	})
}

func cleanupPresenceForTest(uuid string) {
	postPresenceMu.Lock()
	if entry, ok := postPresenceStates[uuid]; ok {
		entry.generation++
		entry.timer.Stop()
		delete(postPresenceStates, uuid)
	}
	postPresenceMu.Unlock()
	agent_runtime.DeleteConnectedClients(uuid)
	notifier.ForgetClient(uuid)
}

func TestDynamicHTTPPresenceTTLAndExpiry(t *testing.T) {
	ensurePresenceTestDB(t)
	uuid := "node-http-dynamic"
	t.Cleanup(func() { cleanupPresenceForTest(uuid) })

	refreshPostPresenceWithTTL(uuid, 55*time.Second)
	if !agent_runtime.IsPresent(uuid) {
		t.Fatal("node should be present after refreshPostPresenceWithTTL")
	}
	if httpSessionGoneShouldNotify(uuid) {
		t.Fatal("active node should not notify offline")
	}

	postPresenceMu.Lock()
	entry := postPresenceStates[uuid]
	firstGen := entry.generation
	firstConnID := entry.connID
	postPresenceMu.Unlock()

	// Refreshing again bumps generation; stale expiry callback must be ignored.
	refreshPostPresenceWithTTL(uuid, 55*time.Second)
	postPresenceExpired(uuid, firstConnID, firstGen)
	if !agent_runtime.IsPresent(uuid) {
		t.Fatal("stale generation callback must not clear active presence")
	}

	postPresenceMu.Lock()
	curGen := postPresenceStates[uuid].generation
	postPresenceMu.Unlock()

	// Current generation expiry clears presence.
	postPresenceExpired(uuid, firstConnID, curGen)
	if agent_runtime.IsPresent(uuid) {
		t.Fatal("expired HTTP presence must clear online state")
	}
	if !httpSessionGoneShouldNotify(uuid) {
		t.Fatal("expired node should notify offline")
	}
}

func TestAgentPullMethodIsRemoved(t *testing.T) {
	ensurePresenceTestDB(t)
	uuid := "node-pull-removed"
	t.Cleanup(func() { cleanupPresenceForTest(uuid) })

	resp := handleV2RPCWithPresence(uuid, v2.Request{
		JSONRPC: v2.Version,
		ID:      "pull-1",
		Method:  v2.MethodAgentPull,
	}, false, true)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("expected -32601 method not found for agent.pull, got %+v", resp)
	}
}
