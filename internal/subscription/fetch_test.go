package subscription

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAlwaysSendsCompatibleDeviceHeaders(t *testing.T) {
	want := deviceDetails{
		hwid:      "machine-id",
		os:        "Linux",
		osVersion: "6.18.1-arch1-1",
		model:     "Arch Linux",
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		checks := map[string]string{
			"x-hwid":         want.hwid,
			"x-device-os":    want.os,
			"x-ver-os":       want.osVersion,
			"x-device-model": want.model,
		}
		for name, expected := range checks {
			if actual := request.Header.Get(name); actual != expected {
				t.Errorf("%s = %q, want %q", name, actual, expected)
			}
		}
		payload := base64.StdEncoding.EncodeToString([]byte("trojan://secret@example.com:443#Test"))
		_, _ = writer.Write([]byte(payload))
	}))
	defer server.Close()

	fetcher := NewFetcher()
	fetcher.device = want
	servers, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "Test" {
		t.Fatalf("Fetch() servers = %#v", servers)
	}
}

func TestDeviceHeadersRejectLineBreaksAndLimitLength(t *testing.T) {
	header := http.Header{}
	applyDeviceHeaders(header, deviceDetails{
		hwid:      "abc\r\ndef",
		os:        "Linux",
		osVersion: "version",
		model:     string(make([]byte, 600)),
	})
	if got := header.Get("x-hwid"); got != "abcdef" {
		t.Fatalf("x-hwid = %q", got)
	}
	if got := len(header.Get("x-device-model")); got != 512 {
		t.Fatalf("x-device-model length = %d, want 512", got)
	}
}
