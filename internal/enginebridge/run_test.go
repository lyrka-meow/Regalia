package enginebridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPrivateConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.json")
	if err := os.WriteFile(path, []byte(`{"safe":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := readPrivateConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != `{"safe":true}` {
		t.Fatalf("unexpected content: %s", content)
	}
}

func TestReadPrivateConfigurationRejectsOpenMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readPrivateConfiguration(path)
	if err == nil || !strings.Contains(err.Error(), "group and other access") {
		t.Fatalf("unexpected mode error: %v", err)
	}
}

func TestOpenPrivateLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.log")
	file, err := openPrivateLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected log mode: %o", info.Mode().Perm())
	}
}

func TestOpenPrivateLogRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "engine.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openPrivateLog(link); err == nil {
		t.Fatal("expected symlink log to be rejected")
	}
}

func TestPrivateLogIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.log")
	file, err := openPrivateLog(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("x", 64*1024)
	for index := 0; index < 24; index++ {
		if _, err := file.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxEngineLogSize {
		t.Fatalf("engine log grew to %d bytes", info.Size())
	}
}
