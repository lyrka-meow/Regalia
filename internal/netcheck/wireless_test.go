package netcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseWirelessSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wireless")
	content := "Inter-| sta-| Quality\n face | tus | link level noise\n wlan0: 0000   48.  -62.  -256  0  0\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	signal := parseWirelessSignal(file, "wlan0")
	if signal == nil || *signal != -62 {
		t.Fatalf("signal = %v, want -62", signal)
	}
}
