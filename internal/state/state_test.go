package state

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lyrka-meow/Regalia/internal/subscription"
)

func TestRouteRuleReplacesSameProcessPath(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.CreateRoute("Default", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	rule := AppRule{ProcessPath: "/usr/bin/chromium", Outbound: "direct"}
	if err := store.SetAppRule(route.ID, rule); err != nil {
		t.Fatal(err)
	}
	rule.Outbound = "proxy"
	if err := store.SetAppRule(route.ID, rule); err != nil {
		t.Fatal(err)
	}

	apps := store.Snapshot().RouteProfiles[0].Apps
	if len(apps) != 1 {
		t.Fatalf("got %d rules, want 1", len(apps))
	}
	if apps[0].Outbound != "proxy" {
		t.Fatalf("got outbound %q, want proxy", apps[0].Outbound)
	}
}

func TestRouteRuleReplacesSameDesktopApplication(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.CreateRoute("Default", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppRule(route.ID, AppRule{DesktopID: "chromium.desktop", ProcessPath: "/usr/bin/chromium", Outbound: "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppRule(route.ID, AppRule{DesktopID: "chromium.desktop", ProcessPath: "/usr/lib/chromium/chromium", Outbound: "direct"}); err != nil {
		t.Fatal(err)
	}
	apps := store.Snapshot().RouteProfiles[0].Apps
	if len(apps) != 1 || apps[0].ProcessPath != "/usr/lib/chromium/chromium" {
		t.Fatalf("unexpected rules: %#v", apps)
	}
}

func TestStatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProfile("Main", "https://example.invalid/sub"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Snapshot().Profiles); got != 1 {
		t.Fatalf("got %d profiles, want 1", got)
	}
}

func TestVPNEnabledPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetVPNEnabled(true); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Snapshot().VPNEnabled {
		t.Fatal("enabled VPN state was not persisted")
	}
	if err := reopened.SetVPNEnabled(false); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Snapshot().VPNEnabled {
		t.Fatal("disabled VPN state was not persisted")
	}
}

func TestReplaceServersKeepsStableIDsAndSelection(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", "https://example.invalid/sub")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []subscription.Server{{
		Name:     "Warsaw",
		Protocol: "trojan",
		Address:  "vpn.example",
		Port:     443,
		Source:   "trojan://secret@vpn.example:443#Warsaw",
		Outbound: json.RawMessage(`{"type":"trojan","server":"vpn.example","server_port":443,"password":"secret"}`),
	}}
	first, err := store.ReplaceServers(profile.ID, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(first.Servers))
	}
	if err := store.SelectServer(first.Servers[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := store.ReplaceServers(profile.ID, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if second.Servers[0].ID != first.Servers[0].ID {
		t.Fatalf("server ID changed: %q != %q", second.Servers[0].ID, first.Servers[0].ID)
	}
	if store.Snapshot().ActiveServerID != first.Servers[0].ID {
		t.Fatal("active server selection was not preserved")
	}
}
