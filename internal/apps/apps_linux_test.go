package apps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstExecToken(t *testing.T) {
	tests := map[string]string{
		`chromium %U`:                    "chromium",
		`"/opt/My App/bin/app" --flag`:   "/opt/My App/bin/app",
		`env FOO=bar ignored`:            "env",
		`'single quoted app' --argument`: "single quoted app",
	}
	for input, expected := range tests {
		if actual := firstExecToken(input); actual != expected {
			t.Errorf("firstExecToken(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestApplicationExecTokenSkipsLaunchWrappers(t *testing.T) {
	tests := map[string]string{
		`prime-run blender %F`:                         "blender",
		`env FOO=bar BAZ=qux chromium %U`:              "chromium",
		`env -u WAYLAND_DISPLAY /opt/My\ App/bin/app`:  "/opt/My App/bin/app",
		`gamemoderun /usr/bin/game --fullscreen`:       "/usr/bin/game",
		`sh -c 'exec /opt/example/bin/example --flag'`: "/opt/example/bin/example",
	}
	for input, expected := range tests {
		if actual := applicationExecToken(input); actual != expected {
			t.Errorf("applicationExecToken(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestMatchingExecutableFindsCaseInsensitivePackagedBinary(t *testing.T) {
	root := t.TempDir()
	processPath := filepath.Join(root, "current", "Discord")
	if err := os.MkdirAll(filepath.Dir(processPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(processPath, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if actual := matchingExecutable(root, "discord", 2); actual != processPath {
		t.Fatalf("matchingExecutable() = %q, want %q", actual, processPath)
	}
}
