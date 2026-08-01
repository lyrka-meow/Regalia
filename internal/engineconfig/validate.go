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
	if err := validateTUN(document["inbounds"]); err != nil {
		return err
	}
	if err := validateOutbounds(document["outbounds"], document["endpoints"]); err != nil {
		return err
	}
	if err := validateRoute(document["route"]); err != nil {
		return err
	}
	if _, ok := document["dns"].(map[string]any); !ok {
		return errors.New("dns must be an object")
	}
	return nil
}

func validateTUN(value any) error {
	inbounds, ok := value.([]any)
	if !ok || len(inbounds) != 1 {
		return errors.New("exactly one inbound is required")
	}
	tun, ok := inbounds[0].(map[string]any)
	if !ok {
		return errors.New("TUN inbound must be an object")
	}
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

func validateRoute(value any) error {
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
	for index, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			return errors.New("route rule must be an object")
		}
		action := stringValue(rule["action"])
		switch action {
		case "sniff":
			if index != 0 || len(rule) != 1 {
				return errors.New("sniff must be the first plain rule")
			}
		case "hijack-dns":
			if index != 1 || stringValue(rule["protocol"]) != "dns" {
				return errors.New("DNS hijack must be the second rule")
			}
			if err := onlyKeys(rule, "protocol", "action"); err != nil {
				return fmt.Errorf("DNS rule: %w", err)
			}
		case "route":
			if err := onlyKeys(rule, "process_path", "action", "outbound"); err != nil {
				return fmt.Errorf("application rule: %w", err)
			}
			outbound := stringValue(rule["outbound"])
			if outbound != "proxy" && outbound != "direct" {
				return errors.New("application rule outbound must be proxy or direct")
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
