package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/common/instancelock"
	"github.com/v2rayA/v2rayA/db/runtimebackend"
	"go.etcd.io/bbolt"
)

func startupSQLiteFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	database := startupFixture(t, filepath.Join(dir, "bolt.db"))
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := runtimebackend.Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, result.Database
}

func TestSQLiteStartupRejectsSelectionErrorsBeforeDNSOrPasswordChanges(t *testing.T) {
	for _, mode := range []string{"normal", "report", "reset"} {
		for _, scenario := range []string{"invalid marker", "missing selected", "missing guard", "wrong expectation", "old legacy owner"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				dir, path := startupSQLiteFixture(t)
				var extra []string
				switch scenario {
				case "invalid marker":
					if err := os.WriteFile(filepath.Join(dir, runtimebackend.SelectionFilename), []byte(`{"version":1}`), 0600); err != nil {
						t.Fatal(err)
					}
				case "missing selected":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "missing guard":
					if err := os.Remove(filepath.Join(dir, "bolt.db")); err != nil {
						t.Fatal(err)
					}
				case "wrong expectation":
					extra = append(extra, "V2RAYA_DATABASE_BACKEND=bolt")
				case "old legacy owner":
					legacy := startupFixture(t, filepath.Join(dir, "boltv4.db"))
					defer legacy.Close()
				}
				dnsPath := filepath.Join(dir, "resolv.conf.v2raya_backup")
				dns := []byte("private startup DNS sentinel")
				if err := os.WriteFile(dnsPath, dns, 0600); err != nil {
					t.Fatal(err)
				}
				before, readErr := os.ReadFile(path)
				if output, err := runStartupProcess(t, dir, mode, extra...); err == nil {
					t.Fatalf("unsafe SQLite startup succeeded: %s", output)
				}
				after, afterErr := os.ReadFile(path)
				if !bytes.Equal(before, after) || errors.Is(readErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
					t.Fatal("rejected SQLite startup changed selected database")
				}
				if after, err := os.ReadFile(dnsPath); err != nil || !bytes.Equal(after, dns) {
					t.Fatalf("rejected SQLite startup consumed DNS state: %v", err)
				}
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("rejected SQLite startup created %s", suffix)
					}
				}
				lock, err := instancelock.Acquire(dir)
				if err != nil {
					t.Fatalf("failed SQLite startup retained directory lock: %v", err)
				}
				_ = lock.Close()
			})
		}
	}
}

func TestSQLiteReportAndPasswordResetUseSelectedDatabase(t *testing.T) {
	dir, path := startupSQLiteFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldBolt, err := os.ReadFile(filepath.Join(dir, "bolt.db"))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := runStartupProcess(t, dir, "report", "V2RAYA_DATABASE_BACKEND=sqlite"); err != nil {
		t.Fatalf("SQLite report failed: %v %s", err, output)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("SQLite report modified source: %v", err)
	}
	if output, err := runStartupProcess(t, dir, "reset"); err != nil {
		t.Fatalf("SQLite reset failed: %v %s", err, output)
	}
	archive, err := runtimebackend.Export(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range archive.Buckets {
		if string(bucket.Name) == "accounts" && len(bucket.Values) > 0 {
			t.Fatal("password reset did not update selected SQLite")
		}
	}
	after, err = os.ReadFile(filepath.Join(dir, "bolt.db"))
	if err != nil || !bytes.Equal(oldBolt, after) {
		t.Fatalf("SQLite reset modified retired Bolt: %v", err)
	}
	guard, err := bbolt.Open(filepath.Join(dir, "bolt.db"), 0600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("SQLite subprocess retained retired Bolt lock: %v", err)
	}
	_ = guard.Close()
}
