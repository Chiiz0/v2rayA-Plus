package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/runtimebackend"
	"github.com/v2rayA/v2rayA/db/sqlitestore"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

func sqliteLifecycleFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	name := "sqlite-" + strings.Repeat("a", 64) + ".db"
	path := filepath.Join(dir, name)
	value := []byte(`{"encryption":"test","xhttpExtra":{"large":9007199254740993,"enabled":false}}`)
	archive, err := backup.NewSnapshot("sqlite", []backup.Bucket{{Name: []byte("test"), Values: []backup.Value{{Key: []byte("value"), Data: value}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitestore.Create(path, archive); err != nil {
		t.Fatal(err)
	}
	createTestBolt(t, filepath.Join(dir, "bolt.db"), []byte("retired"))
	selection, err := json.Marshal(runtimebackend.Selection{Version: 1, Backend: "sqlite", Database: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runtimebackend.SelectionFilename), selection, 0600); err != nil {
		t.Fatal(err)
	}
	return dir, path, value
}

func readLifecycleValue(t *testing.T, backend store.Store) []byte {
	t.Helper()
	var result []byte
	if err := backend.View(func(tx store.ReadTx) error {
		var err error
		result, err = tx.Get([]byte("test"), []byte("value"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSQLiteRuntimeLifecycleAndRetiredBoltLock(t *testing.T) {
	dir, path, value := sqliteLifecycleFixture(t)
	state := databaseState{}
	t.Cleanup(func() { _ = state.close() })
	if err := state.openSelected(dir, "auto", true, testDatabaseTimeout); err != nil {
		t.Fatal(err)
	}
	if state.selection.Backend != "sqlite" || state.sourcePath != path || state.handle != nil || state.sqlite == nil {
		t.Fatal("selection did not open only the SQLite runtime")
	}
	backend, err := state.currentStore()
	if err != nil || !bytes.Equal(readLifecycleValue(t, backend), value) {
		t.Fatalf("SQLite source changed: %v", err)
	}
	old, err := bbolt.Open(filepath.Join(dir, "bolt.db"), 0600, &bbolt.Options{Timeout: testDatabaseTimeout})
	if old != nil {
		_ = old.Close()
	}
	if !errors.Is(err, bbolt.ErrTimeout) {
		t.Fatalf("SQLite runtime did not exclude an older Bolt writer: %v", err)
	}
	if err := backend.Update(func(tx store.WriteTx) error { return tx.Put([]byte("test"), []byte("value"), []byte("runtime write")) }); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.currentStore(); !errors.Is(err, bbolt.ErrDatabaseNotOpen) {
		t.Fatalf("closed lifecycle reopened SQLite: %v", err)
	}
	if err := backend.View(func(store.ReadTx) error { t.Fatal("closed backend callback"); return nil }); !errors.Is(err, sqlitestore.ErrClosed) {
		t.Fatalf("retained SQLite adapter was not closed: %v", err)
	}
	reopened, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if string(readLifecycleValue(t, reopened)) != "runtime write" {
		t.Fatal("SQLite runtime writes were not persisted")
	}
	old, err = bbolt.Open(filepath.Join(dir, "bolt.db"), 0600, &bbolt.Options{Timeout: testDatabaseTimeout})
	if err != nil {
		t.Fatalf("Close retained the retired Bolt lock: %v", err)
	}
	_ = old.Close()
}

func TestSQLiteRuntimeReportUsesPrivateWritableCopy(t *testing.T) {
	dir, path, value := sqliteLifecycleFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state := databaseState{readOnly: true}
	t.Cleanup(func() { _ = state.close() })
	if err := state.openSelected(dir, "sqlite", true, testDatabaseTimeout); err != nil {
		t.Fatal(err)
	}
	if state.temporaryDir == "" || state.sourcePath != path || !bytes.Equal(readLifecycleValue(t, state.backend), value) {
		t.Fatal("SQLite report did not use a complete private snapshot")
	}
	if err := state.backend.Update(func(tx store.WriteTx) error { return tx.Put([]byte("test"), []byte("value"), []byte("private report change")) }); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("report changed the source SQLite bytes: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("report created a source sidecar %s: %v", suffix, err)
		}
	}
	temporaryDir := state.temporaryDir
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporaryDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private SQLite report directory remains: %v", err)
	}
}

func TestSelectedDatabaseRefusesFallbackAndOldOwners(t *testing.T) {
	for _, scenario := range []string{"backend mismatch", "missing selected", "missing compatibility", "old writer", "invalid marker"} {
		t.Run(scenario, func(t *testing.T) {
			dir, path, _ := sqliteLifecycleFixture(t)
			expected := "auto"
			switch scenario {
			case "backend mismatch":
				expected = "bolt"
			case "missing selected":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "missing compatibility":
				if err := os.Remove(filepath.Join(dir, "bolt.db")); err != nil {
					t.Fatal(err)
				}
			case "old writer":
				writer, err := bbolt.Open(filepath.Join(dir, "bolt.db"), 0600, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
			case "invalid marker":
				if err := os.WriteFile(filepath.Join(dir, runtimebackend.SelectionFilename), []byte(`{"backend":"sqlite"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, readErr := os.ReadFile(path)
			state := databaseState{}
			defer state.close()
			started := time.Now()
			err := state.openSelected(dir, expected, true, testDatabaseTimeout)
			if err == nil || state.backend != nil || state.sqlite != nil || state.handle != nil {
				t.Fatalf("unsafe selection opened a backend: %v", err)
			}
			if scenario == "old writer" {
				requireDatabaseTimeout(t, started, err)
			}
			after, afterErr := os.ReadFile(path)
			if !bytes.Equal(before, after) || errors.Is(readErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("rejected startup modified or recreated the selected SQLite source")
			}
		})
	}
}

func TestExplicitSQLiteWithoutSelectionDoesNotCreateDatabase(t *testing.T) {
	dir := t.TempDir()
	state := databaseState{}
	defer state.close()
	if err := state.openSelected(dir, "sqlite", false, testDatabaseTimeout); err == nil {
		t.Fatal("explicit SQLite bypassed the required offline initialization")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed explicit backend created files: %v", err)
	}
}
