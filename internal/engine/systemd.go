package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type systemctlRunner func(context.Context, ...string) ([]byte, error)

type Systemd struct {
	operation    sync.Mutex
	binary       string
	configPath   string
	logPath      string
	unit         string
	startupGrace time.Duration
	jobTimeout   time.Duration
	run          systemctlRunner
}

func NewSystemd(binary, configPath, logPath, unit string) *Systemd {
	return &Systemd{
		binary:       binary,
		configPath:   configPath,
		logPath:      logPath,
		unit:         unit,
		startupGrace: 400 * time.Millisecond,
		jobTimeout:   45 * time.Second,
		run:          runSystemctl,
	}
}

func (s *Systemd) Status() Status {
	if _, err := exec.LookPath(s.binary); err != nil {
		return Status{
			State: StateUnavailable,
			Error: fmt.Sprintf("sing-box executable %q was not found", s.binary),
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := s.run(ctx,
		"show", "--no-pager",
		"--property=LoadState",
		"--property=ActiveState",
		"--property=SubState",
		"--property=MainPID",
		"--property=Result",
		"--property=ExecMainStatus",
		"--property=ActiveEnterTimestamp",
		s.unit,
	)
	if err != nil {
		detail := trimDiagnostic(output)
		if detail == "" {
			detail = err.Error()
		}
		return Status{State: StateUnavailable, Error: "query systemd engine unit: " + detail}
	}
	properties := parseProperties(output)
	if properties["LoadState"] != "loaded" {
		return Status{State: StateUnavailable, Error: fmt.Sprintf("systemd unit %s is not installed", s.unit)}
	}
	status := Status{Available: true}
	status.PID, _ = strconv.Atoi(properties["MainPID"])
	status.StartedAt = properties["ActiveEnterTimestamp"]
	switch properties["ActiveState"] {
	case "active":
		status.State = StateConnected
	case "activating", "reloading":
		status.State = StateStarting
	case "deactivating":
		status.State = StateStopping
	case "failed":
		status.State = StateFailed
		status.PID = 0
		status.StartedAt = ""
		status.Error = s.failureDiagnostic(properties)
	default:
		status.State = StateStopped
		status.PID = 0
		status.StartedAt = ""
	}
	return status
}

func (s *Systemd) Start(configuration []byte) error {
	s.operation.Lock()
	defer s.operation.Unlock()
	status := s.Status()
	if Active(status) {
		return ErrAlreadyRunning
	}
	if !status.Available {
		return errors.New(status.Error)
	}
	if err := writePrivateFile(s.configPath, configuration); err != nil {
		return fmt.Errorf("write engine configuration: %w", err)
	}
	if err := checkConfiguration(s.binary, s.configPath); err != nil {
		return err
	}
	if err := truncatePrivateFile(s.logPath); err != nil {
		return fmt.Errorf("prepare engine log: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout)
	output, err := s.run(ctx, "start", s.unit)
	cancel()
	if err != nil {
		detail := trimDiagnostic(output)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("start systemd engine unit: %s", detail)
	}
	timer := time.NewTimer(s.startupGrace)
	<-timer.C
	status = s.Status()
	if status.State != StateConnected {
		if status.Error != "" {
			return errors.New(status.Error)
		}
		return fmt.Errorf("systemd engine unit entered state %s", status.State)
	}
	return nil
}

func (s *Systemd) Stop() error {
	s.operation.Lock()
	defer s.operation.Unlock()
	status := s.Status()
	switch status.State {
	case StateUnavailable, StateStopped:
		return ErrNotRunning
	case StateFailed:
		return s.systemctl("reset-failed")
	}
	if err := s.systemctl("stop"); err != nil {
		return err
	}
	status = s.Status()
	if status.State != StateStopped {
		if status.Error != "" {
			return errors.New(status.Error)
		}
		return fmt.Errorf("systemd engine unit entered state %s", status.State)
	}
	return nil
}

func (s *Systemd) systemctl(verb string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout)
	defer cancel()
	output, err := s.run(ctx, verb, s.unit)
	if err == nil {
		return nil
	}
	detail := trimDiagnostic(output)
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("systemctl %s %s: %s", verb, s.unit, detail)
}

func (s *Systemd) failureDiagnostic(properties map[string]string) string {
	detail := "systemd engine unit failed"
	if result := properties["Result"]; result != "" && result != "success" {
		detail += ": result=" + result
	}
	if exitStatus := properties["ExecMainStatus"]; exitStatus != "" && exitStatus != "0" {
		detail += ", status=" + exitStatus
	}
	if logTail := readTail(s.logPath, maxDiagnosticLength); logTail != "" {
		detail += ": " + logTail
	}
	return detail
}

func runSystemctl(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "systemctl", arguments...)
	return command.CombinedOutput()
}

func parseProperties(output []byte) map[string]string {
	properties := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			properties[key] = value
		}
	}
	return properties
}

func truncatePrivateFile(path string) error {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
