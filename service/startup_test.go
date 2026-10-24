package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/common/instancelock"
	"go.etcd.io/bbolt"
)

func TestInstanceLockCoversShutdownAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		dir := t.TempDir()
		want := errors.New("HTTP bind failed")
		err := withInstanceLock(dir, func() error {
			ch := make(chan error, 1)
			if failure {
				ch <- want
			} else {
				ch <- nil
			}
			return waitForShutdown(ch, func() {
				lock, err := instancelock.Acquire(dir)
				if lock != nil {
					lock.Close()
				}
				if !errors.Is(err, instancelock.ErrAlreadyRunning) {
					t.Errorf("instance ownership released before shutdown cleanup: %v", err)
				}
			})
		})
		if failure && !errors.Is(err, want) || !failure && err != nil {
			t.Fatalf("startup returned %v (failure=%v)", err, failure)
		}
		lock, err := instancelock.Acquire(dir)
		if err != nil {
			t.Fatalf("instance ownership retained after return: %v", err)
		}
		lock.Close()
	}
}

func TestStartupRejectsOtherOwnersBeforeSideEffects(t *testing.T) {
	for _, mode := range []string{"normal", "report", "reset"} {
		for _, owner := range []string{"directory", "bolt", "legacy"} {
			t.Run(owner+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				name := "bolt.db"
				if owner == "legacy" {
					name = "boltv4.db"
				}
				database := startupFixture(t, filepath.Join(dir, name))
				if owner == "directory" {
					database.Close()
					lock, err := instancelock.Acquire(dir)
					if err != nil {
						t.Fatal(err)
					}
					defer lock.Close()
				} else {
					defer database.Close()
				}
				before, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				sidecar := filepath.Join(dir, "resolv.conf.v2raya_backup")
				if err := os.WriteFile(sidecar, []byte("test-only DNS recovery sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
				out, err := runStartupProcess(t, dir, mode)
				if err == nil {
					t.Fatalf("second instance succeeded: %s", out)
				}
				if owner == "directory" && !strings.Contains(out, "already") || owner != "directory" && !strings.Contains(out, "timeout") {
					t.Fatalf("did not reject the actual owner: %s", out)
				}
				after, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("rejected startup changed database: %v", err)
				}
				data, err := os.ReadFile(sidecar)
				if err != nil || string(data) != "test-only DNS recovery sentinel" {
					t.Fatalf("rejected startup consumed DNS recovery state: %v", err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != name && entry.Name() != filepath.Base(sidecar) && entry.Name() != instancelock.Filename {
						t.Fatalf("rejected startup created %s", entry.Name())
					}
				}
			})
		}
	}
}

func TestOfflineReportReleasesLocksAndPreservesDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bolt.db")
	database := startupFixture(t, path)
	database.Close()
	before, _ := os.ReadFile(path)
	if out, err := runStartupProcess(t, dir, "report"); err != nil {
		t.Fatalf("offline report: %v: %s", err, out)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("report wrote to source: %v", err)
	}
	lock, err := instancelock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	database, err = bbolt.Open(path, 0600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
}

func TestConfigReportWorksWhileServiceOwnsDirectory(t *testing.T) {
	dir := t.TempDir()
	lock, err := instancelock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if out, err := runStartupProcess(t, dir, "config-report"); err != nil {
		t.Fatalf("independent report requires downtime: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "bolt.db")); !os.IsNotExist(err) {
		t.Fatalf("independent report created a database: %v", err)
	}
}

func startupFixture(t *testing.T, path string) *bbolt.DB {
	t.Helper()
	database, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte("accounts"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("test-user"), []byte(`"test-password-hash"`))
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}

func runStartupProcess(t *testing.T, dir, mode string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStartupProcessHelper$")
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "V2RAYA_") && !strings.HasPrefix(env, "V2RAYA_STARTUP_TEST=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "V2RAYA_STARTUP_TEST=1", "V2RAYA_CONFIG="+dir, "V2RAYA_LITE=true", "V2RAYA_NFTABLES_SUPPORT=off", "V2RAYA_LOG_DISABLE_COLOR=true", "V2RAYA_V2RAY_BIN="+filepath.Join(dir, "no-core-binary"))
	if mode == "report" {
		cmd.Env = append(cmd.Env, "V2RAYA_REPORT=connection")
	} else if mode == "config-report" {
		cmd.Env = append(cmd.Env, "V2RAYA_REPORT=config")
	} else if mode == "reset" {
		cmd.Env = append(cmd.Env, "V2RAYA_RESET_PASSWORD=true")
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("startup did not fail within its lock deadline: %s", out)
	}
	return string(out), err
}

func TestStartupProcessHelper(t *testing.T) {
	if os.Getenv("V2RAYA_STARTUP_TEST") != "1" {
		return
	}
	main()
}
