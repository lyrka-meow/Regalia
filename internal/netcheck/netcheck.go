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
	Kind      string `json:"kind,omitempty"`
	Interface string `json:"interface,omitempty"`
	Name      string `json:"name,omitempty"`
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
	Server          ServerContext `json:"server,omitempty"`
}

type Result struct {
	ID         string         `json:"id"`
	Mode       string         `json:"mode"`
	StartedAt  string         `json:"startedAt"`
	FinishedAt string         `json:"finishedAt"`
	Network    NetworkContext `json:"network,omitempty"`
	Results    []Measurement  `json:"results"`
	Compare    *Comparison    `json:"compare,omitempty"`
}

type Comparison struct {
	DownloadDeltaPct float64 `json:"downloadDeltaPct"`
	UploadDeltaPct   float64 `json:"uploadDeltaPct"`
	LatencyDeltaMs   float64 `json:"latencyDeltaMs"`
}

type Progress func(phase string, percent int)

func Run(ctx context.Context, route string, proxy Proxy, server ServerContext, progress Progress) (Measurement, error) {
	if route != "direct" && route != "proxy" {
		return Measurement{}, fmt.Errorf("unsupported route %q", route)
	}
	client, err := newClient(proxy)
	if err != nil {
		return Measurement{}, err
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	started := time.Now()
	measurement := Measurement{Route: route}
	if route == "proxy" {
		measurement.Server = server
	}

	progress("latency", 5)
	latencies := make([]float64, 0, latencyRuns)
	failures := 0
	for index := 0; index < latencyRuns; index++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, latencyURL, nil)
		if err != nil {
			return Measurement{}, err
		}
		request.Header.Set("User-Agent", "Regalia-Netcheck/1")
		start := time.Now()
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return Measurement{}, ctx.Err()
			}
			failures++
			continue
		}
		_, copyErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if copyErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			failures++
			continue
		}
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000)
		progress("latency", 5+(index+1)*20/latencyRuns)
	}
	if len(latencies) == 0 {
		return Measurement{}, errors.New("latency check failed on every request")
	}
	measurement.LatencyMs = median(latencies)
	measurement.JitterMs = jitter(latencies)
	measurement.HTTPErrorRate = float64(failures) * 100 / latencyRuns

	progress("download", 30)
	downloaded, downloadDuration, err := measureDownload(ctx, client)
	if err != nil {
		return Measurement{}, err
	}
	measurement.BytesDownloaded = downloaded
	measurement.DownloadMbps = megabitsPerSecond(downloaded, downloadDuration)

	progress("upload", 70)
	uploadDuration, err := measureUpload(ctx, client)
	if err != nil {
		return Measurement{}, err
	}
	measurement.BytesUploaded = uploadBytes
	measurement.UploadMbps = megabitsPerSecond(uploadBytes, uploadDuration)
	measurement.DurationMs = time.Since(started).Milliseconds()
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
