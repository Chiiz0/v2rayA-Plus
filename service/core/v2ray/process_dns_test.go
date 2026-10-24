package v2ray

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/db/configure"
)

const processDNSOriginal = "nameserver 192.0.2.53\nsearch original.lan\n"

func processDNSPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("resolver hijacking is Linux-only")
	}
	if hijacker != nil {
		t.Fatal("test requires no active resolver hijacker")
	}
	dir := t.TempDir()
	previousPath, previousBackup := resolvPath, resolvBackupPath
	resolvPath, resolvBackupPath = filepath.Join(dir, "resolv.conf"), filepath.Join(dir, "backup")
	t.Cleanup(func() {
		removeResolvHijacker()
		resolvPath, resolvBackupPath = previousPath, previousBackup
	})
	if err := os.WriteFile(resolvPath, []byte(processDNSOriginal), 0644); err != nil {
		t.Fatal(err)
	}
}

func leaveProcessDNSSnapshot(t *testing.T) {
	t.Helper()
	resolvMu.Lock()
	err := backupResolv()
	resolvMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolvPath, []byte(HijackFlag+"\nnameserver 127.2.0.17\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertProcessDNSRestored(t *testing.T) {
	t.Helper()
	content, err := os.ReadFile(resolvPath)
	if err != nil || string(content) != processDNSOriginal {
		t.Fatalf("resolver = %q, want original %q (%v)", content, processDNSOriginal, err)
	}
	if _, err := os.Lstat(resolvBackupPath); !os.IsNotExist(err) {
		t.Fatalf("successfully restored snapshot still exists: %v", err)
	}
}

func TestStopRestoresDNSWithoutRunningProcess(t *testing.T) {
	for _, tt := range []struct {
		name string
		stop func(*CoreProcessManager)
	}{
		{name: "core stop", stop: func(m *CoreProcessManager) { m.Stop(false) }},
		{name: "transparent stop without template", stop: func(m *CoreProcessManager) { m.CheckAndStopTransparentProxy(nil) }},
		{name: "transparent mode already disabled", stop: func(m *CoreProcessManager) {
			m.CheckAndStopTransparentProxy(&configure.Setting{Transparent: configure.TransparentClose})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			processDNSPaths(t)
			leaveProcessDNSSnapshot(t)
			tt.stop(&CoreProcessManager{})
			assertProcessDNSRestored(t)
		})
	}
}

func TestTransparentStopHookFailureRestoresDNS(t *testing.T) {
	processDNSPaths(t)
	leaveProcessDNSSnapshot(t)
	t.Setenv("V2RAYA_CONFIG", t.TempDir())
	config := conf.GetEnvironmentConfig()
	previousHook := config.TransparentHook
	config.TransparentHook = filepath.Join(t.TempDir(), "nonexistent-hook")
	t.Cleanup(func() { config.TransparentHook = previousHook })
	manager := &CoreProcessManager{}
	// The failing pre-stop hook returns before any iptables operation.
	manager.CheckAndStopTransparentProxy(&configure.Setting{
		Transparent: configure.TransparentGfwlist, TransparentType: configure.TransparentRedirect,
	})
	assertProcessDNSRestored(t)
}

func TestFailedCoreStartupRestoresDNSBeforeReturning(t *testing.T) {
	processDNSPaths(t)
	previousFactory := newCoreProcess
	t.Cleanup(func() { newCoreProcess = previousFactory })
	previousCleanup := cleanupFailedCoreStart
	t.Cleanup(func() { cleanupFailedCoreStart = previousCleanup })
	cleanups := 0
	cleanupFailedCoreStart = func() {
		cleanups++
		// Exercise DNS restoration while substituting for host firewall
		// and TUN teardown, which must never run in a unit test.
		removeResolvHijacker()
	}
	startupErr := errors.New("post-start hook failed")
	callbackDone := make(chan struct{})
	newCoreProcess = func(tmpl *Template, prestart, poststart func() error, unexpectedStop func(*Process)) (*Process, error) {
		// Model a failure after afterStart installed DNS but before Start
		// could assign m.p. No real core, host resolver or firewall is used.
		leaveProcessDNSSnapshot(t)
		go func() {
			defer close(callbackDone)
			unexpectedStop(&Process{template: tmpl})
		}()
		return nil, startupErr
	}
	manager := &CoreProcessManager{}
	if err := manager.Start(&Template{}); !errors.Is(err, startupErr) {
		t.Fatalf("Start error = %v, want %v", err, startupErr)
	}
	if manager.p != nil {
		t.Fatal("failed startup must not publish a process")
	}
	if cleanups != 1 {
		t.Fatalf("failed startup cleanup ran %d times, want once", cleanups)
	}
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("early-exit callback deadlocked against startup")
	}
	assertProcessDNSRestored(t)
}

