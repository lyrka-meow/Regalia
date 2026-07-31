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
