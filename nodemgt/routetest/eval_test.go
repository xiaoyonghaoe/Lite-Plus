package routetest

import (
	"context"
	"testing"

	"github.com/nuomiiiii/lite/nodemgt/models"
	"github.com/nuomiiiii/lite/nodemgt/store"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestEvaluateVirtualInboundAndPriority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Inbound{}, &models.Outbound{}, &models.Route{}, &models.NodeUser{}, &models.Grant{}, &models.Setting{}); err != nil {
		t.Fatal(err)
	}
	st := store.New(db)

	inb := &models.Inbound{
		ID:           "inb-1",
		NodeUUID:     "node-1",
		Core:         models.CoreSingBox,
		Name:         "ppf",
		Protocol:     "anytls",
		Port:         443,
		Enabled:      true,
		SettingsJSON: `{"virtual_tags":["home"]}`,
	}
	if err := st.UpsertInbound(inb); err != nil {
		t.Fatal(err)
	}
	out := &models.Outbound{
		ID:       "out-home",
		NodeUUID: "node-1",
		Name:     "家宽",
		Protocol: "shadowsocks",
		Enabled:  true,
	}
	if err := st.UpsertOutbound(out); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRoute(&models.Route{
		ID:       "r-block-priv",
		NodeUUID: "node-1",
		Name:     "禁止内网",
		Priority: -10,
		Enabled:  true,
		RuleJSON: `{"outbound_id":"block","ip":"geoip:private"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRoute(&models.Route{
		ID:       "r-home",
		NodeUUID: "node-1",
		Name:     "家宽",
		Priority: 0,
		Enabled:  true,
		RuleJSON: `{"inbound_id":"inb-1#home","outbound_id":"out-home"}`,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Private IP blocked even on virtual inbound #home (priority -10 < 0)
	res1, err := Evaluate(context.Background(), st, Request{
		NodeUUID:  "node-1",
		InboundID: "inb-1#home",
		Target:    "192.168.1.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res1.Matched || res1.OutboundID != "block" || res1.MatchedRoute != "禁止内网" {
		t.Fatalf("expected block for private IP, got %+v", res1)
	}

	// 2. Public IP on virtual inbound #home matches 家宽
	res2, err := Evaluate(context.Background(), st, Request{
		NodeUUID:  "node-1",
		InboundID: "inb-1#home",
		Target:    "1.1.1.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Matched || res2.OutboundID != "out-home" || res2.OutboundName != "家宽" {
		t.Fatalf("expected out-home for virtual inbound, got %+v", res2)
	}

	// 3. Public IP on primary inbound inb-1 falls back to direct
	res3, err := Evaluate(context.Background(), st, Request{
		NodeUUID:  "node-1",
		InboundID: "inb-1",
		Target:    "1.1.1.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Matched || res3.OutboundID != "direct" {
		t.Fatalf("expected direct for primary inbound, got %+v", res3)
	}
}
