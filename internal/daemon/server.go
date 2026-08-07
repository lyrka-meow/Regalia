package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lyrka-meow/Regalia/internal/apps"
	"github.com/lyrka-meow/Regalia/internal/config"
	"github.com/lyrka-meow/Regalia/internal/engine"
	"github.com/lyrka-meow/Regalia/internal/netcheck"
	"github.com/lyrka-meow/Regalia/internal/paths"
	"github.com/lyrka-meow/Regalia/internal/protocol"
	"github.com/lyrka-meow/Regalia/internal/state"
	"github.com/lyrka-meow/Regalia/internal/subscription"
)

type Server struct {
	socketPath      string
	store           *state.Store
	fetcher         *subscription.Fetcher
	engine          engine.Controller
	vpnMu           sync.Mutex
	restoreMu       sync.RWMutex
	restoreErr      string
	netcheckOptions config.NetcheckOptions
	netcheckHistory *netcheck.History
	netcheckMu      sync.Mutex
	netcheckJob     *networkTestJob
}

type networkTestJob struct {
	ID        string           `json:"id"`
	State     string           `json:"state"`
	Mode      string           `json:"mode"`
	Phase     string           `json:"phase"`
	Percent   int              `json:"percent"`
	StartedAt string           `json:"startedAt"`
	Error     string           `json:"error,omitempty"`
	Result    *netcheck.Result `json:"result,omitempty"`
	cancel    context.CancelFunc
}

type profileView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	ServerCount int    `json:"serverCount"`
}

type serverView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Address  string `json:"address,omitempty"`
	Port     int    `json:"port,omitempty"`
	Ready    bool   `json:"ready"`
}

type routeSummaryView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DefaultOutbound string `json:"defaultOutbound"`
}

var errVPNConfigurationIncomplete = errors.New("VPN configuration is incomplete")

func New(socketPath string, store *state.Store) *Server {
	return NewWithEngine(socketPath, store, engine.NewUnavailable("VPN engine controller is not configured"))
}

func NewWithEngine(socketPath string, store *state.Store, controller engine.Controller) *Server {
	if controller == nil {
		controller = engine.NewUnavailable("VPN engine controller is not configured")
	}
	historyPath, err := paths.NetcheckHistory()
	if err != nil {
		historyPath = filepath.Join(paths.RuntimeDirectory(), "netchecks.json")
	}
	return &Server{
		socketPath:      socketPath,
		store:           store,
		fetcher:         subscription.NewFetcher(),
		engine:          controller,
		netcheckOptions: newNetcheckOptions(),
		netcheckHistory: netcheck.NewHistory(historyPath),
	}
}

func (s *Server) ListenAndServe() error {
	return s.ListenAndServeContext(context.Background())
}

func (s *Server) ListenAndServeContext(ctx context.Context) error {
	defer func() {
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if engine.Active(s.engine.Status()) {
			_ = s.engine.Stop()
		}
	}()
	socketDirectory := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(socketDirectory, 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Chmod(socketDirectory, 0o700); err != nil {
		return fmt.Errorf("protect socket directory: %w", err)
	}
	if err := removeStaleSocket(s.socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}
	defer listener.Close()
	defer os.Remove(s.socketPath)
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		return fmt.Errorf("protect socket: %w", err)
	}
	stopClosing := context.AfterFunc(ctx, func() {
		_ = listener.Close()
	})
	defer stopClosing()
	s.restoreDesiredState()

	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConnection(connection)
	}
}

func (s *Server) serveConnection(connection net.Conn) {
	defer connection.Close()
	scanner := bufio.NewScanner(connection)
	encoder := json.NewEncoder(connection)
	for scanner.Scan() {
		var request protocol.Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			_ = encoder.Encode(protocol.Response{
				Error: &protocol.Error{Code: "invalid_json", Message: err.Error()},
			})
			continue
		}
		_ = encoder.Encode(s.handle(request))
	}
}

func (s *Server) handle(request protocol.Request) protocol.Response {
	result, apiError := s.dispatch(request.Method, request.Params)
	return protocol.Response{ID: request.ID, Result: result, Error: apiError}
}

