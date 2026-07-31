package subscription

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Server struct {
	Name     string          `json:"name"`
	Protocol string          `json:"protocol"`
	Address  string          `json:"address,omitempty"`
	Port     int             `json:"port,omitempty"`
	Source   string          `json:"source,omitempty"`
	Outbound json.RawMessage `json:"outbound,omitempty"`
}

func Parse(input []byte) ([]Server, error) {
	return parse(bytes.TrimSpace(input), 0)
}

func parse(input []byte, depth int) ([]Server, error) {
	if len(input) == 0 {
		return nil, errors.New("subscription is empty")
	}
	if depth > 2 {
		return nil, errors.New("subscription nesting is too deep")
	}

	if servers, recognized, err := parseJSON(input); recognized {
		if err != nil {
			return nil, err
		}
		return requireServers(servers)
	}

	if decoded, ok := decodeBase64(input); ok {
		if servers, err := parse(decoded, depth+1); err == nil {
			return servers, nil
		}
	}

	var servers []Server
	var parseErrors []string
	for _, line := range strings.Split(strings.ReplaceAll(string(input), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		server, err := parseLink(line)
		if err != nil {
			parseErrors = append(parseErrors, err.Error())
			continue
		}
		servers = append(servers, server)
	}
	if len(servers) == 0 {
		if len(parseErrors) > 0 {
			return nil, fmt.Errorf("no supported servers: %s", strings.Join(parseErrors, "; "))
		}
		return nil, errors.New("no supported servers found")
	}
	return servers, nil
}

func parseJSON(input []byte) ([]Server, bool, error) {
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return nil, false, nil
	}
	var objects []map[string]any
	switch typed := value.(type) {
	case map[string]any:
		if outbounds, ok := typed["outbounds"].([]any); ok {
			objects = objectList(outbounds)
		} else if endpoints, ok := typed["endpoints"].([]any); ok {
			objects = objectList(endpoints)
		} else if _, hasType := typed["type"]; hasType {
			objects = []map[string]any{typed}
		} else if _, hasProtocol := typed["protocol"]; hasProtocol {
			objects = []map[string]any{typed}
		} else {
			return nil, true, errors.New("JSON does not contain outbounds")
		}
	case []any:
		objects = objectList(typed)
	default:
		return nil, true, errors.New("unsupported JSON subscription")
	}

	var servers []Server
	for _, object := range objects {
		server, ok := serverFromObject(object)
		if ok {
			servers = append(servers, server)
		}
	}
	return servers, true, nil
}

