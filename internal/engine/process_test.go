package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessLifecycle(t *testing.T) {
	directory := t.TempDir()
	binary := writeEngineScript(t, directory, `
case "$1" in
  check) exit 0 ;;
  run)
    trap 'exit 0' INT TERM
    while :; do sleep 0.1; done
    ;;
  *) exit 64 ;;
esac
`)
	configPath := filepath.Join(directory, "runtime", "engine.json")
	controller := NewProcess(binary, configPath, filepath.Join(directory, "engine.log"))
	controller.startupGrace = 30 * time.Millisecond
	controller.stopTimeout = time.Second

	if status := controller.Status(); status.State != StateStopped || !status.Available {
		t.Fatalf("unexpected initial status: %#v", status)
	}
	if err := controller.Start([]byte(`{"test":true}`)); err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.State != StateConnected || status.PID == 0 {
		t.Fatalf("unexpected connected status: %#v", status)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode is %o, want 600", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("runtime directory mode is %o, want 700", directoryInfo.Mode().Perm())
	}
	if err := controller.Start([]byte(`{}`)); err != ErrAlreadyRunning {
		t.Fatalf("second start returned %v, want ErrAlreadyRunning", err)
	}
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.State != StateStopped || status.PID != 0 {
		t.Fatalf("unexpected stopped status: %#v", status)
	}
}

func TestProcessReportsEarlyFailure(t *testing.T) {
	directory := t.TempDir()
	binary := writeEngineScript(t, directory, `
case "$1" in
  check) exit 0 ;;
  run) echo 'tun permission denied' >&2; exit 7 ;;
  *) exit 64 ;;
esac
`)
	controller := NewProcess(binary, filepath.Join(directory, "engine.json"), filepath.Join(directory, "engine.log"))
	controller.startupGrace = 100 * time.Millisecond

	err := controller.Start([]byte(`{"test":true}`))
	if err == nil || !strings.Contains(err.Error(), "tun permission denied") {
		t.Fatalf("unexpected start error: %v", err)
	}
	status := controller.Status()
	if status.State != StateFailed || !strings.Contains(status.Error, "exit status 7") {
		t.Fatalf("unexpected failed status: %#v", status)
	}
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.State != StateStopped {
		t.Fatalf("failed state was not cleared: %#v", status)
	}
}

func TestProcessRejectsInvalidConfiguration(t *testing.T) {
	directory := t.TempDir()
	binary := writeEngineScript(t, directory, `
case "$1" in
  check) echo 'invalid outbound' >&2; exit 1 ;;
  *) exit 64 ;;
esac
`)
	controller := NewProcess(binary, filepath.Join(directory, "engine.json"), filepath.Join(directory, "engine.log"))
	err := controller.Start([]byte(`{"broken":true}`))
	if err == nil || !strings.Contains(err.Error(), "invalid outbound") {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if status := controller.Status(); status.State != StateFailed || status.PID != 0 {
		t.Fatalf("unexpected failed status: %#v", status)
	}
}

func writeEngineScript(t *testing.T, directory, body string) string {
	t.Helper()
	path := filepath.Join(directory, "fake-sing-box")
	content := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
