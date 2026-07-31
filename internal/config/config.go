package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/lyrka-meow/Regalia/internal/state"
)

type Result struct {
	JSON        []byte
	TunIPv4CIDR string
	Server      state.Server
	Route       *state.RouteProfile
}

func Build(snapshot state.State) (Result, error) {
	server, err := activeServer(snapshot)
	if err != nil {
		return Result{}, err
	}
	var proxy map[string]any
	if err := ValidateServer(server); err != nil {
		return Result{}, err
	}
	_ = json.Unmarshal(server.Outbound, &proxy)
	proxyType := proxy["type"].(string)
	proxy["tag"] = "proxy"
	outbounds := []any{map[string]any{"type": "direct", "tag": "direct"}}
	var endpoints []any
	if isEndpoint(proxyType) {
		endpoints = append(endpoints, proxy)
	} else {
		outbounds = append([]any{proxy}, outbounds...)
	}

	routeProfile := activeRoute(snapshot)
	route := buildRoute(routeProfile)
	tunCIDR := "172.19.0.1/30"
	document := map[string]any{
		"log": map[string]any{
			"level":     "info",
			"timestamp": true,
		},
		"certificate": map[string]any{
			"store": "system",
		},
		"dns": map[string]any{
			"servers": []any{
				map[string]any{
					"type": "local",
					"tag":  "dns-local",
				},
				map[string]any{
					"type":            "https",
					"tag":             "dns-remote",
					"server":          "1.1.1.1",
					"server_port":     443,
					"path":            "/dns-query",
					"detour":          "proxy",
					"domain_resolver": "dns-local",
				},
			},
			"rules": []any{
				map[string]any{
					"action": "route",
					"server": "dns-remote",
				},
			},
		},
		"inbounds": []any{
			map[string]any{
				"type":           "tun",
				"tag":            "tun-in",
				"interface_name": "regalia0",
				"address":        []string{tunCIDR},
				"auto_route":     true,
				"auto_redirect":  true,
				"strict_route":   true,
				"stack":          "system",
				"mtu":            9000,
				"route_exclude_address": []string{
					"127.0.0.0/8",
					"10.0.0.0/8",
					"172.16.0.0/12",
					"192.168.0.0/16",
					"169.254.0.0/16",
					"224.0.0.0/4",
					"255.255.255.255/32",
				},
			},
		},
		"outbounds": outbounds,
		"route":     route,
	}
	if len(endpoints) > 0 {
		document["endpoints"] = endpoints
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return Result{}, fmt.Errorf("encode core config: %w", err)
	}
	return Result{
		JSON:        raw,
		TunIPv4CIDR: tunCIDR,
		Server:      server,
		Route:       routeProfile,
	}, nil
}

func ValidateServer(server state.Server) error {
	if len(server.Outbound) == 0 {
		return fmt.Errorf("%s is imported but its protocol is not ready for connection generation", server.Protocol)
	}
	var outbound map[string]any
	if err := json.Unmarshal(server.Outbound, &outbound); err != nil {
		return fmt.Errorf("decode selected outbound: %w", err)
	}
	if outboundType, _ := outbound["type"].(string); outboundType == "" {
		return errors.New("selected outbound is not a sing-box outbound")
	}
	return nil
}

func isEndpoint(proxyType string) bool {
	switch proxyType {
	case "wireguard", "tailscale":
		return true
	default:
		return false
	}
}

func activeServer(snapshot state.State) (state.Server, error) {
	if snapshot.ActiveServerID == "" {
		return state.Server{}, errors.New("no server is selected")
	}
	for _, profile := range snapshot.Profiles {
		index := slices.IndexFunc(profile.Servers, func(server state.Server) bool {
			return server.ID == snapshot.ActiveServerID
		})
		if index >= 0 {
			return profile.Servers[index], nil
		}
	}
	return state.Server{}, errors.New("selected server no longer exists")
}

func activeRoute(snapshot state.State) *state.RouteProfile {
	if snapshot.ActiveRouteID == "" {
		return nil
	}
	index := slices.IndexFunc(snapshot.RouteProfiles, func(route state.RouteProfile) bool {
		return route.ID == snapshot.ActiveRouteID
	})
	if index < 0 {
		return nil
	}
	route := snapshot.RouteProfiles[index]
	return &route
}

func buildRoute(profile *state.RouteProfile) map[string]any {
	final := "proxy"
	var appRules []state.AppRule
	if profile != nil {
		final = profile.DefaultOutbound
		appRules = profile.Apps
	}
	rules := []any{
		map[string]any{"action": "sniff"},
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
	}
	for _, outbound := range []string{"direct", "proxy"} {
		var processPaths []string
		for _, rule := range appRules {
			if rule.Outbound == outbound {
				processPaths = append(processPaths, rule.ProcessPath)
			}
		}
		if len(processPaths) > 0 {
			rules = append(rules, map[string]any{
				"process_path": processPaths,
				"action":       "route",
				"outbound":     outbound,
			})
		}
	}
	return map[string]any{
		"rules":                 rules,
		"final":                 final,
		"find_process":          true,
		"auto_detect_interface": true,
		"default_domain_resolver": map[string]any{
			"server":   "dns-local",
			"strategy": "prefer_ipv4",
		},
	}
}