func (s *Server) dispatch(method string, raw json.RawMessage) (any, *protocol.Error) {
	switch method {
	case "status":
		return s.status(), nil
	case "vpn.connect":
		return s.setVPNEnabled(true)
	case "vpn.disconnect":
		return s.setVPNEnabled(false)
	case "vpn.setEnabled":
		var params struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		if params.Enabled == nil {
			return nil, invalidParams(errors.New("enabled is required"))
		}
		return s.setVPNEnabled(*params.Enabled)
	case "apps.list":
		return apps.List(), nil
	case "apps.processes":
		return apps.Processes(), nil
	case "profiles.list":
		profiles := s.store.Snapshot().Profiles
		result := make([]profileView, 0, len(profiles))
		for _, profile := range profiles {
			result = append(result, publicProfile(profile))
		}
		return result, nil
	case "profiles.create":
		var params struct {
			Name            string `json:"name"`
			SubscriptionURL string `json:"subscriptionUrl"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		profile, err := s.store.CreateProfile(params.Name, params.SubscriptionURL)
		if err != nil {
			return nil, invalidParams(err)
		}
		return publicProfile(profile), nil
	case "profiles.delete":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			ID string `json:"id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return emptyOrError(s.store.DeleteProfile(params.ID))
	case "profiles.refresh":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			ID string `json:"id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		profile, err := s.store.Profile(params.ID)
		if err != nil {
			return nil, invalidParams(err)
		}
		servers, err := s.fetcher.Fetch(context.Background(), profile.SubscriptionURL)
		if err != nil {
			_ = s.store.RecordProfileError(profile.ID, err)
			return nil, &protocol.Error{Code: "subscription_failed", Message: err.Error()}
		}
		updated, err := s.store.ReplaceServers(profile.ID, servers)
		if err != nil {
			return nil, &protocol.Error{Code: "state_failed", Message: err.Error()}
		}
		return map[string]any{
			"profile":     publicProfile(updated),
			"serverCount": len(updated.Servers),
		}, nil
	case "servers.list":
		snapshot := s.store.Snapshot()
		type profileServers struct {
			ProfileID   string       `json:"profileId"`
			ProfileName string       `json:"profileName"`
			Items       []serverView `json:"items"`
		}
		groups := make([]profileServers, 0, len(snapshot.Profiles))
		for _, profile := range snapshot.Profiles {
			items := make([]serverView, 0, len(profile.Servers))
			for _, server := range profile.Servers {
				items = append(items, publicServer(server))
			}
			groups = append(groups, profileServers{
				ProfileID:   profile.ID,
				ProfileName: profile.Name,
				Items:       items,
			})
		}
		return map[string]any{
			"activeServerId": snapshot.ActiveServerID,
			"profiles":       groups,
		}, nil
	case "servers.select":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			ID string `json:"id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		server, err := s.store.Server(params.ID)
		if err != nil {
			return nil, invalidParams(err)
		}
		if err := config.ValidateServer(server); err != nil {
			return nil, &protocol.Error{Code: "server_not_ready", Message: err.Error()}
		}
		return emptyOrError(s.store.SelectServer(params.ID))
	case "routes.list":
		snapshot := s.store.Snapshot()
		return map[string]any{
			"activeRouteId": snapshot.ActiveRouteID,
			"items":         snapshot.RouteProfiles,
		}, nil
	case "routes.create":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			Name            string `json:"name"`
			DefaultOutbound string `json:"defaultOutbound"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		route, err := s.store.CreateRoute(params.Name, params.DefaultOutbound)
		return resultOrError(route, err)
	case "routes.delete":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			ID string `json:"id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return emptyOrError(s.store.DeleteRoute(params.ID))
	case "routes.activate":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			ID string `json:"id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return emptyOrError(s.store.ActivateRoute(params.ID))
	case "routes.app.set":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			RouteID string        `json:"routeId"`
			App     state.AppRule `json:"app"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return emptyOrError(s.store.SetAppRule(params.RouteID, params.App))
	case "routes.app.remove":
		s.vpnMu.Lock()
		defer s.vpnMu.Unlock()
		if apiError := s.requireStopped(); apiError != nil {
			return nil, apiError
		}
		var params struct {
			RouteID     string `json:"routeId"`
			ProcessPath string `json:"processPath"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return emptyOrError(s.store.RemoveAppRule(params.RouteID, params.ProcessPath))
	case "network.test.start":
		var params struct {
			Mode    string                  `json:"mode"`
			Network netcheck.NetworkContext `json:"network"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return s.startNetworkTest(params.Mode, params.Network)
	case "network.test.status":
		var params struct {
			ID string `json:"id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return s.networkTestStatus(params.ID)
	case "network.test.cancel":
		var params struct {
			ID string `json:"id"`
		}
		if err := decodeParams(raw, &params); err != nil {
			return nil, invalidParams(err)
		}
		return s.cancelNetworkTest(params.ID)
	case "network.test.history":
		results, err := s.netcheckHistory.List()
		if err != nil {
			return nil, &protocol.Error{Code: "history_failed", Message: err.Error()}
		}
		return map[string]any{"items": results, "maxItems": 20, "maxAgeDays": 30}, nil
	case "network.test.history.clear":
		if err := s.netcheckHistory.Clear(); err != nil {
			return nil, &protocol.Error{Code: "history_failed", Message: err.Error()}
		}
		return map[string]bool{"ok": true}, nil
	default:
		return nil, &protocol.Error{Code: "method_not_found", Message: "unknown method: " + method}
	}
}

func (s *Server) status() map[string]any {
	snapshot := s.store.Snapshot()
	engineStatus := s.engine.Status()
	connected := engineStatus.State == engine.StateConnected
	status := map[string]any{
		"apiVersion": protocol.Version,
		"capabilities": []string{
			"vpn.toggle",
			"subscriptions",
			"servers",
			"routes.processPath",
			"apps.desktop",
			"apps.processes",
			"network.test",
			"network.test.compare",
			"network.test.history",
		},
		"enabled":         snapshot.VPNEnabled,
		"engine":          engineStatus.State,
		"engineAvailable": engineStatus.Available,
		"connected":       connected,
		"tun":             connected,
		"activeRouteId":   snapshot.ActiveRouteID,
		"activeServerId":  snapshot.ActiveServerID,
		"socket":          s.socketPath,
	}
	for _, profile := range snapshot.Profiles {
		for _, server := range profile.Servers {
			if server.ID == snapshot.ActiveServerID {
				status["activeServer"] = publicServer(server)
				break
			}
		}
	}
	for _, route := range snapshot.RouteProfiles {
		if route.ID == snapshot.ActiveRouteID {
			status["activeRoute"] = routeSummaryView{
				ID:              route.ID,
				Name:            route.Name,
				DefaultOutbound: route.DefaultOutbound,
			}
			break
		}
	}
	if restoreErr := s.restoreError(); restoreErr != "" {
		status["restoreError"] = restoreErr
	}
	if engineStatus.PID > 0 {
		status["enginePid"] = engineStatus.PID
	}
	if engineStatus.StartedAt != "" {
		status["engineStartedAt"] = engineStatus.StartedAt
	}
	if engineStatus.Error != "" {
		status["engineError"] = engineStatus.Error
	}
	if _, err := config.Build(snapshot); err != nil {
		status["configuration"] = "incomplete"
		status["configurationError"] = err.Error()
	} else {
		status["configuration"] = "ready"
	}
	return status
}

func (s *Server) setVPNEnabled(enabled bool) (any, *protocol.Error) {
	s.vpnMu.Lock()
	defer s.vpnMu.Unlock()
	if enabled {
		if _, err := config.Build(s.store.Snapshot()); err != nil {
			return nil, &protocol.Error{
				Code:    "configuration_incomplete",
				Message: fmt.Sprintf("%s: %v", errVPNConfigurationIncomplete, err),
			}
		}
	}
	if err := s.store.SetVPNEnabled(enabled); err != nil {
		return nil, &protocol.Error{Code: "state_failed", Message: err.Error()}
	}
	if err := s.reconcileVPN(enabled); err != nil {
		code := "engine_stop_failed"
		if enabled {
			code = "engine_start_failed"
			if errors.Is(err, errVPNConfigurationIncomplete) {
				code = "configuration_incomplete"
			}
		}
		return nil, &protocol.Error{Code: code, Message: err.Error()}
	}
	s.setRestoreError("")
	return s.status(), nil
}

func (s *Server) restoreDesiredState() {
	s.vpnMu.Lock()
	defer s.vpnMu.Unlock()
	enabled := s.store.Snapshot().VPNEnabled
	if err := s.reconcileVPN(enabled); err != nil {
		s.setRestoreError(err.Error())
		fmt.Fprintf(os.Stderr, "regaliad: restore VPN state: %v\n", err)
		return
	}
	s.setRestoreError("")
}

func (s *Server) setRestoreError(message string) {
	s.restoreMu.Lock()
	s.restoreErr = message
	s.restoreMu.Unlock()
}

func (s *Server) restoreError() string {
	s.restoreMu.RLock()
	defer s.restoreMu.RUnlock()
	return s.restoreErr
}

func (s *Server) reconcileVPN(enabled bool) error {
	status := s.engine.Status()
	if enabled {
		switch status.State {
		case engine.StateConnected, engine.StateStarting:
			return nil
		case engine.StateStopping:
			return errors.New("VPN engine is stopping")
		}
		if !loopbackPortAvailable(s.netcheckOptions.Port) {
			s.netcheckOptions = newNetcheckOptions()
		}
		result, err := config.BuildWithOptions(s.store.Snapshot(), s.netcheckOptions)
		if err != nil {
			return fmt.Errorf("%w: %v", errVPNConfigurationIncomplete, err)
		}
		if err := s.engine.Start(result.JSON); err != nil && !errors.Is(err, engine.ErrAlreadyRunning) {
			return err
		}
		return nil
	}
	if status.State == engine.StateUnavailable || status.State == engine.StateStopped {
		return nil
	}
	if status.State == engine.StateStopping {
		return nil
	}
	if err := s.engine.Stop(); err != nil && !errors.Is(err, engine.ErrNotRunning) {
		return err
	}
	return nil
}

func (s *Server) startNetworkTest(mode string, network netcheck.NetworkContext) (any, *protocol.Error) {
	mode, err := netcheck.NormalizeMode(mode)
	if err != nil {
		return nil, invalidParams(err)
	}
	connected := s.engine.Status().State == engine.StateConnected
	if (mode == "proxy" || mode == "compare") && !connected {
		return nil, &protocol.Error{Code: "vpn_required", Message: "connect Regalia before testing the VPN route"}
	}
	s.netcheckMu.Lock()
	if s.netcheckJob != nil && s.netcheckJob.State == "running" {
		s.netcheckMu.Unlock()
		return nil, &protocol.Error{Code: "test_busy", Message: "a network test is already running"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	now := time.Now().UTC()
	job := &networkTestJob{
		ID: fmt.Sprintf("netcheck-%d", now.UnixNano()), State: "running", Mode: mode,
		Phase: "preparing", Percent: 0, StartedAt: now.Format(time.RFC3339), cancel: cancel,
	}
	s.netcheckJob = job
	view := s.networkTestJobViewLocked()
	s.netcheckMu.Unlock()
	go s.runNetworkTest(ctx, job.ID, mode, network, connected)
	return view, nil
}

func (s *Server) runNetworkTest(ctx context.Context, id, mode string, network netcheck.NetworkContext, connected bool) {
	defer func() {
		s.netcheckMu.Lock()
		if s.netcheckJob != nil && s.netcheckJob.ID == id && s.netcheckJob.cancel != nil {
			s.netcheckJob.cancel()
			s.netcheckJob.cancel = nil
		}
		s.netcheckMu.Unlock()
	}()
	snapshot := s.store.Snapshot()
	server := activeNetcheckServer(snapshot)
	result := netcheck.Result{ID: id, Mode: mode, StartedAt: time.Now().UTC().Format(time.RFC3339), Network: network}
	routes := []string{mode}
	if mode == "compare" {
		routes = []string{"direct", "proxy"}
	}
	for routeIndex, route := range routes {
		proxy := netcheck.Proxy{}
		if connected {
			proxy.Port = s.netcheckOptions.Port
			if route == "direct" {
				proxy.Username = s.netcheckOptions.DirectUser
				proxy.Password = s.netcheckOptions.DirectPassword
			} else {
				proxy.Username = s.netcheckOptions.ProxyUser
				proxy.Password = s.netcheckOptions.ProxyPassword
			}
		}
		measurement, err := netcheck.Run(ctx, route, proxy, server, func(phase string, percent int) {
			overall := percent
			if len(routes) == 2 {
				overall = routeIndex*50 + percent/2
			}
			s.updateNetworkTest(id, route+":"+phase, overall)
		})
		if err != nil {
			s.finishNetworkTestError(id, err)
			return
		}
		result.Results = append(result.Results, measurement)
	}
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	netcheck.Compare(&result)
	if err := s.netcheckHistory.Add(result); err != nil {
		s.finishNetworkTestError(id, fmt.Errorf("save test history: %w", err))
		return
	}
	s.netcheckMu.Lock()
	if s.netcheckJob != nil && s.netcheckJob.ID == id {
		s.netcheckJob.State = "completed"
		s.netcheckJob.Phase = "done"
		s.netcheckJob.Percent = 100
		s.netcheckJob.Result = &result
	}
	s.netcheckMu.Unlock()
}

func (s *Server) updateNetworkTest(id, phase string, percent int) {
	s.netcheckMu.Lock()
	defer s.netcheckMu.Unlock()
	if s.netcheckJob != nil && s.netcheckJob.ID == id && s.netcheckJob.State == "running" {
		s.netcheckJob.Phase = phase
		s.netcheckJob.Percent = percent
	}
}

func (s *Server) finishNetworkTestError(id string, err error) {
	s.netcheckMu.Lock()
	defer s.netcheckMu.Unlock()
	if s.netcheckJob == nil || s.netcheckJob.ID != id {
		return
	}
	if errors.Is(err, context.Canceled) {
		s.netcheckJob.State = "cancelled"
		s.netcheckJob.Error = "test cancelled"
	} else {
		s.netcheckJob.State = "failed"
		s.netcheckJob.Error = err.Error()
	}
}

func (s *Server) networkTestStatus(id string) (any, *protocol.Error) {
	s.netcheckMu.Lock()
	defer s.netcheckMu.Unlock()
	if s.netcheckJob == nil || id == "" || s.netcheckJob.ID != id {
		return nil, &protocol.Error{Code: "test_not_found", Message: "network test was not found"}
	}
	return s.networkTestJobViewLocked(), nil
}

func (s *Server) cancelNetworkTest(id string) (any, *protocol.Error) {
	s.netcheckMu.Lock()
	defer s.netcheckMu.Unlock()
	if s.netcheckJob == nil || id == "" || s.netcheckJob.ID != id {
		return nil, &protocol.Error{Code: "test_not_found", Message: "network test was not found"}
	}
	if s.netcheckJob.State == "running" && s.netcheckJob.cancel != nil {
		s.netcheckJob.cancel()
	}
	return s.networkTestJobViewLocked(), nil
}

func (s *Server) networkTestJobViewLocked() networkTestJob {
	view := *s.netcheckJob
	view.cancel = nil
	return view
}

func activeNetcheckServer(snapshot state.State) netcheck.ServerContext {
	for _, profile := range snapshot.Profiles {
		for _, server := range profile.Servers {
			if server.ID == snapshot.ActiveServerID {
				return netcheck.ServerContext{ID: server.ID, Name: server.Name, Protocol: server.Protocol}
			}
		}
	}
	return netcheck.ServerContext{}
}

func newNetcheckOptions() config.NetcheckOptions {
	return config.NetcheckOptions{
		Port: pickLoopbackPort(), DirectUser: "direct", DirectPassword: randomSecret(),
		ProxyUser: "proxy", ProxyPassword: randomSecret(),
	}
}

func pickLoopbackPort() int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 39481
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func loopbackPortAvailable(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

func randomSecret() string {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for index := range buffer {
		buffer[index] = alphabet[int(buffer[index])%len(alphabet)]
	}
	return string(buffer)
}

func (s *Server) requireStopped() *protocol.Error {
	status := s.engine.Status()
	if engine.Active(status) {
		return &protocol.Error{
			Code:    "vpn_active",
			Message: "disconnect the VPN before changing its active configuration",
		}
	}
	return nil
}

func decodeParams(raw json.RawMessage, destination any) error {
	if len(raw) == 0 {
		return errors.New("params are required")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	return nil
}

func resultOrError[T any](result T, err error) (any, *protocol.Error) {
	if err != nil {
		return nil, invalidParams(err)
	}
	return result, nil
}

func emptyOrError(err error) (any, *protocol.Error) {
	if err != nil {
		return nil, invalidParams(err)
	}
	return map[string]bool{"ok": true}, nil
}

func invalidParams(err error) *protocol.Error {
	return &protocol.Error{Code: "invalid_params", Message: err.Error()}
}

func publicProfile(profile state.SubscriptionProfile) profileView {
	return profileView{
		ID:          profile.ID,
		Name:        profile.Name,
		CreatedAt:   profile.CreatedAt,
		UpdatedAt:   profile.UpdatedAt,
		LastError:   profile.LastError,
		ServerCount: len(profile.Servers),
	}
}

func publicServer(server state.Server) serverView {
	return serverView{
		ID:       server.ID,
		Name:     server.Name,
		Protocol: server.Protocol,
		Address:  server.Address,
		Port:     server.Port,
		Ready:    config.ValidateServer(server) == nil,
	}
}

func removeStaleSocket(path string) error {
	connection, err := net.Dial("unix", path)
	if err == nil {
		connection.Close()
		return fmt.Errorf("regaliad is already listening on %s", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket path %s", path)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}
