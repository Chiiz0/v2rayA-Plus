//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || windows

package instancelock

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireBusy(t *testing.T, directory string) {
	t.Helper()
	lock, err := Acquire(directory)
	if lock != nil {
		_ = lock.Close()
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Acquire(%q) error = %v, want ErrAlreadyRunning", directory, err)
	}
}

func TestLockReentryAndStableFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, Filename)
	contents := []byte("existing lock file contents must survive\n")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	requireBusy(t, directory)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("Close replaced or removed the stable lock file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("lock file contents changed: %q, %v", got, err)
	}
	next, err := Acquire(directory)
	if err != nil {
		t.Fatalf("Acquire after Close: %v", err)
	}
	defer next.Close()
	// Re-closing the old handle must not release the replacement lock.
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	requireBusy(t, directory)
}

func TestLockCloseConcurrentAndNewFilePermissions(t *testing.T) {
	directory := t.TempDir()
	lock, err := Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, Filename))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("new lock mode = %o, want 600", info.Mode().Perm())
	}
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := lock.Close(); err != nil {
				t.Errorf("concurrent Close: %v", err)
			}
		}()
	}
	wait.Wait()
	next, err := Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	_ = next.Close()
	var nilLock *Lock
	if err := nilLock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := new(Lock).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLockDirectoryAliases(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "config")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "config-alias")
	if err := os.Symlink(directory, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	lock, err := Acquire(alias)
	if err != nil {
		t.Fatalf("Acquire through directory symlink: %v", err)
	}
	defer lock.Close()
	requireBusy(t, directory)
	requireBusy(t, alias)
	requireBusy(t, directory+string(os.PathSeparator)+".")
}

func TestLockDifferentDirectoriesAreIndependent(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	firstLock, err := Acquire(first)
	if err != nil {
		t.Fatal(err)
	}
	defer firstLock.Close()
	secondLock, err := Acquire(second)
	if err != nil {
		t.Fatalf("independent configuration directory was blocked: %v", err)
	}
	defer secondLock.Close()
	requireBusy(t, first)
	requireBusy(t, second)
}

func TestLockRequiresExistingDirectory(t *testing.T) {
	parent := t.TempDir()
	ordinary := filepath.Join(parent, "file")
	if err := os.WriteFile(ordinary, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(parent, "missing")
	for _, path := range []string{"", ordinary, missing} {
		lock, err := Acquire(path)
		if lock != nil {
			_ = lock.Close()
		}
		if err == nil || errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Acquire(%q) should reject an invalid directory, got %v", path, err)
		}
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("Acquire created missing directory: %v", err)
	}
}

func TestLockRejectsLockFileSymlinksAndDirectory(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling-symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, Filename)
			target := filepath.Join(directory, "target")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, path); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlink creation unavailable: %v", err)
					}
					t.Fatal(err)
				}
			}
			lock, err := Acquire(directory)
			if lock != nil {
				_ = lock.Close()
			}
			if err == nil || errors.Is(err, ErrAlreadyRunning) {
				t.Fatalf("Acquire should reject %s, got %v", kind, err)
			}
			if kind == "symlink" {
				got, err := os.ReadFile(target)
				if err != nil || string(got) != "untouched" {
					t.Fatalf("symlink target changed: %q %v", got, err)
				}
			} else if kind == "dangling-symlink" {
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatalf("dangling link target was created: %v", err)
				}
			}
		})
	}
}

const helperMode = "V2RAYA_INSTANCELOCK_TEST_MODE"
const helperDirectory = "V2RAYA_INSTANCELOCK_TEST_DIRECTORY"

func TestLockProcessHelper(t *testing.T) {
	mode := os.Getenv(helperMode)
	if mode == "" {
		return
	}
	lock, err := Acquire(os.Getenv(helperDirectory))
	if mode == "busy" && errors.Is(err, ErrAlreadyRunning) {
		os.Exit(0)
	}
	if mode == "invalid" && err != nil && !errors.Is(err, ErrAlreadyRunning) {
		os.Exit(0)
	}
	if mode != "hold" || err != nil {
		if lock != nil {
			_ = lock.Close()
		}
		fmt.Fprintf(os.Stderr, "helper mode %s: unexpected Acquire result: %v\n", mode, err)
		os.Exit(2)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	runtime.KeepAlive(lock)
	// Intentionally skip Close and all defers. Process exit must release locks.
	os.Exit(0)
}

func lockHelperCommand(ctx context.Context, directory, mode string) *exec.Cmd {
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProcessHelper$")
	command.Env = append(os.Environ(), helperMode+"="+mode, helperDirectory+"="+directory)
	return command
}

func TestLockCrossProcessContention(t *testing.T) {
	directory := t.TempDir()
	lock, err := Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := lockHelperCommand(ctx, directory, "busy").CombinedOutput(); err != nil {
		t.Fatalf("competing child: %v\n%s", err, output)
	}
}

func TestLockReleasedByProcessExit(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("kill=%t", force), func(t *testing.T) {
			directory := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := lockHelperCommand(ctx, directory, "hold")
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			command.Stderr = &diagnostic
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer command.Process.Kill()
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "locked" {
				_ = command.Wait()
				t.Fatalf("child did not acquire lock: %q, %v, %s", line, err, &diagnostic)
			}
			requireBusy(t, directory)
			if force {
				if err := command.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = input.Close()
			}
			err = command.Wait()
			if !force && err != nil {
				t.Fatalf("normal child exit: %v, %s", err, &diagnostic)
			}
			if ctx.Err() != nil {
				t.Fatalf("child timed out: %v", ctx.Err())
			}
			lock, err := Acquire(directory)
			if err != nil {
				t.Fatalf("Acquire after process exit: %v", err)
			}
			_ = lock.Close()
			if _, err := os.Stat(filepath.Join(directory, Filename)); err != nil {
				t.Fatalf("process exit removed lock file: %v", err)
			}
		})
	}
}
