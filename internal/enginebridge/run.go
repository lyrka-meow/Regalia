package enginebridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lyrka-meow/Regalia/internal/engineconfig"
)

func Run(configPath, binaryPath, logPath string) error {
	configuration, err := readPrivateConfiguration(configPath)
	if err != nil {
		return err
	}
	if err := engineconfig.Validate(configuration); err != nil {
		return fmt.Errorf("unsafe engine configuration: %w", err)
	}
	if err := validateSystemBinary(binaryPath); err != nil {
		return err
	}
	if err := check(binaryPath, configuration); err != nil {
		return err
	}
	logFile, err := openPrivateLog(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	command := exec.Command(binaryPath, "run", "--disable-color", "-c", "stdin")
	command.Stdin = bytes.NewReader(configuration)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Run(); err != nil {
		return fmt.Errorf("sing-box exited: %w", err)
	}
	return nil
}

func openPrivateLog(path string) (*os.File, error) {
	fileDescriptor, err := syscall.Open(
		path,
		syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open engine log: %w", err)
	}
	file := os.NewFile(uintptr(fileDescriptor), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect engine log: %w", err)
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !owned || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("engine log must be a private regular file owned by the service user")
	}
	return file, nil
}

func readPrivateConfiguration(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open engine configuration: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect engine configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("engine configuration is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("engine configuration mode is %o, group and other access must be disabled", info.Mode().Perm())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return nil, errors.New("engine configuration is not owned by the service user")
	}
	reader := io.LimitReader(file, engineconfig.MaxSize+1)
	configuration, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read engine configuration: %w", err)
	}
	if len(configuration) > engineconfig.MaxSize {
		return nil, fmt.Errorf("engine configuration exceeds %d bytes", engineconfig.MaxSize)
	}
	return configuration, nil
}

func validateSystemBinary(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("sing-box path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect sing-box: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("sing-box is not a regular executable")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("sing-box must not be writable by group or other users")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("sing-box must be owned by root")
	}
	return nil
}

func check(binaryPath string, configuration []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, "check", "--disable-color", "-c", "stdin")
	command.Stdin = bytes.NewReader(configuration)
	output, err := command.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("sing-box configuration check timed out")
	}
	if err != nil {
		detail := string(bytes.TrimSpace(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("sing-box rejected the configuration: %s", detail)
	}
	return nil
}
