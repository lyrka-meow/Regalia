package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSystemdLifecycle(t *testing.T) {
	directory := t.TempDir()
	binary := writeEngineScript(t, directory, `
case "$1" in
  check) exit 0 ;;
  *) exit 64 ;;
esac
`)
	controller := NewSystemd(
		binary,
		filepath.Join(directory, "runtime", "engine.json"),
		filepath.Join(directory, "runtime", "engine.log"),
		"regalia-engine@1000.service",
	)
	controller.startupGrace = time.Millisecond
	controller.jobTimeout = time.Second

	var mu sync.Mutex
	activeState := "inactive"
	controller.run = func(ctx context.Context, arguments ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch arguments[0] {
		case "show":
			pid := "0"
			timestamp := ""
			if activeState == "active" {
				pid = "4242"
				timestamp = "Sat 2026-08-01 12:00:00 MSK"
			}
			return []byte(fmt.Sprintf(
				"LoadState=loaded\nActiveState=%s\nSubState=running\nMainPID=%s\nResult=success\nExecMainStatus=0\nActiveEnterTimestamp=%s\n",
				activeState, pid, timestamp,
			)), nil
		case "start":
			activeState = "active"
			return nil, nil
		case "stop":
			activeState = "inactive"
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected systemctl arguments: %v", arguments)
		}
	}

	if status := controller.Status(); status.State != StateStopped || !status.Available {
		t.Fatalf("unexpected initial status: %#v", status)
	}
	if err := controller.Start([]byte(`{"test":true}`)); err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.State != StateConnected || status.PID != 4242 {
		t.Fatalf("unexpected connected status: %#v", status)
	}
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.State != StateStopped {
		t.Fatalf("unexpected stopped status: %#v", status)
	}
}

func TestSystemdUnavailableUnit(t *testing.T) {
	directory := t.TempDir()
	binary := writeEngineScript(t, directory, "exit 0\n")
	controller := NewSystemd(binary, filepath.Join(directory, "engine.json"), filepath.Join(directory, "engine.log"), "missing.service")
	controller.run = func(context.Context, ...string) ([]byte, error) {
		return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
	}
	status := controller.Status()
	if status.State != StateUnavailable || !strings.Contains(status.Error, "not installed") {
		t.Fatalf("unexpected unavailable status: %#v", status)
	}
}

func TestParseSystemdFailure(t *testing.T) {
	properties := parseProperties([]byte("LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainStatus=1\n"))
	if properties["ActiveState"] != "failed" || properties["ExecMainStatus"] != "1" {
		t.Fatalf("unexpected properties: %#v", properties)
	}
}
