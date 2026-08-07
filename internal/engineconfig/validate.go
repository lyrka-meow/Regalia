package engineconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

const MaxSize = 4 * 1024 * 1024

func Validate(raw []byte) error {
	if len(raw) == 0 {
		return errors.New("configuration is empty")
	}
	if len(raw) > MaxSize {
		return fmt.Errorf("configuration exceeds %d bytes", MaxSize)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	if err := onlyKeys(document, "log", "certificate", "dns", "inbounds", "outbounds", "endpoints", "route"); err != nil {
		return fmt.Errorf("top level: %w", err)
	}
	hasNetcheck, err := validateInbounds(document["inbounds"])
	if err != nil {
		return err
	}
	if err := validateOutbounds(document["outbounds"], document["endpoints"]); err != nil {
		return err
	}
	if err := validateRoute(document["route"], hasNetcheck); err != nil {
		return err
	}
	if _, ok := document["dns"].(map[string]any); !ok {
		return errors.New("dns must be an object")
	}
	return nil
}

func validateInbounds(value any) (bool, error) {
	inbounds, ok := value.([]any)
	if !ok || len(inbounds) < 1 || len(inbounds) > 2 {
		return false, errors.New("one TUN and at most one netcheck inbound are required")
	}
	tunCount := 0
	netcheckCount := 0
	for _, value := range inbounds {
		inbound, ok := value.(map[string]any)
		if !ok {
			return false, errors.New("inbound must be an object")
		}
		switch stringValue(inbound["tag"]) {
		case "tun-in":
			tunCount++
			if err := validateTUN(inbound); err != nil {
				return false, err
			}
		case "regalia-netcheck":
			netcheckCount++
			if err := validateNetcheckInbound(inbound); err != nil {
				return false, err
			}
		default:
			return false, fmt.Errorf("unexpected inbound tag %q", stringValue(inbound["tag"]))
		}
	}
	if tunCount != 1 || netcheckCount > 1 {
		return false, errors.New("one Regalia TUN and at most one netcheck inbound are required")
	}
	return netcheckCount == 1, nil
}

func validateTUN(tun map[string]any) error {
	if err := onlyKeys(tun,
		"type", "tag", "interface_name", "address", "auto_route", "auto_redirect",
		"strict_route", "stack", "mtu", "route_exclude_address"); err != nil {
		return fmt.Errorf("TUN inbound: %w", err)
	}
	if stringValue(tun["type"]) != "tun" || stringValue(tun["tag"]) != "tun-in" {
		return errors.New("inbound must be the Regalia TUN")
	}
	if stringValue(tun["interface_name"]) != "regalia0" {
		return errors.New("TUN interface must be regalia0")
	}
	for _, key := range []string{"auto_route", "auto_redirect", "strict_route"} {
		if enabled, ok := tun[key].(bool); !ok || !enabled {
			return fmt.Errorf("TUN %s must be enabled", key)
		}
	}
	addresses, ok := stringList(tun["address"])
	if !ok || len(addresses) != 1 || addresses[0] != "172.19.0.1/30" {
		return errors.New("unexpected TUN address")
	}
	return nil
}

func validateNetcheckInbound(inbound map[string]any) error {
	if err := onlyKeys(inbound, "type", "tag", "listen", "listen_port", "users"); err != nil {
		return fmt.Errorf("netcheck inbound: %w", err)
	}
	if stringValue(inbound["type"]) != "mixed" || stringValue(inbound["tag"]) != "regalia-netcheck" {
		return errors.New("netcheck inbound must be the Regalia mixed proxy")
	}
	if stringValue(inbound["listen"]) != "127.0.0.1" {
		return errors.New("netcheck inbound must listen on IPv4 loopback")
	}
	port, ok := numberValue(inbound["listen_port"])
	if !ok || port < 1024 || port > 65535 {
		return errors.New("netcheck inbound uses an invalid port")
	}
	users, ok := inbound["users"].([]any)
	if !ok || len(users) != 2 {
		return errors.New("netcheck inbound requires exactly two authenticated users")
	}
	seen := map[string]bool{}
	for _, value := range users {
		user, ok := value.(map[string]any)
		if !ok {
			return errors.New("netcheck user must be an object")
		}
		if err := onlyKeys(user, "username", "password"); err != nil {
			return fmt.Errorf("netcheck user: %w", err)
		}
		username := stringValue(user["username"])
		password := stringValue(user["password"])
		if username == "" || password == "" || seen[username] {
			return errors.New("netcheck users require unique non-empty credentials")
		}
		seen[username] = true
	}
	return nil
}

func validateOutbounds(outboundValue, endpointValue any) error {
	outbounds, ok := outboundValue.([]any)
	if !ok || len(outbounds) < 1 {
		return errors.New("direct outbound is required")
	}
	directFound := false
	proxyFound := false
	for _, value := range outbounds {
		outbound, ok := value.(map[string]any)
		if !ok {
			return errors.New("outbound must be an object")
		}
		tag := stringValue(outbound["tag"])
		outboundType := stringValue(outbound["type"])
		switch tag {
		case "direct":
			if outboundType != "direct" {
				return errors.New("direct tag must use the direct outbound")
			}
			directFound = true
		case "proxy":
			if outboundType == "" || outboundType == "direct" || outboundType == "block" || outboundType == "dns" {
				return errors.New("proxy tag uses an invalid outbound type")
			}
			proxyFound = true
		default:
			return fmt.Errorf("unexpected outbound tag %q", tag)
		}
	}
	if endpointValue != nil {
		endpoints, ok := endpointValue.([]any)
		if !ok || len(endpoints) != 1 {
			return errors.New("at most one proxy endpoint is allowed")
		}
		endpoint, ok := endpoints[0].(map[string]any)
		if !ok || stringValue(endpoint["tag"]) != "proxy" {
			return errors.New("endpoint must use the proxy tag")
		}
		endpointType := stringValue(endpoint["type"])
		if endpointType != "wireguard" && endpointType != "tailscale" {
			return fmt.Errorf("unsupported proxy endpoint type %q", endpointType)
		}
		proxyFound = true
	}
	if !directFound || !proxyFound {
		return errors.New("configuration must contain direct and proxy routes")
	}
	return nil
}

func validateRoute(value any, expectNetcheck bool) error {
	route, ok := value.(map[string]any)
	if !ok {
		return errors.New("route must be an object")
	}
	if err := onlyKeys(route, "rules", "final", "find_process", "auto_detect_interface", "default_domain_resolver"); err != nil {
		return fmt.Errorf("route: %w", err)
	}
	if final := stringValue(route["final"]); final != "proxy" && final != "direct" {
		return errors.New("route final must be proxy or direct")
	}
	for _, key := range []string{"find_process", "auto_detect_interface"} {
		if enabled, ok := route[key].(bool); !ok || !enabled {
			return fmt.Errorf("route %s must be enabled", key)
		}
	}
	rules, ok := route["rules"].([]any)
	if !ok || len(rules) < 2 {
		return errors.New("route must contain sniff and DNS rules")
	}
	sniffIndex := 0
	if expectNetcheck {
		if len(rules) < 5 {
			return errors.New("netcheck inbound requires resolver, direct and proxy route rules")
		}
		sniffIndex = 3
	}
	netcheckUsers := map[string]bool{}
	netcheckDirectUser := ""
	for index, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			return errors.New("route rule must be an object")
		}
		action := stringValue(rule["action"])
		switch action {
		case "resolve":
			user := stringValue(rule["auth_user"])
			if !expectNetcheck || index != 0 || stringValue(rule["inbound"]) != "regalia-netcheck" || user == "" {
				return errors.New("invalid netcheck resolver rule")
			}
			if stringValue(rule["server"]) != "dns-remote" || stringValue(rule["strategy"]) != "prefer_ipv4" {
				return errors.New("netcheck resolver must use the remote IPv4 resolver")
			}
			if err := onlyKeys(rule, "inbound", "auth_user", "action", "server", "strategy"); err != nil {
				return fmt.Errorf("netcheck resolver rule: %w", err)
			}
			netcheckDirectUser = user
		case "sniff":
			if index != sniffIndex || len(rule) != 1 {
				return errors.New("sniff must be the first plain rule")
			}
		case "hijack-dns":
			if index != sniffIndex+1 || stringValue(rule["protocol"]) != "dns" {
				return errors.New("DNS hijack must be the second rule")
			}
			if err := onlyKeys(rule, "protocol", "action"); err != nil {
				return fmt.Errorf("DNS rule: %w", err)
			}
		case "route":
			outbound := stringValue(rule["outbound"])
			if outbound != "proxy" && outbound != "direct" {
				return errors.New("application rule outbound must be proxy or direct")
			}
			if inbound := stringValue(rule["inbound"]); inbound != "" {
				user := stringValue(rule["auth_user"])
				if !expectNetcheck || index < 1 || index >= 3 || inbound != "regalia-netcheck" || user == "" || netcheckUsers[user] {
					return errors.New("invalid netcheck route rule")
				}
				if (index == 1 && (outbound != "direct" || user != netcheckDirectUser)) || (index == 2 && outbound != "proxy") {
					return errors.New("netcheck rules must route direct before proxy")
				}
				if err := onlyKeys(rule, "inbound", "auth_user", "action", "outbound"); err != nil {
					return fmt.Errorf("netcheck rule: %w", err)
				}
				netcheckUsers[user] = true
				continue
			}
			if err := onlyKeys(rule, "process_path", "action", "outbound"); err != nil {
				return fmt.Errorf("application rule: %w", err)
			}
			paths, ok := stringList(rule["process_path"])
			if !ok || len(paths) == 0 {
				return errors.New("application rule requires process paths")
			}
			for _, path := range paths {
				if !filepath.IsAbs(path) {
					return errors.New("application process path must be absolute")
				}
			}
		default:
			return fmt.Errorf("unsupported route action %q", action)
		}
	}
	if expectNetcheck && len(netcheckUsers) != 2 {
		return errors.New("netcheck inbound requires two unique route users")
	}
	return nil
}

func onlyKeys(object map[string]any, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key := range object {
		if !set[key] {
			return fmt.Errorf("field %q is not allowed", key)
		}
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func stringList(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func numberValue(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}