func TestLateCoreExitPreservesReplacementDNS(t *testing.T) {
	processDNSPaths(t)
	leaveProcessDNSSnapshot(t)
	old := &Process{template: &Template{Setting: &configure.Setting{Transparent: configure.TransparentClose}}}
	replacement := &Process{template: &Template{Setting: &configure.Setting{Transparent: configure.TransparentClose}}}
	manager := &CoreProcessManager{p: old}
	manager.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.handleUnexpectedStop(old)
	}()
	// Model replacement ownership being published before the old waiter
	// can acquire the manager lock and attempt host-state cleanup.
	manager.p = replacement
	manager.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old process callback did not finish")
	}
	content, err := os.ReadFile(resolvPath)
	if err != nil || string(content) != HijackFlag+"\nnameserver 127.2.0.17\n" {
		t.Fatalf("old process restored replacement DNS: %q, %v", content, err)
	}
	if _, err := os.Stat(resolvBackupPath); err != nil {
		t.Fatalf("old process removed replacement backup: %v", err)
	}
}

func TestNewProcessFailureCancelsAndReapsCore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses a temporary shell core and Linux process state")
	}
	for _, stage := range []string{"post-start", "readiness"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			pidPath := filepath.Join(dir, "pid")
			corePath := filepath.Join(dir, "fake-core")
			if err := os.WriteFile(corePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$V2RAYA_TEST_CORE_PID\"\nexec sleep 60\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("V2RAYA_TEST_CORE_PID", pidPath)
			config := conf.GetEnvironmentConfig()
			previous := *config
			config.Config, config.V2rayBin = dir, corePath
			config.PluginManager, config.V2rayConfigDirectory = "", ""
			config.CoreStartupTimeout = 0
			t.Cleanup(func() { *config = previous })
			pid := 0
			t.Cleanup(func() {
				if pid > 0 {
					if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
						p, _ := os.FindProcess(pid)
						_ = p.Kill()
					}
				}
			})
			closed := false
			tmpl := &Template{API: &coreObj.APIObject{}, ApiCloses: []func(){func() { closed = true }}}
			postStartErr := errors.New("post-start failed")
			process, err := NewProcess(tmpl, func() error { return nil }, func() error {
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					if data, err := os.ReadFile(pidPath); err == nil {
						pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
						if err == nil && pid > 0 {
							if stage == "post-start" {
								return postStartErr
							}
							return nil
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				return errors.New("temporary core did not publish its pid")
			}, func(*Process) {})
			if process != nil || err == nil {
				t.Fatalf("NewProcess = %v, %v; want startup failure", process, err)
			}
			if stage == "post-start" && !errors.Is(err, postStartErr) {
				t.Fatalf("post-start error = %v, want %v", err, postStartErr)
			}
			if stage == "readiness" && !strings.Contains(err.Error(), "timeout:") {
				t.Fatalf("readiness error = %v, want timeout", err)
			}
			if !closed {
				t.Fatal("failed startup did not close its template")
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); os.IsNotExist(err) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("failed core pid %d remains running or unreaped", pid)
		})
	}
}
