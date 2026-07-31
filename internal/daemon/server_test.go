package daemon

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyrka-meow/Regalia/internal/state"
)

func TestSubscriptionRefreshAndServerSelection(t *testing.T) {
	subscriptionBody := base64.StdEncoding.EncodeToString([]byte(
		"trojan://secret@vpn.example:443#Warsaw\n" +
			"vless://uuid@vpn.example:8443#Tokyo",
	))
	subscriptionServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(subscriptionBody))
	}))
	defer subscriptionServer.Close()

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(filepath.Join(t.TempDir(), "regaliad.sock"), store)
	created, apiError := server.dispatch("profiles.create", params(t, map[string]string{
		"name":            "Main",
		"subscriptionUrl": subscriptionServer.URL,
	}))
	if apiError != nil {
		t.Fatal(apiError)
	}
	profile := created.(profileView)

	result, apiError := server.dispatch("profiles.refresh", params(t, map[string]string{"id": profile.ID}))
	if apiError != nil {
		t.Fatal(apiError)
	}
	refreshed := result.(map[string]any)
	if refreshed["serverCount"] != 2 {
		t.Fatalf("got server count %#v, want 2", refreshed["serverCount"])
	}
	snapshot := store.Snapshot()
	if len(snapshot.Profiles[0].Servers) != 2 {
		t.Fatalf("got %d stored servers, want 2", len(snapshot.Profiles[0].Servers))
	}
	publicServers, apiError := server.dispatch("servers.list", nil)
	if apiError != nil {
		t.Fatal(apiError)
	}
	publicJSON, err := json.Marshal(publicServers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), "secret") || strings.Contains(string(publicJSON), "uuid@") {
		t.Fatal("public server list exposed connection credentials")
	}

	selectedID := snapshot.Profiles[0].Servers[1].ID
	if _, apiError := server.dispatch("servers.select", params(t, map[string]string{"id": selectedID})); apiError != nil {
		t.Fatal(apiError)
	}
	if store.Snapshot().ActiveServerID != selectedID {
		t.Fatal("selected server was not persisted")
	}
}

func TestFailedRefreshKeepsOldServers(t *testing.T) {
	responseBody := "trojan://secret@vpn.example:443#Warsaw"
	subscriptionServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(responseBody))
	}))
	defer subscriptionServer.Close()

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(filepath.Join(t.TempDir(), "regaliad.sock"), store)
	profile, err := store.CreateProfile("Main", subscriptionServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, apiError := server.dispatch("profiles.refresh", params(t, map[string]string{"id": profile.ID})); apiError != nil {
		t.Fatal(apiError)
	}

	responseBody = "broken subscription"
	if _, apiError := server.dispatch("profiles.refresh", params(t, map[string]string{"id": profile.ID})); apiError == nil {
		t.Fatal("expected refresh error")
	}
	snapshot := store.Snapshot()
	if len(snapshot.Profiles[0].Servers) != 1 {
		t.Fatal("failed refresh removed the previous servers")
	}
	if snapshot.Profiles[0].LastError == "" {
		t.Fatal("failed refresh was not recorded")
	}
}

func params(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
