package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/lyrka-meow/Regalia/internal/apps"
	"github.com/lyrka-meow/Regalia/internal/config"
	"github.com/lyrka-meow/Regalia/internal/engine"
	"github.com/lyrka-meow/Regalia/internal/protocol"
	"github.com/lyrka-meow/Regalia/internal/state"
	"github.com/lyrka-meow/Regalia/internal/subscription"
)

type Server struct {
	socketPath string
	store      *state.Store
	fetcher    *subscription.Fetcher
	engine     engine.Controller
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

func New(socketPath string, store *state.Store) *Server {
	return NewWithEngine(socketPath, store, engine.NewUnavailable("VPN engine controller is not configured"))
}

func NewWithEngine(socketPath string, store *state.Store, controller engine.Controller) *Server {
	if controller == nil {
		controller = engine.NewUnavailable("VPN engine controller is not configured")
	}
	return &Server{
		socketPath: socketPath,
		store:      store,
		fetcher:    subscription.NewFetcher(),
		engine:     controller,
	}
}

func (s *Server) ListenAndServe() error {
	defer func() {
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

	for {
		connection, err := listener.Accept()
		if err != nil {
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
		result, err := config.Build(s.store.Snapshot())
		if err != nil {
			return nil, &protocol.Error{Code: "configuration_incomplete", Message: err.Error()}
		}
		if err := s.engine.Start(result.JSON); err != nil {
			return nil, &protocol.Error{Code: "engine_start_failed", Message: err.Error()}
		}
		return s.status(), nil
	case "vpn.disconnect":
		if err := s.engine.Stop(); err != nil && !errors.Is(err, engine.ErrNotRunning) {
			return nil, &protocol.Error{Code: "engine_stop_failed", Message: err.Error()}
		}
		return s.status(), nil
	case "apps.list":
		return apps.List(), nil
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
	default:
		return nil, &protocol.Error{Code: "method_not_found", Message: "unknown method: " + method}
	}
}

func (s *Server) status() map[string]any {
	snapshot := s.store.Snapshot()
	engineStatus := s.engine.Status()
	connected := engineStatus.State == engine.StateConnected
	status := map[string]any{
		"apiVersion":      protocol.Version,
		"engine":          engineStatus.State,
		"engineAvailable": engineStatus.Available,
		"connected":       connected,
		"tun":             connected,
		"activeRouteId":   snapshot.ActiveRouteID,
		"activeServerId":  snapshot.ActiveServerID,
		"socket":          s.socketPath,
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