func objectList(values []any) []map[string]any {
	objects := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

func serverFromObject(object map[string]any) (Server, bool) {
	protocol := stringValue(object["type"])
	if protocol == "" {
		protocol = stringValue(object["protocol"])
	}
	if isInternalProtocol(protocol) {
		return Server{}, false
	}
	name := stringValue(object["tag"])
	if name == "" {
		name = stringValue(object["remarks"])
	}
	address := stringValue(object["server"])
	port := intValue(object["server_port"])

	if protocol == "vless" && address == "" {
		settings, _ := object["settings"].(map[string]any)
		if vnext, ok := settings["vnext"].([]any); ok && len(vnext) > 0 {
			if first, ok := vnext[0].(map[string]any); ok {
				address = stringValue(first["address"])
				port = intValue(first["port"])
			}
		}
	}
	if protocol == "" {
		return Server{}, false
	}
	if name == "" {
		name = displayName(protocol, address, port)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return Server{}, false
	}
	return Server{
		Name:     name,
		Protocol: normalizeProtocol(protocol),
		Address:  address,
		Port:     port,
		Outbound: raw,
	}, true
}

func parseLink(link string) (Server, error) {
	schemeEnd := strings.Index(link, "://")
	if schemeEnd < 1 {
		return Server{}, errors.New("unsupported line")
	}
	scheme := strings.ToLower(link[:schemeEnd])
	if scheme == "vmess" {
		return parseVMess(link)
	}
	if scheme == "json" {
		return parseJSONLink(link)
	}
	if !supportedScheme(scheme) {
		return Server{}, fmt.Errorf("unsupported protocol %q", scheme)
	}

	parsed, err := url.Parse(link)
	if err != nil {
		return Server{}, fmt.Errorf("%s link: %w", scheme, err)
	}
	address := parsed.Hostname()
	port := portNumber(parsed.Port())
	if scheme == "ss" && address == "" {
		address, port = parseEncodedShadowsocksHost(parsed)
	}
	if address == "" {
		return Server{}, fmt.Errorf("%s link has no server address", scheme)
	}
	name, _ := url.PathUnescape(parsed.Fragment)
	if name == "" {
		name = displayName(scheme, address, port)
	}
	outbound, _ := outboundFromURL(parsed, normalizeProtocol(scheme))
	return Server{
		Name:     name,
		Protocol: normalizeProtocol(scheme),
		Address:  address,
		Port:     port,
		Source:   link,
		Outbound: outbound,
	}, nil
}

func parseVMess(link string) (Server, error) {
	payload := strings.TrimPrefix(link, "vmess://")
	decoded, ok := decodeBase64([]byte(payload))
	if !ok {
		return Server{}, errors.New("vmess link has invalid base64")
	}
	var object map[string]any
	if err := json.Unmarshal(decoded, &object); err != nil {
		return Server{}, fmt.Errorf("vmess JSON: %w", err)
	}
	address := stringValue(object["add"])
	port := intValue(object["port"])
	if address == "" {
		return Server{}, errors.New("vmess link has no server address")
	}
	name := stringValue(object["ps"])
	if name == "" {
		name = displayName("vmess", address, port)
	}
	outbound, _ := vmessOutbound(object)
	return Server{
		Name:     name,
		Protocol: "vmess",
		Address:  address,
		Port:     port,
		Source:   link,
		Outbound: outbound,
	}, nil
}

func parseJSONLink(link string) (Server, error) {
	parsed, err := url.Parse(link)
	if err != nil {
		return Server{}, err
	}
	decoded, ok := decodeBase64([]byte(parsed.Fragment))
	if !ok {
		return Server{}, errors.New("JSON link has invalid base64")
	}
	servers, recognized, err := parseJSON(decoded)
	if !recognized || err != nil || len(servers) != 1 {
		if err == nil {
			err = errors.New("JSON link must contain exactly one outbound")
		}
		return Server{}, err
	}
	servers[0].Source = link
	return servers[0], nil
}

func parseEncodedShadowsocksHost(parsed *url.URL) (string, int) {
	encoded := parsed.Host
	if encoded == "" {
		encoded = parsed.Opaque
	}
	decoded, ok := decodeBase64([]byte(encoded))
	if !ok {
		return "", 0
	}
	value := string(decoded)
	at := strings.LastIndex(value, "@")
	if at >= 0 {
		value = value[at+1:]
	}
	host, portText, found := strings.Cut(value, ":")
	if !found {
		return "", 0
	}
	return host, portNumber(portText)
}

func decodeBase64(input []byte) ([]byte, bool) {
	compact := strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == ' ' || character == '\t' {
			return -1
		}
		return character
	}, string(input))
	if compact == "" {
		return nil, false
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(compact)
		if err == nil && len(decoded) > 0 && utf8.Valid(decoded) && looksLikeSubscription(decoded) {
			return bytes.TrimSpace(decoded), true
		}
	}
	return nil, false
}

func looksLikeSubscription(decoded []byte) bool {
	text := strings.TrimSpace(string(decoded))
	return strings.HasPrefix(text, "{") ||
		strings.HasPrefix(text, "[") ||
		strings.Contains(text, "://")
}

func requireServers(servers []Server) ([]Server, error) {
	if len(servers) == 0 {
		return nil, errors.New("subscription contains no supported server outbounds")
	}
	return servers, nil
}

func supportedScheme(scheme string) bool {
	switch scheme {
	case "socks", "socks4", "socks4a", "socks5", "http", "https", "ss",
		"vless", "trojan", "anytls", "mieru", "mierus", "hysteria",
		"hysteria2", "hy2", "tuic", "juicity", "tt", "shadowtls", "wg",
		"ssh", "naive+https", "naive+quic":
		return true
	default:
		return false
	}
}

func normalizeProtocol(protocol string) string {
	switch strings.ToLower(protocol) {
	case "hy2":
		return "hysteria2"
	case "ss":
		return "shadowsocks"
	case "socks4", "socks4a", "socks5":
		return "socks"
	case "https":
		return "http"
	case "mierus":
		return "mieru"
	case "tt":
		return "trusttunnel"
	case "wg":
		return "wireguard"
	default:
		return strings.ToLower(protocol)
	}
}

func isInternalProtocol(protocol string) bool {
	switch strings.ToLower(protocol) {
	case "", "direct", "block", "dns", "selector", "urltest", "freedom",
		"blackhole", "loopback":
		return true
	default:
		return false
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		number, _ := strconv.Atoi(typed.String())
		return number
	case string:
		return portNumber(typed)
	default:
		return 0
	}
}

func portNumber(value string) int {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

func displayName(protocol, address string, port int) string {
	if address == "" {
		return strings.ToUpper(protocol)
	}
	if port > 0 {
		return fmt.Sprintf("%s:%d", address, port)
	}
	return address
}
