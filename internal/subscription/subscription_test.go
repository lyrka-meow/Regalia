package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseBase64LinkList(t *testing.T) {
	input := strings.Join([]string{
		"trojan://secret@example.com:443#Warsaw",
		"vless://uuid@example.net:8443?security=reality#Tokyo",
	}, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(input))
	servers, err := Parse([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("got %d servers, want 2", len(servers))
	}
	if servers[0].Name != "Warsaw" || servers[0].Protocol != "trojan" {
		t.Fatalf("unexpected first server: %#v", servers[0])
	}
	if servers[1].Address != "example.net" || servers[1].Port != 8443 {
		t.Fatalf("unexpected second server: %#v", servers[1])
	}
}

func TestVLESSRealityOutboundPreservesConnectionFields(t *testing.T) {
	servers, err := Parse([]byte(
		"vless://example-uuid@vpn.example:443?security=reality&sni=cdn.example&fp=chrome&pbk=public-key&sid=abcd&type=ws&host=edge.example&path=%2Fws#Tokyo",
	))
	if err != nil {
		t.Fatal(err)
	}
	var outbound map[string]any
	if err := json.Unmarshal(servers[0].Outbound, &outbound); err != nil {
		t.Fatal(err)
	}
	if outbound["type"] != "vless" || outbound["uuid"] != "example-uuid" {
		t.Fatalf("unexpected outbound: %#v", outbound)
	}
	tls := outbound["tls"].(map[string]any)
	if tls["server_name"] != "cdn.example" {
		t.Fatalf("unexpected TLS: %#v", tls)
	}
	reality := tls["reality"].(map[string]any)
	if reality["public_key"] != "public-key" || reality["short_id"] != "abcd" {
		t.Fatalf("unexpected Reality settings: %#v", reality)
	}
	transport := outbound["transport"].(map[string]any)
	if transport["type"] != "ws" || transport["path"] != "/ws" {
		t.Fatalf("unexpected transport: %#v", transport)
	}
}

func TestParseVMess(t *testing.T) {
	payload := `{"v":"2","ps":"Amsterdam","add":"vpn.example","port":"443","id":"uuid"}`
	link := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(payload))
	servers, err := Parse([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "Amsterdam" {
		t.Fatalf("unexpected servers: %#v", servers)
	}
}

func TestParseSingBoxJSON(t *testing.T) {
	input := `{
		"outbounds": [
			{"type":"direct","tag":"direct"},
			{"type":"trojan","tag":"Main","server":"vpn.example","server_port":443}
		]
	}`
	servers, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Protocol != "trojan" {
		t.Fatalf("unexpected servers: %#v", servers)
	}
	if len(servers[0].Outbound) == 0 {
		t.Fatal("sing-box outbound was not preserved")
	}
}

func TestInvalidInputDoesNotProduceServers(t *testing.T) {
	if _, err := Parse([]byte("hello, world")); err == nil {
		t.Fatal("expected parse error")
	}
}
