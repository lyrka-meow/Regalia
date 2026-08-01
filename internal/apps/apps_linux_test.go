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

func TestAppImageDetection(t *testing.T) {
	if !isAppImage("/tmp/.mount_Test42/usr/bin/app") {
		t.Fatal("temporary AppImage mount was not detected")
	}
	if !isAppImage("/home/user/Example.AppImage") {
		t.Fatal("AppImage command was not detected")
	}
	if isAppImage("/usr/lib/chromium/chromium") {
		t.Fatal("ordinary application detected as AppImage")
	}
}

func TestProcessesIncludesCurrentExecutable(t *testing.T) {
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		current = resolved
	}
	for _, process := range Processes() {
		if process.ProcessPath == current {
			if process.ProcessCount < 1 {
				t.Fatalf("invalid process count: %#v", process)
			}
			return
		}
	}
	t.Fatalf("current executable %q was not discovered through /proc", current)
}

func TestProcessesIncludesExpectedExecutable(t *testing.T) {
	expected := os.Getenv("REGALIA_TEST_EXPECT_PROCESS")
	if expected == "" {
		t.Skip("REGALIA_TEST_EXPECT_PROCESS is not set")
	}
	for _, process := range Processes() {
		if process.ProcessPath == expected {
			return
		}
	}
	t.Fatalf("running executable %q was not discovered through /proc", expected)
}
