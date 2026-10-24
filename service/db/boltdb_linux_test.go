package db

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDatabaseRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	for _, noFollow := range []bool{false, true} {
		finished := make(chan error, 1)
		go func() {
			file, err := openRegularDatabaseFile(path, os.O_RDONLY, 0, noFollow)
			if file != nil {
				_ = file.Close()
			}
			finished <- err
		}()
		select {
		case err := <-finished:
			if err == nil {
				t.Fatal("opened FIFO as a database")
			}
		case <-time.After(time.Second):
			t.Fatal("FIFO open blocked before Bolt lock handling")
		}
	}
}
