package state

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lyrka-meow/Regalia/internal/subscription"
)

const SchemaVersion = 1

type SubscriptionProfile struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	SubscriptionURL string   `json:"subscriptionUrl"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt,omitempty"`
	LastError       string   `json:"lastError,omitempty"`
	Servers         []Server `json:"servers"`
}

type Server struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Protocol string          `json:"protocol"`
	Address  string          `json:"address,omitempty"`
	Port     int             `json:"port,omitempty"`
	Source   string          `json:"source,omitempty"`
	Outbound json.RawMessage `json:"outbound,omitempty"`
}

type AppRule struct {
	DesktopID   string `json:"desktopId,omitempty"`
	Name        string `json:"name,omitempty"`
	Icon        string `json:"icon,omitempty"`
	ProcessPath string `json:"processPath"`
	Outbound    string `json:"outbound"`
}

type RouteProfile struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	DefaultOutbound string    `json:"defaultOutbound"`
	Apps            []AppRule `json:"apps,omitempty"`
	CreatedAt       string    `json:"createdAt"`
}

type State struct {
	SchemaVersion  int                   `json:"schemaVersion"`
	VPNEnabled     bool                  `json:"vpnEnabled"`
	ActiveRouteID  string                `json:"activeRouteId,omitempty"`
	ActiveServerID string                `json:"activeServerId,omitempty"`
	Profiles       []SubscriptionProfile `json:"profiles"`
	RouteProfiles  []RouteProfile        `json:"routeProfiles"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	data State
}

func (s *Store) SetVPNEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.VPNEnabled == enabled {
		return nil
	}
	previous := s.data.VPNEnabled
	s.data.VPNEnabled = enabled
	if err := s.saveLocked(); err != nil {
		s.data.VPNEnabled = previous
		return err
	}
	return nil
}

func Open(path string) (*Store, error) {
	store := &Store{
		path: path,
		data: State{
			SchemaVersion: SchemaVersion,
			Profiles:      []SubscriptionProfile{},
			RouteProfiles: []RouteProfile{},
		},
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("protect state file: %w", err)
	}
	if err := json.Unmarshal(raw, &store.data); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if store.data.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported state schema %d", store.data.SchemaVersion)
	}
	if store.data.Profiles == nil {
		store.data.Profiles = []SubscriptionProfile{}
	}
	for index := range store.data.Profiles {
		if store.data.Profiles[index].Servers == nil {
			store.data.Profiles[index].Servers = []Server{}
		}
	}
	if store.data.RouteProfiles == nil {
		store.data.RouteProfiles = []RouteProfile{}
	}
	return store, nil
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data)
}

func (s *Store) CreateProfile(name, subscriptionURL string) (SubscriptionProfile, error) {
	name = strings.TrimSpace(name)
	subscriptionURL = strings.TrimSpace(subscriptionURL)
	if name == "" || subscriptionURL == "" {
		return SubscriptionProfile{}, errors.New("name and subscriptionUrl are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	profile := SubscriptionProfile{
		ID:              newID("profile"),
		Name:            name,
		SubscriptionURL: subscriptionURL,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		Servers:         []Server{},
	}
	s.data.Profiles = append(s.data.Profiles, profile)
	return profile, s.saveLocked()
}

func (s *Store) DeleteProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := slices.IndexFunc(s.data.Profiles, func(profile SubscriptionProfile) bool {
		return profile.ID == id
	})
	if index < 0 {
		return errors.New("profile not found")
	}
	for _, server := range s.data.Profiles[index].Servers {
		if server.ID == s.data.ActiveServerID {
			s.data.ActiveServerID = ""
			break
		}
	}
	s.data.Profiles = slices.Delete(s.data.Profiles, index, index+1)
	return s.saveLocked()
}

func (s *Store) Profile(id string) (SubscriptionProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index := slices.IndexFunc(s.data.Profiles, func(profile SubscriptionProfile) bool {
		return profile.ID == id
	})
	if index < 0 {
		return SubscriptionProfile{}, errors.New("profile not found")
	}
	return cloneProfile(s.data.Profiles[index]), nil
}

func (s *Store) ReplaceServers(profileID string, candidates []subscription.Server) (SubscriptionProfile, error) {
	if len(candidates) == 0 {
		return SubscriptionProfile{}, errors.New("subscription contains no servers")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	index := slices.IndexFunc(s.data.Profiles, func(profile SubscriptionProfile) bool {
		return profile.ID == profileID
	})
	if index < 0 {
		return SubscriptionProfile{}, errors.New("profile not found")
	}

	servers := make([]Server, 0, len(candidates))
	seenIdentities := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		identity := candidate.Source
		if identity == "" {
			identity = string(candidate.Outbound)
		}
		if identity == "" || seenIdentities[identity] {
			continue
		}
		seenIdentities[identity] = true
		servers = append(servers, Server{
			ID:       stableServerID(profileID, identity),
			Name:     candidate.Name,
			Protocol: candidate.Protocol,
			Address:  candidate.Address,
			Port:     candidate.Port,
			Source:   candidate.Source,
			Outbound: candidate.Outbound,
		})
	}
	if len(servers) == 0 {
		return SubscriptionProfile{}, errors.New("subscription contains no unique servers")
	}

	if s.data.ActiveServerID != "" && !slices.ContainsFunc(servers, func(server Server) bool {
		return server.ID == s.data.ActiveServerID
	}) {
		s.data.ActiveServerID = ""
	}
	s.data.Profiles[index].Servers = servers
	s.data.Profiles[index].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.data.Profiles[index].LastError = ""
	if err := s.saveLocked(); err != nil {
		return SubscriptionProfile{}, err
	}
	return cloneProfile(s.data.Profiles[index]), nil
}

func (s *Store) RecordProfileError(profileID string, updateError error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := slices.IndexFunc(s.data.Profiles, func(profile SubscriptionProfile) bool {
		return profile.ID == profileID
	})
	if index < 0 {
		return errors.New("profile not found")
	}
	s.data.Profiles[index].LastError = updateError.Error()
	return s.saveLocked()
}

func (s *Store) SelectServer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, profile := range s.data.Profiles {
		for _, server := range profile.Servers {
			if server.ID != id {
				continue
			}
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return errors.New("server not found")
	}
	s.data.ActiveServerID = id
	return s.saveLocked()
}

func (s *Store) Server(id string) (Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, profile := range s.data.Profiles {
		for _, server := range profile.Servers {
			if server.ID == id {
				return server, nil
			}
		}
	}
	return Server{}, errors.New("server not found")
}

func (s *Store) CreateRoute(name, defaultOutbound string) (RouteProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return RouteProfile{}, errors.New("name is required")
	}
	if !validOutbound(defaultOutbound) {
		return RouteProfile{}, errors.New("defaultOutbound must be proxy or direct")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	route := RouteProfile{
		ID:              newID("route"),
		Name:            name,
		DefaultOutbound: defaultOutbound,
		Apps:            []AppRule{},
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	s.data.RouteProfiles = append(s.data.RouteProfiles, route)
	if s.data.ActiveRouteID == "" {
		s.data.ActiveRouteID = route.ID
	}
	return route, s.saveLocked()
}

func (s *Store) DeleteRoute(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := slices.IndexFunc(s.data.RouteProfiles, func(route RouteProfile) bool {
		return route.ID == id
	})
	if index < 0 {
		return errors.New("route profile not found")
	}
	s.data.RouteProfiles = slices.Delete(s.data.RouteProfiles, index, index+1)
	if s.data.ActiveRouteID == id {
		s.data.ActiveRouteID = ""
		if len(s.data.RouteProfiles) > 0 {
			s.data.ActiveRouteID = s.data.RouteProfiles[0].ID
		}
	}
	return s.saveLocked()
}

func (s *Store) ActivateRoute(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.ContainsFunc(s.data.RouteProfiles, func(route RouteProfile) bool {
		return route.ID == id
	}) {
		return errors.New("route profile not found")
	}
	s.data.ActiveRouteID = id
	return s.saveLocked()
}

func (s *Store) SetAppRule(routeID string, rule AppRule) error {
	rule.ProcessPath = filepath.Clean(strings.TrimSpace(rule.ProcessPath))
	if !filepath.IsAbs(rule.ProcessPath) {
		return errors.New("processPath must be an absolute path")
	}
	if !validOutbound(rule.Outbound) {
		return errors.New("outbound must be proxy or direct")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	routeIndex := slices.IndexFunc(s.data.RouteProfiles, func(route RouteProfile) bool {
		return route.ID == routeID
	})
	if routeIndex < 0 {
		return errors.New("route profile not found")
	}
	apps := s.data.RouteProfiles[routeIndex].Apps
	appIndex := slices.IndexFunc(apps, func(candidate AppRule) bool {
		return candidate.ProcessPath == rule.ProcessPath ||
			rule.DesktopID != "" && candidate.DesktopID == rule.DesktopID
	})
	if appIndex < 0 {
		apps = append(apps, rule)
	} else {
		apps[appIndex] = rule
	}
	s.data.RouteProfiles[routeIndex].Apps = apps
	return s.saveLocked()
}

// ReconcileAppPaths migrates stored desktop rules from launcher paths to the
// executable paths observed by sing-box. This keeps existing user profiles
// working after a launcher/real-process distinction is discovered.
func (s *Store) ReconcileAppPaths(paths map[string]string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for routeIndex := range s.data.RouteProfiles {
		for appIndex := range s.data.RouteProfiles[routeIndex].Apps {
			rule := &s.data.RouteProfiles[routeIndex].Apps[appIndex]
			processPath := filepath.Clean(strings.TrimSpace(paths[rule.DesktopID]))
			if rule.DesktopID == "" || !filepath.IsAbs(processPath) || processPath == rule.ProcessPath {
				continue
			}
			rule.ProcessPath = processPath
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, s.saveLocked()
}

func (s *Store) RemoveAppRule(routeID, processPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	routeIndex := slices.IndexFunc(s.data.RouteProfiles, func(route RouteProfile) bool {
		return route.ID == routeID
	})
	if routeIndex < 0 {
		return errors.New("route profile not found")
	}
	apps := s.data.RouteProfiles[routeIndex].Apps
	appIndex := slices.IndexFunc(apps, func(rule AppRule) bool {
		return rule.ProcessPath == processPath
	})
	if appIndex < 0 {
		return errors.New("application rule not found")
	}
	s.data.RouteProfiles[routeIndex].Apps = slices.Delete(apps, appIndex, appIndex+1)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func clone(value State) State {
	raw, _ := json.Marshal(value)
	var result State
	_ = json.Unmarshal(raw, &result)
	return result
}

func cloneProfile(value SubscriptionProfile) SubscriptionProfile {
	raw, _ := json.Marshal(value)
	var result SubscriptionProfile
	_ = json.Unmarshal(raw, &result)
	return result
}

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
}

func stableServerID(profileID, identity string) string {
	sum := sha256.Sum256([]byte(profileID + "\x00" + identity))
	return fmt.Sprintf("server-%x", sum[:8])
}

func validOutbound(value string) bool {
	return value == "proxy" || value == "direct"
}
