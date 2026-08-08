package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyrka-meow/Regalia/internal/engine"
	"github.com/lyrka-meow/Regalia/internal/state"
	"github.com/lyrka-meow/Regalia/internal/subscription"
)

type fakeEngine struct {
	status        engine.Status
	configuration []byte
	starts        int
	stops         int
	startErr      error
	stopErr       error
}

func (f *fakeEngine) Status() engine.Status {
	return f.status
}

func (f *fakeEngine) Start(configuration []byte) error {
	f.starts++
	f.configuration = append([]byte(nil), configuration...)
	if f.startErr != nil {
		return f.startErr
	}
	f.status = engine.Status{State: engine.StateConnected, Available: true, PID: 4242}
	return nil
}

func (f *fakeEngine) Stop() error {
	f.stops++
	if f.stopErr != nil {
		return f.stopErr
	}
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
	if store.Snapshot().VPNEnabled {
		t.Fatal("incomplete configuration persisted the enabled state")
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
	if status["apiVersion"] != 5 || status["enabled"] != true || status["engine"] != engine.StateConnected || status["connected"] != true || status["enginePid"] != 4242 {
		t.Fatalf("unexpected connected status: %#v", status)
	}
	activeServer, ok := status["activeServer"].(serverView)
	if !ok || activeServer.ID != serverID || activeServer.Name != "Warsaw" {
		t.Fatalf("active server summary missing: %#v", status["activeServer"])
	}
	publicStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicStatus), "secret") {
		t.Fatal("status exposed server credentials")
	}
	if _, apiError := server.dispatch("vpn.connect", nil); apiError != nil {
		t.Fatalf("idempotent connect returned %#v", apiError)
	}
	if controller.starts != 1 {
		t.Fatalf("idempotent connect started engine %d times", controller.starts)
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
	if status["enabled"] != false || status["engine"] != engine.StateStopped || status["connected"] != false {
		t.Fatalf("unexpected disconnected status: %#v", status)
	}
}

func TestRestoreDesiredVPNState(t *testing.T) {
	store := configuredStore(t)
	if err := store.SetVPNEnabled(true); err != nil {
		t.Fatal(err)
	}
	controller := &fakeEngine{status: engine.Status{State: engine.StateStopped, Available: true}}
	server := NewWithEngine(filepath.Join(t.TempDir(), "regaliad.sock"), store, controller)
	server.restoreDesiredState()
	if controller.starts != 1 || controller.status.State != engine.StateConnected {
		t.Fatalf("VPN was not restored: %#v", controller)
	}
	if status := server.status(); status["enabled"] != true || status["restoreError"] != nil {
		t.Fatalf("unexpected restored status: %#v", status)
	}
}

func TestNetworkProxyTestRequiresConnectedVPN(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeEngine{status: engine.Status{State: engine.StateStopped, Available: true}}
	server := NewWithEngine(filepath.Join(t.TempDir(), "regaliad.sock"), store, controller)
	if _, apiError := server.dispatch("network.test.start", params(t, map[string]any{"mode": "proxy"})); apiError == nil || apiError.Code != "vpn_required" {
		t.Fatalf("proxy test returned %#v, want vpn_required", apiError)
	}
	if _, apiError := server.dispatch("network.test.start", params(t, map[string]any{"mode": "compare"})); apiError == nil || apiError.Code != "vpn_required" {
		t.Fatalf("compare test returned %#v, want vpn_required", apiError)
	}
}

func TestWaitForLoopbackListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := waitForLoopbackListener(context.Background(), port); err != nil {
		t.Fatalf("listener was not detected: %v", err)
	}
}

func TestRestoreFailureIsReportedWithoutClearingIntent(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetVPNEnabled(true); err != nil {
		t.Fatal(err)
	}
	controller := &fakeEngine{status: engine.Status{State: engine.StateStopped, Available: true}}
	server := NewWithEngine(filepath.Join(t.TempDir(), "regaliad.sock"), store, controller)
	server.restoreDesiredState()
	status := server.status()
	if status["enabled"] != true || status["restoreError"] == nil {
		t.Fatalf("restore error or desired state missing: %#v", status)
	}
	if controller.starts != 0 {
		t.Fatal("engine started with incomplete restored configuration")
	}
}

func TestFailedStartKeepsEnabledIntent(t *testing.T) {
	store := configuredStore(t)
	controller := &fakeEngine{
		status:   engine.Status{State: engine.StateStopped, Available: true},
		startErr: errors.New("TUN failed"),
	}
	server := NewWithEngine(filepath.Join(t.TempDir(), "regaliad.sock"), store, controller)
	if _, apiError := server.dispatch("vpn.setEnabled", params(t, map[string]bool{"enabled": true})); apiError == nil || apiError.Code != "engine_start_failed" {
		t.Fatalf("failed start returned %#v", apiError)
	}
	if !store.Snapshot().VPNEnabled {
		t.Fatal("failed engine start cleared the user's enabled intent")
	}
}

func TestRestoreDisabledStateStopsStaleEngine(t *testing.T) {
	store := configuredStore(t)
	controller := &fakeEngine{status: engine.Status{State: engine.StateConnected, Available: true}}
	server := NewWithEngine(filepath.Join(t.TempDir(), "regaliad.sock"), store, controller)
	server.restoreDesiredState()
	if controller.stops != 1 || controller.status.State != engine.StateStopped {
		t.Fatalf("stale engine was not stopped: %#v", controller)
	}
}

func TestSetEnabledRequiresBoolean(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(filepath.Join(t.TempDir(), "regaliad.sock"), store)
	if _, apiError := server.dispatch("vpn.setEnabled", params(t, map[string]any{})); apiError == nil || apiError.Code != "invalid_params" {
		t.Fatalf("missing enabled returned %#v", apiError)
	}
}

func TestContextShutdownStopsEngine(t *testing.T) {
	store := configuredStore(t)
	if err := store.SetVPNEnabled(true); err != nil {
		t.Fatal(err)
	}
	controller := &fakeEngine{status: engine.Status{State: engine.StateConnected, Available: true}}
	socketPath := filepath.Join(t.TempDir(), "regaliad.sock")
	server := NewWithEngine(socketPath, store, controller)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.ListenAndServeContext(ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon socket was not created")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if controller.stops != 1 {
		t.Fatalf("shutdown stopped engine %d times, want 1", controller.stops)
	}
}

func configuredStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile("Main", "https://example.invalid/sub")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReplaceServers(profile.ID, []subscription.Server{{
		Name:     "Warsaw",
		Protocol: "trojan",
		Address:  "vpn.example",
		Port:     443,
		Source:   "trojan://secret@vpn.example:443#Warsaw",
		Outbound: json.RawMessage(`{"type":"trojan","server":"vpn.example","server_port":443,"password":"secret"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectServer(updated.Servers[0].ID); err != nil {
		t.Fatal(err)
	}
	return store
}

func params(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
