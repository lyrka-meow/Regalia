package subscription

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const maximumSubscriptionSize = 16 << 20

type Fetcher struct {
	client *http.Client
	device deviceDetails
}

type deviceDetails struct {
	hwid      string
	os        string
	osVersion string
	model     string
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(request *http.Request, previous []*http.Request) error {
				if len(previous) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return validateRemoteURL(request.URL)
			},
		},
		device: detectDeviceDetails(),
	}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]Server, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid subscription URL: %w", err)
	}
	if err := validateRemoteURL(parsed); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Regalia/0 (Linux; subscription client)")
	request.Header.Set("Accept", "text/plain, application/json, application/yaml, */*")
	applyDeviceHeaders(request.Header, f.device)

	response, err := f.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download subscription: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription server returned HTTP %d", response.StatusCode)
	}
	reader := io.LimitReader(response.Body, maximumSubscriptionSize+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read subscription: %w", err)
	}
	if len(body) > maximumSubscriptionSize {
		return nil, fmt.Errorf("subscription is larger than %d MiB", maximumSubscriptionSize>>20)
	}
	return Parse(body)
}

func applyDeviceHeaders(header http.Header, details deviceDetails) {
	setSafeHeader(header, "x-hwid", details.hwid)
	setSafeHeader(header, "x-device-os", details.os)
	setSafeHeader(header, "x-ver-os", details.osVersion)
	setSafeHeader(header, "x-device-model", details.model)
}

func setSafeHeader(header http.Header, name, value string) {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	if len(value) > 512 {
		value = value[:512]
	}
	if value != "" {
		header.Set(name, value)
	}
}

func detectDeviceDetails() deviceDetails {
	return deviceDetails{
		hwid:      firstFileValue("/etc/machine-id", "/var/lib/dbus/machine-id"),
		os:        linuxName(),
		osVersion: firstFileValue("/proc/sys/kernel/osrelease"),
		model:     osPrettyName(),
	}
}

func linuxName() string {
	if runtime.GOOS == "linux" {
		return "Linux"
	}
	return runtime.GOOS
}

func firstFileValue(paths ...string) string {
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimSpace(string(raw))
		}
	}
	return ""
}

func osPrettyName() string {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return linuxName()
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"")
		values[strings.TrimSpace(key)] = value
	}
	if values["PRETTY_NAME"] != "" {
		return values["PRETTY_NAME"]
	}
	if values["NAME"] != "" {
		return values["NAME"]
	}
	return linuxName()
}

func validateRemoteURL(parsed *url.URL) error {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("subscription URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("subscription URL has no host")
	}
	return nil
}
