package netcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	downloadURL   = "https://speed.cloudflare.com/__down?bytes=5000000"
	latencyURL    = "https://speed.cloudflare.com/__down?bytes=0"
	uploadURL     = "https://speed.cloudflare.com/__up"
	latencyRuns   = 6
	downloadBytes = 5_000_000
	uploadBytes   = 2_000_000
)

type Proxy struct {
	Port     int
	Username string
	Password string
}

type ServerContext struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type NetworkContext struct {
	Kind           string   `json:"kind,omitempty"`
	Interface      string   `json:"interface,omitempty"`
	Name           string   `json:"name,omitempty"`
	SignalPercent  int      `json:"signalPercent,omitempty"`
	SignalStartDBm *float64 `json:"signalStartDbm,omitempty"`
	SignalEndDBm   *float64 `json:"signalEndDbm,omitempty"`
}

type Measurement struct {
	Route           string        `json:"route"`
	DownloadMbps    float64       `json:"downloadMbps"`
	UploadMbps      float64       `json:"uploadMbps"`
	LatencyMs       float64       `json:"latencyMs"`
	JitterMs        float64       `json:"jitterMs"`
	HTTPErrorRate   float64       `json:"httpErrorRate"`
	BytesDownloaded int64         `json:"bytesDownloaded"`
	BytesUploaded   int64         `json:"bytesUploaded"`
	DurationMs      int64         `json:"durationMs"`
	MeasuredAt      string        `json:"measuredAt"`
	Rating          string        `json:"rating"`
	LatencySamples  int           `json:"latencySamples"`
	Server          ServerContext `json:"server,omitempty"`
}

