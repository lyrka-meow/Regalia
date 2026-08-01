package apps

import "testing"

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
