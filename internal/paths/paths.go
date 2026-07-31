package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

func Socket() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "regalia", "regaliad.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("regalia-%d", os.Getuid()), "regaliad.sock")
}

func State() (string, error) {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "regalia", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "regalia", "state.json"), nil
}
