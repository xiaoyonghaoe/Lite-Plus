package jsonrpc

import (
	"os"
	"strings"
	"testing"
)

func TestInboundFirewallMutationsValidateBeforeSave(t *testing.T) {
	src, err := os.ReadFile("admin.nodemgt.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	upsert := extractGoFunc(text, "func nodemgtInboundUpsert")
	if !strings.Contains(upsert, "firewall.EnsureApplyAllowed(") {
		t.Fatal("inbound upsert does not check firewall availability before linking")
	}
	if !strings.Contains(upsert, "firewall.PlannedInboundFirewallOp(") {
		t.Fatal("inbound upsert always mutates firewall")
	}
	check := strings.Index(upsert, "firewall.CheckInboundHostStack(")
	save := strings.Index(upsert, "UpsertInbound(")
	if check < 0 || save < 0 || check > save {
		t.Fatal("inbound upsert must reject a missing host stack before saving")
	}
}

func TestSaveFirewallValidatesAndRevertsSSH(t *testing.T) {
	src, err := os.ReadFile("admin.firewall.go")
	if err != nil {
		t.Fatal(err)
	}
	save := extractGoFunc(string(src), "func adminSaveFirewall")
	guard := strings.Index(save, "firewall.GuardDefaultDeny(")
	ensure := strings.Index(save, "firewall.EnsureApplyAllowed(")
	persist := strings.Index(save, "fwstore.SaveIntent(")
	if ensure < 0 || guard < 0 || persist < 0 || ensure > persist || guard > persist {
		t.Fatal("save must check capability and default deny before persisting")
	}
	if !strings.Contains(save, "RevertSshPort(") || !strings.Contains(save, "prevSSH") {
		t.Fatal("failed ssh listen change must revert the saved ssh_port")
	}
	if !strings.Contains(save, "OmitKeep:") || !strings.Contains(string(src), `json:"omit_keep"`) {
		t.Fatal("save must persist omitted keep holes")
	}
	if !strings.Contains(string(src), "DropOmittedKeep(") {
		t.Fatal("view must not reattach omitted keep holes")
	}
	if !strings.Contains(save, "DropOrphanInboundRules(") {
		t.Fatal("save must drop inbound-origin rules whose inbound is gone")
	}
	get := extractGoFunc(string(src), "func adminGetFirewall")
	if !strings.Contains(get, "reconcileOrphanInbound(") {
		t.Fatal("get must drop leftover inbound-origin rules after the inbound was deleted")
	}
	reconcile := extractGoFunc(string(src), "func reconcileOrphanInbound")
	if strings.Contains(reconcile, "DispatchApply(") {
		t.Fatal("get must not apply cleaned inbound rules directly")
	}
	if !strings.Contains(reconcile, "SaveIntent(") {
		t.Fatal("get must persist cleaned inbound-origin rules locally")
	}
	view := extractGoFunc(string(src), "func firewallView")
	if !strings.Contains(view, "firewall.ReportedStacks(client.IPv4, client.IPv6)") {
		t.Fatal("firewall view stacks must come from the reported node addresses")
	}
	if !strings.Contains(view, "cloned.StackV4 = stackV4") || !strings.Contains(view, "cloned.StackV6 = stackV6") {
		t.Fatal("stored snapshots must not keep agent-scanned stacks over the reported addresses")
	}
	adopt := extractGoFunc(string(src), "func adminAdoptFirewall")
	if !strings.Contains(adopt, `disableOriginal = "timeout"`) || !strings.Contains(adopt, `view["disable_original"]`) {
		t.Fatal("adopt must keep the applied Plus rules when disable-original times out")
	}
	if strings.Index(adopt, "DispatchApply(") < 0 || strings.Index(adopt, "DispatchAdopt(") < strings.Index(adopt, "DispatchApply(") {
		t.Fatal("adopt must apply Plus rules before disable-original")
	}
}

func extractGoFunc(src, signature string) string {
	start := strings.Index(src, signature)
	if start < 0 {
		return ""
	}
	rest := src[start+len(signature):]
	next := strings.Index(rest, "\nfunc ")
	if next < 0 {
		return src[start:]
	}
	return src[start : start+len(signature)+next]
}
