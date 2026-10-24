package db

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

const testDatabaseTimeout = 100 * time.Millisecond

func createTestBolt(t *testing.T, path string, value []byte) {
	t.Helper()
	handle, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = handle.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("test"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("value"), value)
	})
	err = errors.Join(err, handle.Close())
	if err != nil {
		t.Fatal(err)
	}
}

func readTestBolt(t *testing.T, handle *bbolt.DB) []byte {
	t.Helper()
	var value []byte
	if err := handle.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("test"))
		if bucket == nil {
			return errors.New("test bucket missing")
		}
		value = bytes.Clone(bucket.Get([]byte("value")))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func requireDatabaseTimeout(t *testing.T, started time.Time, err error) {
	t.Helper()
	if !errors.Is(err, bbolt.ErrTimeout) {
		t.Fatalf("expected Bolt lock timeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("short lock timeout took %s", elapsed)
	}
}

func TestOpenExistingLeavesMissingDatabaseForMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	state := databaseState{}
	t.Cleanup(func() { _ = state.close() })
	if err := state.open(path, true, testDatabaseTimeout); err != nil {
		t.Fatal(err)
	}
	if state.initialized || state.handle != nil {
		t.Fatal("missing database consumed initialization")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database was created: %v", err)
	}
	createTestBolt(t, path, []byte("migrated"))
	if err := state.open(path, true, testDatabaseTimeout); err != nil {
		t.Fatal(err)
	}
	if got := string(readTestBolt(t, state.handle)); got != "migrated" {
		t.Fatalf("migrated data lost: %q", got)
	}
	started := time.Now()
	other, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: testDatabaseTimeout})
	if other != nil {
		_ = other.Close()
	}
	requireDatabaseTimeout(t, started, err)
}

func TestDatabaseOpenWaitsBoundedlyForOldWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	createTestBolt(t, path, []byte("unchanged"))
	oldWriter, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldWriter.Close() })
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state := databaseState{}
	started := time.Now()
	err = state.open(path, true, testDatabaseTimeout)
	requireDatabaseTimeout(t, started, err)
	if state.handle != nil {
		t.Fatal("failed open retained a handle")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed open modified the source: %v", err)
	}
	if err := oldWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := state.open(path, false, testDatabaseTimeout); !errors.Is(err, bbolt.ErrTimeout) {
		t.Fatalf("initialization error was not retained: %v", err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseCloseDoesNotInitialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	state := databaseState{}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if state.initialized {
		t.Fatal("Close initialized the database")
	}
	if err := state.open(path, false, testDatabaseTimeout); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}
	if err := state.open(path, false, testDatabaseTimeout); !errors.Is(err, bbolt.ErrDatabaseNotOpen) {
		t.Fatalf("database reopened after shutdown: %v", err)
	}
}

func TestExistingOpenNeverCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	handle, _, err := openDatabase(path, false, false, testDatabaseTimeout)
	if handle != nil {
		_ = handle.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing file error, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file was created: %v", err)
	}
}

func TestReportSnapshotPreservesSourceAndCleansUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	value := []byte(`{"serverObj":{"protocol":"vless","encryption":"mlkem768x25519plus.native.0rtt.test","xhttpExtra":{"future":9007199254740993,"noGRPCHeader":false}}}`)
	createTestBolt(t, path, value)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state := databaseState{readOnly: true}
	t.Cleanup(func() { _ = state.close() })
	if err := state.open(path, true, testDatabaseTimeout); err != nil {
		t.Fatal(err)
	}
	if state.temporaryPath == "" || state.temporaryPath == path {
		t.Fatalf("not an isolated snapshot: %q", state.temporaryPath)
	}
	if got := readTestBolt(t, state.handle); !bytes.Equal(value, got) {
		t.Fatalf("snapshot changed raw data: %s", got)
	}
	if err := state.handle.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("test")).Put([]byte("value"), []byte("private report change"))
	}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("report changed original database: %v", err)
	}
	// Only the private snapshot remains open once the copy is complete.
	original, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: testDatabaseTimeout})
	if err != nil {
		t.Fatalf("source lock leaked after snapshot: %v", err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := state.temporaryPath
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot was not removed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("original database was removed: %v", err)
	}
}

func TestReportRejectsActiveWriterWithoutTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	t.Setenv("TEMP", directory)
	t.Setenv("TMP", directory)
	path := filepath.Join(t.TempDir(), "bolt.db")
	writer, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	started := time.Now()
	handle, temporaryPath, err := openDatabase(path, true, false, testDatabaseTimeout)
	if handle != nil {
		_ = handle.Close()
	}
	requireDatabaseTimeout(t, started, err)
	if temporaryPath != "" {
		t.Fatalf("failed snapshot returned a temp file: %q", temporaryPath)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatalf("failed snapshot left files: %v, %v", files, err)
	}
}

func TestReadOnlySourceMissingIsNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	for _, open := range []func() error{
		func() error {
			handle, err := OpenReadOnly(path)
			if handle != nil {
				_ = handle.Close()
			}
			return err
		},
		func() error {
			state := databaseState{readOnly: true}
			defer state.close()
			return state.open(path, false, testDatabaseTimeout)
		},
	} {
		if err := open(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing read-only source, got %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only source was created: %v", err)
		}
	}
}

func TestReportRejectsInvalidSourceWithoutChangingIt(t *testing.T) {
	for _, content := range [][]byte{{}, []byte("invalid database")} {
		path := filepath.Join(t.TempDir(), "bolt.db")
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		handle, temporaryPath, err := openDatabase(path, true, false, testDatabaseTimeout)
		if handle != nil {
			_ = handle.Close()
		}
		if err == nil || temporaryPath != "" {
			t.Fatalf("invalid source accepted: path=%q, err=%v", temporaryPath, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("invalid source was modified: %v", err)
		}
	}
}

func TestSnapshotFailureReleasesSourceLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	createTestBolt(t, path, []byte("source"))
	missingDirectory := filepath.Join(t.TempDir(), "absent")
	t.Setenv("TMPDIR", missingDirectory)
	t.Setenv("TEMP", missingDirectory)
	t.Setenv("TMP", missingDirectory)
	if _, err := snapshotDatabase(path, testDatabaseTimeout); err == nil {
		t.Fatal("snapshot unexpectedly succeeded without a temporary directory")
	}
	handle, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: testDatabaseTimeout})
	if err != nil {
		t.Fatalf("failed snapshot retained the source lock: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyRejectsSymlinksAndDirectories(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "bolt.db")
	createTestBolt(t, path, []byte("source"))
	link := filepath.Join(directory, "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Existing writable installations may use a database symlink. Only the
	// explicit read-only source API imposes the stricter no-follow policy.
	writable := databaseState{}
	if err := writable.open(link, true, testDatabaseTimeout); err != nil {
		t.Fatalf("existing writable database symlink stopped working: %v", err)
	}
	if err := writable.close(); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{link, directory} {
		handle, err := OpenReadOnly(source)
		if handle != nil {
			_ = handle.Close()
		}
		if err == nil {
			t.Fatalf("accepted non-regular source %q", source)
		}
	}
	state := databaseState{}
	if err := state.open(directory, true, testDatabaseTimeout); err == nil {
		_ = state.close()
		t.Fatal("OpenExisting ignored a directory error")
	}
	dangling := filepath.Join(directory, "dangling.db")
	if err := os.Symlink(filepath.Join(directory, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	state = databaseState{}
	if err := state.open(dangling, true, testDatabaseTimeout); err == nil {
		_ = state.close()
		t.Fatal("OpenExisting treated a broken symlink as an absent database")
	}
}

func TestReadOnlyRetainsLockAndSupportsCopyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boltv4.db")
	createTestBolt(t, path, []byte("legacy source"))
	source, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := filepath.Join(t.TempDir(), "bolt.db")
	if err := source.View(func(tx *bbolt.Tx) error { return tx.CopyFile(destination, 0600) }); err != nil {
		t.Fatal(err)
	}
	copy, err := bbolt.Open(destination, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if got := string(readTestBolt(t, copy)); got != "legacy source" {
		t.Fatalf("copy differs: %q", got)
	}
	started := time.Now()
	other, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: testDatabaseTimeout})
	if other != nil {
		_ = other.Close()
	}
	requireDatabaseTimeout(t, started, err)
}

func TestReadOnlyRejectsSourceReplacementDuringCopy(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "bolt.db")
	createTestBolt(t, path, []byte("original"))
	source, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := os.Rename(path, filepath.Join(directory, "old.db")); err != nil {
		t.Skipf("cannot rename open database: %v", err)
	}
	createTestBolt(t, path, []byte("replacement"))
	var buffer bytes.Buffer
	err = source.View(func(tx *bbolt.Tx) error {
		_, err := tx.WriteTo(&buffer)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("copied pages from replaced source: %v", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("copied partial data before checking source: %d bytes", buffer.Len())
	}
}
