package daemon

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyrka-meow/Regalia/internal/engine"
	"github.com/lyrka-meow/Regalia/internal/state"
)

type fakeEngine struct {
	status        engine.Status
	configuration []byte
	starts        int
	stops         int
}

func (f *fakeEngine) Status() engine.Status {
	return f.status
}

func (f *fakeEngine) Start(configuration []byte) error {
	f.starts++
	f.configuration = append([]byte(nil), configuration...)
	f.status = engine.Status{State: engine.StateConnected, Available: true, PID: 4242}
	return nil
}

func (f *fakeEngine) Stop() error {
	f.stops++
	f.status = engine.Status{State: engine.StateStopped, Available: true}
	return nil
}

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

func TestVPNLifecycleAndConfigurationLock(t *testing.T) {
	subscriptionServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte("trojan://secret@vpn.example:443#Warsaw"))
	}))
	defer subscriptionServer.Close()

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeEngine{status: engine.Status{State: engine.StateStopped, Available: true}}
	server := NewWithEngine(filepath.Join(t.TempDir(), "regaliad.sock"), store, controller)

	if _, apiError := server.dispatch("vpn.connect", nil); apiError == nil || apiError.Code != "configuration_incomplete" {
		t.Fatalf("connect without a server returned %#v", apiError)
	}
	if controller.starts != 0 {
		t.Fatal("engine started with incomplete configuration")
	}

	profile, err := store.CreateProfile("Main", subscriptionServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, apiError := server.dispatch("profiles.refresh", params(t, map[string]string{"id": profile.ID})); apiError != nil {
		t.Fatal(apiError)
	}
	serverID := store.Snapshot().Profiles[0].Servers[0].ID
	if _, apiError := server.dispatch("servers.select", params(t, map[string]string{"id": serverID})); apiError != nil {
		t.Fatal(apiError)
	}

	result, apiError := server.dispatch("vpn.connect", nil)
	if apiError != nil {
		t.Fatal(apiError)
	}
	if controller.starts != 1 || !json.Valid(controller.configuration) || !strings.Contains(string(controller.configuration), `"type":"tun"`) {
		t.Fatalf("engine received invalid configuration: %s", controller.configuration)
	}
	status := result.(map[string]any)
	if status["apiVersion"] != 2 || status["engine"] != engine.StateConnected || status["connected"] != true || status["enginePid"] != 4242 {
		t.Fatalf("unexpected connected status: %#v", status)
	}
	if _, apiError := server.dispatch("servers.select", params(t, map[string]string{"id": serverID})); apiError == nil || apiError.Code != "vpn_active" {
		t.Fatalf("active configuration mutation returned %#v", apiError)
	}

	result, apiError = server.dispatch("vpn.disconnect", nil)
	if apiError != nil {
		t.Fatal(apiError)
	}
	if controller.stops != 1 {
		t.Fatalf("engine stop count is %d, want 1", controller.stops)
	}
	status = result.(map[string]any)
	if status["engine"] != engine.StateStopped || status["connected"] != false {
		t.Fatalf("unexpected disconnected status: %#v", status)
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