type Result struct {
	ID          string         `json:"id"`
	Mode        string         `json:"mode"`
	Status      string         `json:"status,omitempty"`
	StartedAt   string         `json:"startedAt"`
	FinishedAt  string         `json:"finishedAt"`
	Network     NetworkContext `json:"network,omitempty"`
	Results     []Measurement  `json:"results"`
	Compare     *Comparison    `json:"compare,omitempty"`
	Reliability string         `json:"reliability,omitempty"`
	Warnings    []string       `json:"warnings,omitempty"`
	ErrorCode   string         `json:"errorCode,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type Comparison struct {
	DownloadDeltaPct float64 `json:"downloadDeltaPct"`
	UploadDeltaPct   float64 `json:"uploadDeltaPct"`
	LatencyDeltaMs   float64 `json:"latencyDeltaMs"`
}

type Progress func(phase string, percent int)
type ComparisonProgress func(route, phase string, percent int)

type testFailure struct {
	code string
	err  error
}

func (failure *testFailure) Error() string {
	return failure.err.Error()
}

func (failure *testFailure) Unwrap() error {
	return failure.err
}

func NewFailure(code string, err error) error {
	return &testFailure{code: code, err: err}
}

type routeRunner struct {
	route     string
	server    ServerContext
	client    *http.Client
	latencies []float64
	failures  int
	duration  time.Duration
}

func Run(ctx context.Context, route string, proxy Proxy, server ServerContext, progress Progress) (Measurement, error) {
	runner, err := newRouteRunner(route, proxy, server)
	if err != nil {
		return Measurement{}, err
	}
	defer runner.close()
	if err := runner.warmUpLatency(ctx); err != nil {
		return Measurement{}, err
	}
	progress("latency", 5)
	for index := 0; index < latencyRuns; index++ {
		if err := runner.measureLatency(ctx); err != nil {
			return Measurement{}, err
		}
		progress("latency", 5+(index+1)*20/latencyRuns)
	}
	if err := runner.finishLatency(); err != nil {
		return Measurement{}, err
	}
	return runner.measureThroughput(ctx, progress)
}

// RunComparison alternates direct and VPN latency requests so both routes are
// sampled under nearly the same radio conditions. Throughput remains
// sequential to avoid the two routes competing for the same connection.
func RunComparison(ctx context.Context, directProxy, vpnProxy Proxy, server ServerContext, progress ComparisonProgress) ([]Measurement, error) {
	direct, err := newRouteRunner("direct", directProxy, server)
	if err != nil {
		return nil, err
	}
	defer direct.close()
	vpn, err := newRouteRunner("proxy", vpnProxy, server)
	if err != nil {
		return nil, err
	}
	defer vpn.close()
	runners := []*routeRunner{direct, vpn}
	for _, runner := range runners {
		if err := runner.warmUpLatency(ctx); err != nil {
			return nil, fmt.Errorf("%s route warm-up: %w", runner.route, err)
		}
	}
	completedLatencyRequests := 0
	for index := 0; index < latencyRuns; index++ {
		order := []int{0, 1}
		if index%2 == 1 {
			order = []int{1, 0}
		}
		for _, routeIndex := range order {
			runner := runners[routeIndex]
			completedLatencyRequests++
			progress(runner.route, "latency", 5+completedLatencyRequests*20/(latencyRuns*2))
			if err := runner.measureLatency(ctx); err != nil {
				return nil, fmt.Errorf("%s route: %w", runner.route, err)
			}
		}
	}
	for _, runner := range runners {
		if err := runner.finishLatency(); err != nil {
			return nil, fmt.Errorf("%s route: %w", runner.route, err)
		}
	}
	measurements := make([]Measurement, 0, 2)
	for routeIndex, runner := range runners {
		measurement, err := runner.measureThroughput(ctx, func(phase string, percent int) {
			overall := 25 + routeIndex*37 + percent*37/100
			progress(runner.route, phase, overall)
		})
		if err != nil {
			return measurements, fmt.Errorf("%s route: %w", runner.route, err)
		}
		measurements = append(measurements, measurement)
	}
	return measurements, nil
}

func newRouteRunner(route string, proxy Proxy, server ServerContext) (*routeRunner, error) {
	if route != "direct" && route != "proxy" {
		return nil, fmt.Errorf("unsupported route %q", route)
	}
	client, err := newClient(proxy)
	if err != nil {
		return nil, err
	}
	return &routeRunner{route: route, server: server, client: client, latencies: make([]float64, 0, latencyRuns)}, nil
}

func (runner *routeRunner) close() {
	if transport, ok := runner.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

// warmUpLatency establishes DNS, TCP and TLS state before samples are kept.
// Otherwise the first cold request can look like severe jitter even on a
// stable connection and distort the direct/VPN comparison.
func (runner *routeRunner) warmUpLatency(ctx context.Context) error {
	if err := runner.measureLatency(ctx); err != nil {
		return err
	}
	runner.latencies = runner.latencies[:0]
	runner.failures = 0
	runner.duration = 0
	return nil
}

func (runner *routeRunner) measureLatency(ctx context.Context) error {
	requestContext, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, latencyURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "Regalia-Netcheck/1")
	started := time.Now()
	response, err := runner.client.Do(request)
	runner.duration += time.Since(started)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		runner.failures++
		return nil
	}
	_, copyErr := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if copyErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		runner.failures++
		return nil
	}
	runner.latencies = append(runner.latencies, float64(time.Since(started).Microseconds())/1000)
	return nil
}

func (runner *routeRunner) finishLatency() error {
	if len(runner.latencies) == 0 {
		return &testFailure{code: "latency_unavailable", err: errors.New("latency check failed on every request")}
	}
	return nil
}

func (runner *routeRunner) measureThroughput(ctx context.Context, progress Progress) (Measurement, error) {
	measurement := Measurement{
		Route:          runner.route,
		LatencyMs:      median(runner.latencies),
		JitterMs:       jitter(runner.latencies),
		HTTPErrorRate:  float64(runner.failures) * 100 / latencyRuns,
		LatencySamples: len(runner.latencies),
	}
	if runner.route == "proxy" {
		measurement.Server = runner.server
	}

	progress("download", 30)
	downloaded, downloadDuration, err := measureDownload(ctx, runner.client)
	if err != nil {
		return Measurement{}, &testFailure{code: "download_failed", err: err}
	}
	runner.duration += downloadDuration
	measurement.BytesDownloaded = downloaded
	measurement.DownloadMbps = megabitsPerSecond(downloaded, downloadDuration)

	progress("upload", 70)
	uploadDuration, err := measureUpload(ctx, runner.client)
	if err != nil {
		return Measurement{}, &testFailure{code: "upload_failed", err: err}
	}
	runner.duration += uploadDuration
	measurement.BytesUploaded = uploadBytes
	measurement.UploadMbps = megabitsPerSecond(uploadBytes, uploadDuration)
	measurement.DurationMs = runner.duration.Milliseconds()
	measurement.MeasuredAt = time.Now().UTC().Format(time.RFC3339)
	measurement.Rating = Rating(measurement)
	progress("done", 100)
	return roundMeasurement(measurement), nil
}

func measureDownload(ctx context.Context, client *http.Client) (int64, time.Duration, error) {
	var lastError error
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return 0, 0, err
		}
		request.Header.Set("User-Agent", "Regalia-Netcheck/1")
		started := time.Now()
		response, err := client.Do(request)
		if err != nil {
			lastError = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		downloaded, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, downloadBytes+1))
		response.Body.Close()
		if copyErr != nil {
			lastError = copyErr
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			lastError = fmt.Errorf("HTTP %d", response.StatusCode)
			continue
		}
		return downloaded, time.Since(started), nil
	}
	return 0, 0, fmt.Errorf("download check: %w", lastError)
}

func measureUpload(ctx context.Context, client *http.Client) (time.Duration, error) {
	var lastError error
	for attempt := 0; attempt < 2; attempt++ {
		payload := bytes.NewReader(make([]byte, uploadBytes))
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, payload)
		if err != nil {
			return 0, err
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("User-Agent", "Regalia-Netcheck/1")
		started := time.Now()
		response, err := client.Do(request)
		if err != nil {
			lastError = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		_, copyErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if copyErr != nil {
			lastError = copyErr
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			lastError = fmt.Errorf("HTTP %d", response.StatusCode)
			continue
		}
		return time.Since(started), nil
	}
	return 0, fmt.Errorf("upload check: %w", lastError)
}

func Compare(result *Result) {
	if len(result.Results) != 2 {
		return
	}
	var direct, proxy *Measurement
	for index := range result.Results {
		switch result.Results[index].Route {
		case "direct":
			direct = &result.Results[index]
		case "proxy":
			proxy = &result.Results[index]
		}
	}
	if direct == nil || proxy == nil {
		return
	}
	result.Compare = &Comparison{
		DownloadDeltaPct: round1(percentDelta(proxy.DownloadMbps, direct.DownloadMbps)),
		UploadDeltaPct:   round1(percentDelta(proxy.UploadMbps, direct.UploadMbps)),
		LatencyDeltaMs:   round1(proxy.LatencyMs - direct.LatencyMs),
	}
}

func FailureCode(err error) string {
	if err == nil {
		return ""
	}
	var failure *testFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "test_timeout"
	}
	return "connection_failed"
}

// Assess marks measurements that should not be used for conclusions such as
// "VPN has lower latency". Numerical values remain available for diagnostics.
func Assess(result *Result) {
	warnings := make([]string, 0, 4)
	seen := map[string]bool{}
	addWarning := func(warning string) {
		if warning != "" && !seen[warning] {
			seen[warning] = true
			warnings = append(warnings, warning)
		}
	}
	if result.Network.Kind == "wifi" {
		if signalAtOrBelow(result.Network.SignalStartDBm, -75) || signalAtOrBelow(result.Network.SignalEndDBm, -75) ||
			(result.Network.SignalStartDBm == nil && result.Network.SignalEndDBm == nil &&
				result.Network.SignalPercent > 0 && result.Network.SignalPercent <= 35) {
			addWarning("weak_wifi_signal")
		}
		if result.Network.SignalStartDBm != nil && result.Network.SignalEndDBm != nil &&
			math.Abs(*result.Network.SignalStartDBm-*result.Network.SignalEndDBm) >= 10 {
			addWarning("wifi_signal_changed")
		}
	}
	for _, measurement := range result.Results {
		if measurement.JitterMs > math.Max(75, measurement.LatencyMs*0.75) {
			addWarning("unstable_latency")
		}
		if measurement.HTTPErrorRate > 0 {
			addWarning("request_errors")
		}
	}
	if latencyDifferenceIsWithinNoise(result.Results) {
		addWarning("unstable_latency")
	}
	if result.Status == "failed" {
		addWarning(result.ErrorCode)
	}
	result.Warnings = warnings
	if len(warnings) > 0 {
		result.Reliability = "unstable"
	} else {
		result.Reliability = "reliable"
	}
}

func latencyDifferenceIsWithinNoise(measurements []Measurement) bool {
	var direct, proxy *Measurement
	for index := range measurements {
		switch measurements[index].Route {
		case "direct":
			direct = &measurements[index]
		case "proxy":
			proxy = &measurements[index]
		}
	}
	if direct == nil || proxy == nil {
		return false
	}
	noise := math.Max(direct.JitterMs, proxy.JitterMs)
	return noise > 0 && math.Abs(proxy.LatencyMs-direct.LatencyMs) <= noise
}

func signalAtOrBelow(value *float64, threshold float64) bool {
	return value != nil && *value <= threshold
}

func Rating(value Measurement) string {
	if value.HTTPErrorRate >= 20 || value.LatencyMs >= 250 || value.DownloadMbps < 3 {
		return "poor"
	}
	if value.LatencyMs >= 120 || value.DownloadMbps < 10 || value.UploadMbps < 2 || value.JitterMs >= 50 {
		return "fair"
	}
	if value.LatencyMs <= 45 && value.DownloadMbps >= 50 && value.UploadMbps >= 10 && value.JitterMs <= 15 {
		return "excellent"
	}
	return "good"
}

func Encode(result Result) ([]byte, error) {
	return json.Marshal(result)
}

func newClient(proxy Proxy) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = false
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.IdleConnTimeout = 20 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	if proxy.Port > 0 {
		proxyURL, err := url.Parse(fmt.Sprintf("http://%s:%s@127.0.0.1:%d",
			url.PathEscape(proxy.Username), url.PathEscape(proxy.Password), proxy.Port))
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport, Timeout: 25 * time.Second}, nil
}

func median(values []float64) float64 {
	copyValues := slices.Clone(values)
	sort.Float64s(copyValues)
	middle := len(copyValues) / 2
	if len(copyValues)%2 == 0 {
		return (copyValues[middle-1] + copyValues[middle]) / 2
	}
	return copyValues[middle]
}

func jitter(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var total float64
	for index := 1; index < len(values); index++ {
		total += math.Abs(values[index] - values[index-1])
	}
	return total / float64(len(values)-1)
}

func megabitsPerSecond(bytes int64, duration time.Duration) float64 {
	if duration <= 0 {
		return 0
	}
	return float64(bytes*8) / duration.Seconds() / 1_000_000
}

func percentDelta(value, baseline float64) float64 {
	if baseline <= 0 {
		return 0
	}
	return (value - baseline) * 100 / baseline
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}

func roundMeasurement(value Measurement) Measurement {
	value.DownloadMbps = round1(value.DownloadMbps)
	value.UploadMbps = round1(value.UploadMbps)
	value.LatencyMs = round1(value.LatencyMs)
	value.JitterMs = round1(value.JitterMs)
	value.HTTPErrorRate = round1(value.HTTPErrorRate)
	return value
}

func NormalizeMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "direct", "proxy", "compare":
		return mode, nil
	default:
		return "", fmt.Errorf("mode must be direct, proxy, or compare")
	}
}
