package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fakeDockerCommand(t *testing.T, mode string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("V2RAYA_COMMAND_TEST_DIR", dir)
	t.Setenv("V2RAYA_COMMAND_TEST_MODE", mode)
	t.Setenv("V2RAYA_COMMAND_TEST_CLEANUP", "")
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$V2RAYA_COMMAND_TEST_DIR/calls"
printf '%s\000' "$@" > "$V2RAYA_COMMAND_TEST_DIR/$1-args"
if [ "$1" = rm ]; then
  if [ "$V2RAYA_COMMAND_TEST_CLEANUP" = hang ]; then exec sleep 60; fi
  exit 0
fi
if [ "$1" = run ]; then
  cat > "$V2RAYA_COMMAND_TEST_DIR/input"
  cat "$V2RAYA_COMMAND_TEST_DIR/input"
  cat "$V2RAYA_COMMAND_TEST_DIR/input" >&2
else
  printf 'docker stdout\n'
fi
case "$V2RAYA_COMMAND_TEST_MODE" in
  hang) exec sleep 60 ;;
  child)
    sleep 60 &
    printf '%s' "$!" > "$V2RAYA_COMMAND_TEST_DIR/child-pid"
    wait
    ;;
  error) printf 'container is already paused\n' >&2; exit 7 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func dockerCommandArgs(t *testing.T, dir, command string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, command+"-args"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

func TestRunDockerCommandDeadline(t *testing.T) {
	dir := fakeDockerCommand(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runDockerCmdContext(ctx, "inspect", "v2raya-socks-19001")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung Docker command error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("hung Docker CLI did not stop promptly: %v", elapsed)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil || string(calls) != "inspect\n" {
		t.Fatalf("ordinary command cancellation must not remove a proxy: %q, %v", calls, err)
	}
}

func TestRunDockerCommandWaitDelayBoundsInheritedPipes(t *testing.T) {
	dir := fakeDockerCommand(t, "child")
	t.Cleanup(func() {
		data, err := os.ReadFile(filepath.Join(dir, "child-pid"))
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Error(err)
			return
		}
		if child, err := os.FindProcess(pid); err == nil {
			_ = child.Kill()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runDockerCmdContext(ctx, "inspect", "v2raya-socks-19001")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inherited pipe error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("inherited command pipes kept the operation blocked: %v", elapsed)
	}
}

func TestRunDockerCommandPreservesOutputAndExitError(t *testing.T) {
	for _, mode := range []string{"success", "error"} {
		t.Run(mode, func(t *testing.T) {
			fakeDockerCommand(t, mode)
			output, err := runDockerCmd("pause", "v2raya-socks-19001")
			if !strings.Contains(output, "docker stdout") {
				t.Fatalf("Docker output was lost: %q", output)
			}
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || !strings.Contains(err.Error(), "is already paused") {
				t.Fatalf("Docker exit cause or existing status diagnostics were lost: %v", err)
			}
		})
	}
}

func TestWriteDockerConfigCleansOnlyItsHelper(t *testing.T) {
	const secret = `{"outbounds":[{"password":"private-volume-config-test"}]}`
	for _, mode := range []string{"success", "error", "hang"} {
		t.Run(mode, func(t *testing.T) {
			dir := fakeDockerCommand(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			err := writeConfigToVolumeContext(ctx, "v2raya-socks-19001", []byte(secret))
			switch mode {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "error":
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
					t.Fatalf("write failure error = %v, want exit status 7", err)
				}
			case "hang":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("hung write error = %v, want DeadlineExceeded", err)
				}
			}
			if err != nil && (strings.Contains(err.Error(), "private-volume-config-test") || strings.Contains(err.Error(), "outbounds") || strings.Contains(err.Error(), "already paused")) {
				t.Fatal("volume writer leaked configuration or Docker diagnostics")
			}
			input, readErr := os.ReadFile(filepath.Join(dir, "input"))
			if readErr != nil || string(input) != secret {
				t.Fatalf("configuration was not sent unchanged on stdin: %v", readErr)
			}
			args := dockerCommandArgs(t, dir, "run")
			if len(args) < 5 || args[2] != "--name" || !strings.HasPrefix(args[3], "v2raya-config-write-") {
				t.Fatalf("writer must own a unique temporary container: %q", args)
			}
			want := []string{"run", "--rm", "--name", args[3], "-i", "--network", "none", "-v", "v2raya-socks-19001:/data", "alpine", "sh", "-c", "cat > /data/config.json"}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("volume write command = %q, want %q", args, want)
			}
			if got, want := dockerCommandArgs(t, dir, "rm"), []string{"rm", "-f", args[3]}; !reflect.DeepEqual(got, want) {
				t.Fatalf("cleanup must remove only this helper: %q, want %q", got, want)
			}
			calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
			if readErr != nil || string(calls) != "run\nrm\n" {
				t.Fatalf("cleanup must run even after the parent deadline: %q, %v", calls, readErr)
			}
		})
	}
}

func TestWriteDockerConfigCleanupHasIndependentDeadline(t *testing.T) {
	fakeDockerCommand(t, "hang")
	t.Setenv("V2RAYA_COMMAND_TEST_CLEANUP", "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := writeConfigToVolumeContext(ctx, "v2raya-socks-19001", []byte(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup replaced the original timeout error: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 3*time.Second || elapsed > 5*time.Second {
		t.Fatalf("cleanup did not use an independent bounded deadline: %v", elapsed)
	}
}
