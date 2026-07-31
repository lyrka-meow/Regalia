package config

import (
	"encoding/json"
	"testing"

	"github.com/lyrka-meow/Regalia/internal/state"
)

func TestBuildUsesSelectedServerAndProcessPaths(t *testing.T) {
	outbound := json.RawMessage(`{
		"type":"trojan",
		"server":"vpn.example",
		"server_port":443,
		"password":"secret",
		"tls":{"enabled":true,"server_name":"vpn.example"}
	}`)
	snapshot := state.State{
		SchemaVersion:  state.SchemaVersion,
		ActiveServerID: "server-1",
		ActiveRouteID:  "route-1",
		Profiles: []state.SubscriptionProfile{{
			ID: "profile-1",
			Servers: []state.Server{{
				ID:       "server-1",
				Name:     "Warsaw",
				Protocol: "trojan",
				Outbound: outbound,
			}},
		}},
		RouteProfiles: []state.RouteProfile{{
			ID:              "route-1",
			DefaultOutbound: "proxy",
			Apps: []state.AppRule{{
				ProcessPath: "/usr/bin/chromium",
				Outbound:    "direct",
			}},
		}},
	}
	result, err := Build(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(result.JSON, &document); err != nil {
		t.Fatal(err)
	}
	outbounds := document["outbounds"].([]any)
	proxy := outbounds[0].(map[string]any)
	if proxy["tag"] != "proxy" || proxy["password"] != "secret" {
		t.Fatalf("unexpected proxy outbound: %#v", proxy)
	}
	route := document["route"].(map[string]any)
	rules := route["rules"].([]any)
	directRule := rules[2].(map[string]any)
	paths := directRule["process_path"].([]any)
	if len(paths) != 1 || paths[0] != "/usr/bin/chromium" {
		t.Fatalf("unexpected process paths: %#v", paths)
	}
}

func TestBuildRequiresSelection(t *testing.T) {
	if _, err := Build(state.State{}); err == nil {
		t.Fatal("expected missing-server error")
	}
}
