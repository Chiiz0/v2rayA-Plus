package backup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenRegularReadOnlyRejectsPipeWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	file, err := openRegularReadOnly(path)
	if file != nil {
		_ = file.Close()
	}
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("non-regular source was accepted or blocked: %v", err)
	}
}

func TestOpenRegularReadOnlyDoesNotCreateMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if _, err := openRegularReadOnly(path); !os.IsNotExist(err) {
		t.Fatalf("missing file error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only open created its source: %v", err)
	}
}
