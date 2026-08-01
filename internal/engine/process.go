package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxDiagnosticLength = 8 * 1024

type Process struct {
	operation    sync.Mutex
	mu           sync.Mutex
	binary       string
	configPath   string
	logPath      string
	startupGrace time.Duration
	stopTimeout  time.Duration
	status       Status
	command      *exec.Cmd
	done         chan struct{}
}

func NewProcess(binary, configPath, logPath string) *Process {
	controller := &Process{
		binary:       binary,
		configPath:   configPath,
		logPath:      logPath,
		startupGrace: 400 * time.Millisecond,
		stopTimeout:  5 * time.Second,
		status:       Status{State: StateUnavailable},
	}
	controller.refreshAvailabilityLocked()
	return controller
}

func (p *Process) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status.State == StateUnavailable {
		p.refreshAvailabilityLocked()
	}
	return p.status
}

func (p *Process) Start(configuration []byte) error {
	p.operation.Lock()
	defer p.operation.Unlock()

	p.mu.Lock()
	if Active(p.status) {
		p.mu.Unlock()
		return ErrAlreadyRunning
	}
	p.refreshAvailabilityLocked()
	if !p.status.Available {
		err := errors.New(p.status.Error)
		p.mu.Unlock()
		return err
	}
	binary := p.binary
	if resolved, err := exec.LookPath(binary); err == nil {
		binary = resolved
	}
	p.status = Status{State: StateStarting, Available: true}
	p.mu.Unlock()

	if err := writePrivateFile(p.configPath, configuration); err != nil {
		return p.fail(fmt.Errorf("write engine configuration: %w", err))
	}
	if err := checkConfiguration(binary, p.configPath); err != nil {
		return p.fail(err)
	}
	if err := ensurePrivateDirectory(filepath.Dir(p.logPath)); err != nil {
		return p.fail(fmt.Errorf("create engine log directory: %w", err))
	}
	logFile, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return p.fail(fmt.Errorf("open engine log: %w", err))
	}
	if err := logFile.Chmod(0o600); err != nil {
		_ = logFile.Close()
		return p.fail(fmt.Errorf("protect engine log: %w", err))
	}

	command := exec.Command(binary, "run", "--disable-color", "-c", p.configPath)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return p.fail(fmt.Errorf("start sing-box: %w", err))
	}
	_ = logFile.Close()

	done := make(chan struct{})
	p.mu.Lock()
	p.command = command
	p.done = done
	p.status.PID = command.Process.Pid
	p.status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	p.mu.Unlock()
	go p.wait(command, done)

	timer := time.NewTimer(p.startupGrace)
	defer timer.Stop()
	select {
	case <-done:
		status := p.Status()
		if status.Error != "" {
			return errors.New(status.Error)
		}
		return errors.New("sing-box exited during startup")
	case <-timer.C:
		p.mu.Lock()
		if p.command == command && p.status.State == StateStarting {
			p.status.State = StateConnected
		}
		status := p.status
		p.mu.Unlock()
		if status.State != StateConnected {
			if status.Error != "" {
				return errors.New(status.Error)
			}
			return fmt.Errorf("sing-box entered unexpected state %s", status.State)
		}
		return nil
	}
}

func (p *Process) Stop() error {
	p.operation.Lock()
	defer p.operation.Unlock()

	p.mu.Lock()
	switch p.status.State {
	case StateUnavailable, StateStopped:
		p.mu.Unlock()
		return ErrNotRunning
	case StateFailed:
		p.status.State = StateStopped
		p.status.Error = ""
		p.status.PID = 0
		p.status.StartedAt = ""
		p.mu.Unlock()
		return nil
	}
	command := p.command
	done := p.done
	p.status.State = StateStopping
	p.mu.Unlock()

	if command == nil || command.Process == nil {
		return p.fail(errors.New("engine process is missing while marked active"))
	}
	if err := command.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = command.Process.Kill()
	}

	timer := time.NewTimer(p.stopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return p.fail(fmt.Errorf("kill unresponsive sing-box: %w", err))
		}
		<-done
		return nil
	}
}

func checkConfiguration(binary, configPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "check", "--disable-color", "-c", configPath)
	output, err := command.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("sing-box configuration check timed out")
	}
	if err != nil {
		detail := trimDiagnostic(output)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("sing-box rejected the configuration: %s", detail)
	}
	return nil
}

func (p *Process) wait(command *exec.Cmd, done chan struct{}) {
	err := command.Wait()
	p.mu.Lock()
	if p.command != command {
		p.mu.Unlock()
		close(done)
		return
	}
	wasStopping := p.status.State == StateStopping
	p.command = nil
	p.done = nil
	p.status.PID = 0
	p.status.StartedAt = ""
	if wasStopping {
		p.status.State = StateStopped
		p.status.Error = ""
	} else {
		p.status.State = StateFailed
		p.status.Error = p.exitDiagnostic(err)
	}
	p.mu.Unlock()
	close(done)
}

func (p *Process) fail(err error) error {
	p.mu.Lock()
	p.status.State = StateFailed
	p.status.Error = err.Error()
	p.status.PID = 0
	p.status.StartedAt = ""
	p.command = nil
	p.done = nil
	p.mu.Unlock()
	return err
}

func (p *Process) refreshAvailabilityLocked() {
	_, err := exec.LookPath(p.binary)
	if err != nil {
		p.status = Status{
			State: StateUnavailable,
			Error: fmt.Sprintf("sing-box executable %q was not found", p.binary),
		}
		return
	}
	if p.status.State == StateUnavailable {
		p.status = Status{State: StateStopped, Available: true}
	} else {
		p.status.Available = true
	}
}

func (p *Process) exitDiagnostic(waitError error) string {
	detail := "sing-box exited unexpectedly"
	if waitError != nil {
		detail += ": " + waitError.Error()
	}
	if logTail := readTail(p.logPath, maxDiagnosticLength); logTail != "" {
		detail += ": " + logTail
	}
	return detail
}

func writePrivateFile(path string, content []byte) error {
	if len(bytes.TrimSpace(content)) == 0 {
		return errors.New("configuration is empty")
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".engine-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func readTail(path string, limit int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, 0); err != nil {
		return ""
	}
	buffer := make([]byte, limit)
	count, _ := file.Read(buffer)
	return trimDiagnostic(buffer[:count])
}

func trimDiagnostic(value []byte) string {
	text := strings.TrimSpace(string(value))
	if len(text) > maxDiagnosticLength {
		text = text[len(text)-maxDiagnosticLength:]
	}
	return strings.Join(strings.Fields(text), " ")
}
