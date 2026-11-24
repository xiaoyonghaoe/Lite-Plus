package agent

import (
	"sync"
	"time"

	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

var (
	realityProbeMu      sync.Mutex
	realityProbeWaiters = map[string]chan v2.RealityProbeResultParams{}
)

func StartRealityProbeWait(requestID string) {
	realityProbeMu.Lock()
	realityProbeWaiters[requestID] = make(chan v2.RealityProbeResultParams, 1)
	realityProbeMu.Unlock()
}

func StopRealityProbeWait(requestID string) {
	realityProbeMu.Lock()
	delete(realityProbeWaiters, requestID)
	realityProbeMu.Unlock()
}

func WaitRealityProbe(requestID string, timeout time.Duration) (v2.RealityProbeResultParams, bool) {
	realityProbeMu.Lock()
	ch := realityProbeWaiters[requestID]
	realityProbeMu.Unlock()
	if ch == nil {
		return v2.RealityProbeResultParams{}, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res, true
	case <-timer.C:
		return v2.RealityProbeResultParams{}, false
	}
}

func CompleteRealityProbe(result v2.RealityProbeResultParams) {
	if result.RequestID == "" {
		return
	}
	realityProbeMu.Lock()
	ch := realityProbeWaiters[result.RequestID]
	realityProbeMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- result:
	default:
	}
}
