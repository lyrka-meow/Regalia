package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func outboundFromURL(parsed *url.URL, protocol string) (json.RawMessage, error) {
	if parsed.Hostname() == "" {
		return nil, errors.New("server address is required")
	}
	outbound := map[string]any{
		"type":   protocol,
		"server": parsed.Hostname(),
	}
	if port := portNumber(parsed.Port()); port > 0 {
		outbound["server_port"] = port
	}
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	query := parsed.Query()

	switch protocol {
	case "trojan", "anytls":
		outbound["password"] = username
		if password != "" {
			outbound["password"] = password
		}
	case "vless":
		if username == "" {
			return nil, errors.New("vless UUID is required")
		}
		outbound["uuid"] = username
		if flow := query.Get("flow"); flow != "" {
			outbound["flow"] = flow
		}
		packetEncoding := query.Get("packetEncoding")
		if packetEncoding == "" {
			packetEncoding = "xudp"
		}
		if packetEncoding != "none" {
			outbound["packet_encoding"] = packetEncoding
		}
	case "hysteria2":
		outbound["password"] = username
		if password != "" {
			outbound["password"] = password
		}
	case "tuic":
		outbound["uuid"] = username
		if password != "" {
			outbound["password"] = password
		} else if value := query.Get("password"); value != "" {
			outbound["password"] = value
		}
	case "socks", "socks4", "socks4a", "socks5", "http", "https":
		if username != "" {
			outbound["username"] = username
		}
		if password != "" {
			outbound["password"] = password
		}
	case "shadowsocks":
		method, secret := shadowsocksCredentials(parsed)
		if method != "" {
			outbound["method"] = method
		}
		if secret != "" {
			outbound["password"] = secret
		}
	default:
		return nil, nil
	}

	tls := tlsFromQuery(query, parsed.Hostname(), protocol)
	if parsed.Scheme == "https" && len(tls) == 0 {
		tls = map[string]any{"enabled": true, "server_name": parsed.Hostname()}
	}
	if len(tls) > 0 {
		outbound["tls"] = tls
	}
	if transport := transportFromQuery(query); len(transport) > 0 {
		outbound["transport"] = transport
	}
	raw, err := json.Marshal(outbound)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func vmessOutbound(source map[string]any) (json.RawMessage, error) {
	address := stringValue(source["add"])
	port := intValue(source["port"])
	uuid := stringValue(source["id"])
	if address == "" || uuid == "" {
		return nil, errors.New("vmess address and UUID are required")
	}
	outbound := map[string]any{
		"type":   "vmess",
		"server": address,
		"uuid":   uuid,
	}
	if port > 0 {
		outbound["server_port"] = port
	}
	if security := stringValue(source["scy"]); security != "" && security != "auto" {
		outbound["security"] = security
	}
	if alterID := intValue(source["aid"]); alterID > 0 {
		outbound["alter_id"] = alterID
	}

	tlsName := stringValue(source["sni"])
	if stringValue(source["tls"]) == "tls" || tlsName != "" {
		tls := map[string]any{"enabled": true}
		if tlsName != "" {
			tls["server_name"] = tlsName
		}
		if fingerprint := stringValue(source["fp"]); fingerprint != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
		}
		outbound["tls"] = tls
	}
	transportType := stringValue(source["net"])
	if transportType == "h2" {
		transportType = "http"
	}
	transport := map[string]any{}
	if transportType != "" && transportType != "tcp" {
		transport["type"] = transportType
	}
	if path := stringValue(source["path"]); path != "" {
		transport["path"] = path
	}
	if host := stringValue(source["host"]); host != "" {
		if transportType == "ws" {
			transport["headers"] = map[string]any{"Host": host}
		} else {
			transport["host"] = host
		}
	}
	if len(transport) > 0 {
		outbound["transport"] = transport
	}
	return json.Marshal(outbound)
}

func tlsFromQuery(query url.Values, address, protocol string) map[string]any {
	security := strings.ToLower(query.Get("security"))
	enabled := security == "tls" || security == "reality" ||
		query.Get("sni") != "" || query.Get("peer") != "" ||
		protocol == "trojan" || protocol == "hysteria2" || protocol == "tuic" ||
		protocol == "anytls"
	if !enabled {
		return nil
	}
	tls := map[string]any{"enabled": true}
	serverName := query.Get("sni")
	if serverName == "" {
		serverName = query.Get("peer")
	}
	if serverName == "" {
		serverName = address
	}
	if serverName != "" {
		tls["server_name"] = serverName
	}
	if truthy(query.Get("allowInsecure")) || truthy(query.Get("allow_insecure")) ||
		truthy(query.Get("insecure")) {
		tls["insecure"] = true
	}
	if alpn := splitNonEmpty(query.Get("alpn"), ","); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	if fingerprint := query.Get("fp"); fingerprint != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
	}
	if publicKey := query.Get("pbk"); publicKey != "" {
		reality := map[string]any{"enabled": true, "public_key": publicKey}
		if shortID := query.Get("sid"); shortID != "" {
			reality["short_id"] = shortID
		}
		tls["reality"] = reality
	}
	return tls
}

func transportFromQuery(query url.Values) map[string]any {
	transportType := query.Get("type")
	if transportType == "h2" ||
		(transportType == "tcp" && query.Get("headerType") == "http") {
		transportType = "http"
	}
	if transportType == "" || transportType == "tcp" {
		return nil
	}
	transport := map[string]any{"type": transportType}
	if path := query.Get("path"); path != "" {
		transport["path"] = path
	}
	if serviceName := query.Get("serviceName"); serviceName != "" {
		transport["service_name"] = serviceName
	}
	if host := query.Get("host"); host != "" {
		if transportType == "ws" {
			transport["headers"] = map[string]any{"Host": host}
		} else {
			transport["host"] = host
		}
	}
	if value := query.Get("max_early_data"); value != "" {
		if number, err := strconv.Atoi(value); err == nil && number > 0 {
			transport["max_early_data"] = number
		}
	}
	if value := query.Get("early_data_header_name"); value != "" {
		transport["early_data_header_name"] = value
	}
	return transport
}

func shadowsocksCredentials(parsed *url.URL) (string, string) {
	if parsed.User == nil {
		return "", ""
	}
	username := parsed.User.Username()
	if password, ok := parsed.User.Password(); ok {
		return username, password
	}
	decoded, err := base64.RawURLEncoding.DecodeString(username)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(username)
	}
	if err != nil {
		return "", ""
	}
	method, password, _ := strings.Cut(string(decoded), ":")
	return method, password
}

func truthy(value string) bool {
	return value == "1" || strings.EqualFold(value, "true")
}

func splitNonEmpty(value, separator string) []string {
	var result []string
	for _, item := range strings.Split(value, separator) {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
