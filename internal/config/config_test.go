package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lyrka-meow/Regalia/internal/state"
)

func TestBuildUsesSelectedServerAndProcessPaths(t *testing.T) {
	snapshot := readySnapshot()
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

func TestGeneratedConfigurationPassesSingBoxCheck(t *testing.T) {
	binary := os.Getenv("REGALIA_SING_BOX")
	if binary == "" {
		t.Skip("REGALIA_SING_BOX is not set")
	}
	result, err := Build(readySnapshot())
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, result.JSON, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "check", "--disable-color", "-c", configPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sing-box check failed: %v\n%s", err, output)
	}
}

func readySnapshot() state.State {
	outbound := json.RawMessage(`{
		"type":"trojan",
		"server":"vpn.example",
		"server_port":443,
		"password":"secret",
		"tls":{"enabled":true,"server_name":"vpn.example"}
	}`)
	return state.State{
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
}

func TestBuildRequiresSelection(t *testing.T) {
	if _, err := Build(state.State{}); err == nil {
		t.Fatal("expected missing-server error")
	}
}
