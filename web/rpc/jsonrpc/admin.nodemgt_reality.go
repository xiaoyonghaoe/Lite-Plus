package jsonrpc

import (
	"context"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/pkg/rpc"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

func init() {
	reg("nodemgt.reality.probe", nodemgtRealityProbe, "Probe Reality handshake target from the selected Agent node")
}

func nodemgtRealityProbe(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var p struct {
		NodeUUID   string `json:"node_uuid"`
		Dest       string `json:"dest"`
		ServerName string `json:"server_name"`
	}
	if err := req.BindParams(&p); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	nodeUUID := strings.TrimSpace(p.NodeUUID)
	dest := strings.TrimSpace(p.Dest)
	sni := strings.TrimSpace(p.ServerName)
	if nodeUUID == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "请先选择节点", nil)
	}
	if dest == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "请先填写 Reality 目标地址", nil)
	}
	if !agent_runtime.IsAgentOnline(nodeUUID) {
		return nil, rpc.MakeError(rpc.InternalError, "目标探针节点当前不在线，无法执行探测", nil)
	}

	requestID := utils.GenerateRandomString(16)
	if requestID == "" {
		requestID = time.Now().UTC().Format("150405.000000")
	}
	agent_runtime.StartRealityProbeWait(requestID)
	defer agent_runtime.StopRealityProbeWait(requestID)

	agent_runtime.TouchAdminActivity()
	agent_runtime.EnqueueV2Event(nodeUUID, v2.MethodNodemgtRealityProbe, v2.RealityProbeParams{
		RequestID:  requestID,
		Dest:       dest,
		ServerName: sni,
	})

	res, ok := agent_runtime.WaitRealityProbe(requestID, 15*time.Second)
	if !ok {
		return nil, rpc.MakeError(rpc.InternalError, "等待探针返回探测结果超时（请确认探针已更新到最新版本）", nil)
	}
	return res, nil
}
