package engineconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lyrka-meow/Regalia/internal/config"
	"github.com/lyrka-meow/Regalia/internal/state"
)

func TestValidateGeneratedConfiguration(t *testing.T) {
	result, err := config.Build(validState())
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(result.JSON); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGeneratedEndpointConfiguration(t *testing.T) {
	snapshot := validState()
	snapshot.Profiles[0].Servers[0].Protocol = "wireguard"
	snapshot.Profiles[0].Servers[0].Outbound = json.RawMessage(`{
		"type":"wireguard",
		"address":["10.0.0.2/32"],
		"private_key":"private",
		"peers":[{"address":"vpn.example","port":51820,"public_key":"public"}]
	}`)
	result, err := config.Build(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(result.JSON); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsAdditionalInbound(t *testing.T) {
	configuration := generatedDocument(t)
	inbounds := configuration["inbounds"].([]any)
	configuration["inbounds"] = append(inbounds, map[string]any{
		"type":        "mixed",
		"listen":      "0.0.0.0",
		"listen_port": 1080,
	})
	assertRejected(t, configuration, "unexpected inbound tag")
}

func TestValidateGeneratedNetcheckConfiguration(t *testing.T) {
	result, err := config.BuildWithOptions(validState(), config.NetcheckOptions{
		Port: 43123, DirectUser: "direct", DirectPassword: "direct-secret",
		ProxyUser: "proxy", ProxyPassword: "proxy-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(result.JSON); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsPrivilegedTopLevelFeatures(t *testing.T) {
	configuration := generatedDocument(t)
	configuration["experimental"] = map[string]any{"cache_file": map[string]any{"enabled": true}}
	assertRejected(t, configuration, "experimental")
}

func TestRejectsNonApplicationRouteRule(t *testing.T) {
	configuration := generatedDocument(t)
	route := configuration["route"].(map[string]any)
	route["rules"] = append(route["rules"].([]any), map[string]any{
		"ip_cidr":  []any{"0.0.0.0/0"},
		"action":   "route",
		"outbound": "direct",
	})
	assertRejected(t, configuration, "ip_cidr")
}

func generatedDocument(t *testing.T) map[string]any {
	t.Helper()
	result, err := config.Build(validState())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(result.JSON, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func assertRejected(t *testing.T, document map[string]any, expected string) {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(raw)
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Fatalf("validation returned %v, want error containing %q", err, expected)
	}
}

func validState() state.State {
	return state.State{
		SchemaVersion:  state.SchemaVersion,
		ActiveServerID: "server-1",
		Profiles: []state.SubscriptionProfile{{
			ID: "profile-1",
			Servers: []state.Server{{
				ID:       "server-1",
				Name:     "Warsaw",
				Protocol: "trojan",
				Outbound: json.RawMessage(`{
					"type":"trojan",
					"server":"vpn.example",
					"server_port":443,
					"password":"secret",
					"tls":{"enabled":true,"server_name":"vpn.example"}
				}`),
			}},
		}},
	}
}
