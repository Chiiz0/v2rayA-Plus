package sqlitestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/sqlitebackup"
	"github.com/v2rayA/v2rayA/db/store"
)

type bundleFile struct {
	digest [32]byte
	info   os.FileInfo
}

func inspectBundle(t *testing.T, path string) map[string]bundleFile {
	t.Helper()
	result := map[string]bundleFile{}
	for _, suffix := range append([]string{""}, sidecars...) {
		info, err := os.Stat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		result[suffix] = bundleFile{digest: sha256.Sum256(data), info: info}
	}
	return result
}

func assertBundleUnchanged(t *testing.T, path string, before map[string]bundleFile) {
	t.Helper()
	after := inspectBundle(t, path)
	if len(after) != len(before) {
		t.Fatal("source sidecar set changed")
	}
	for suffix, prior := range before {
		next, ok := after[suffix]
		if !ok || prior.digest != next.digest || !os.SameFile(prior.info, next.info) || prior.info.Mode() != next.info.Mode() || !prior.info.ModTime().Equal(next.info.ModTime()) || prior.info.Size() != next.info.Size() {
			t.Fatalf("source bundle file %q changed", suffix)
		}
	}
}

func TestCreateIsPrivateAtomicAndNeverOverwrites(t *testing.T) {
	archive := contractArchive(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.sqlite")
	if err := Create(path, archive); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("runtime mode not 0600: %v", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("staging files leaked: %v", err)
	}
	before := inspectBundle(t, path)
	if err := Create(path, archive); err == nil {
		t.Fatal("overwrote target")
	}
	assertBundleUnchanged(t, path, before)
	actual, err := SnapshotReadOnly(path)
	if err != nil || actual.Manifest != archive.Manifest {
		t.Fatalf("published archive mismatch: %v", err)
	}
	assertBundleUnchanged(t, path, before)
	for _, suffix := range sidecars {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.sqlite")
			if err := os.WriteFile(path+suffix, []byte("preexisting"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Create(path, archive); err == nil {
				t.Fatal("created beside preexisting sidecar")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed Create left target")
			}
			data, err := os.ReadFile(path + suffix)
			if err != nil || string(data) != "preexisting" {
				t.Fatal("modified preexisting sidecar")
			}
		})
	}
}

func TestFrozenCandidateCannotBecomeRuntime(t *testing.T) {
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "source.db")
	if err := backup.Restore(boltPath, contractArchive(t)); err != nil {
		t.Fatal(err)
	}
	v1, err := backup.Export(boltPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "candidate.sqlite")
	if _, err := sqlitebackup.Import(context.Background(), path, v1); err != nil {
		t.Fatal(err)
	}
	before := inspectBundle(t, path)
	if s, err := Open(path); err == nil {
		_ = s.Close()
		t.Fatal("frozen candidate accepted as runtime")
	}
	if _, err := SnapshotReadOnly(path); err == nil {
		t.Fatal("frozen candidate accepted by runtime snapshot")
	}
	assertBundleUnchanged(t, path, before)
}

func TestRejectFilesWithoutMutation(t *testing.T) {
	archive := contractArchive(t)
	for _, kind := range []string{"missing", "truncated", "directory", "symlink", "sidecar-symlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "runtime.sqlite")
			switch kind {
			case "truncated":
				if err := os.WriteFile(path, []byte("SQLite format 3\x00"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "target.sqlite")
				if err := Create(target, archive); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip(err)
				}
			case "sidecar-symlink":
				if err := Create(path, archive); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(dir, "secret")
				if err := os.WriteFile(target, []byte("credential-must-stay-private"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path+"-wal"); err != nil {
					t.Skip(err)
				}
			case "oversize":
				if err := Create(path, archive); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, maxFileBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			if database, err := Open(path); err == nil {
				_ = database.Close()
				t.Fatal("invalid file accepted")
			} else if strings.Contains(err.Error(), "credential-must-stay-private") {
				t.Fatal("error leaked contents")
			}
			if kind == "missing" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("Open created missing runtime")
				}
			}
			if kind == "sidecar-symlink" {
				data, err := os.ReadFile(filepath.Join(dir, "secret"))
				if err != nil || string(data) != "credential-must-stay-private" {
					t.Fatal("sidecar target modified")
				}
			}
		})
	}
}

func TestInvalidSchemaAndConstraintsRejectBeforeSourceWrites(t *testing.T) {
	for _, statement := range []string{
		"PRAGMA application_id=123", "PRAGMA user_version=2",
		"CREATE TABLE unexpected(secret TEXT)",
		"CREATE INDEX unexpected ON kv_entries(data)",
		"CREATE VIEW unexpected AS SELECT data FROM kv_entries",
		"CREATE TRIGGER secret_do_not_print AFTER INSERT ON kv_entries BEGIN DELETE FROM kv_entries; END",
		"UPDATE runtime_meta SET format='secret_do_not_print'",
		"UPDATE schema_migrations SET schema_sha256='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'",
		"PRAGMA foreign_keys=OFF; INSERT INTO kv_entries(id,parent_id,name,kind,data) VALUES(999,998,X'01',1,X'00')",
		"PRAGMA ignore_check_constraints=ON; UPDATE kv_entries SET sequence=X'00' WHERE kind=0",
	} {
		t.Run(strings.Split(statement, " ")[0]+"/"+strings.Split(statement, " ")[1], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.sqlite")
			if err := Create(path, contractArchive(t)); err != nil {
				t.Fatal(err)
			}
			info, err := regularInfo(path)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := openDatabase(path, info, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.ExecContext(context.Background(), statement); err != nil {
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			before := inspectBundle(t, path)
			if database, err := Open(path); err == nil {
				_ = database.Close()
				t.Fatal("invalid runtime accepted")
			} else if strings.Contains(err.Error(), "secret_do_not_print") {
				t.Fatal("schema diagnostic leaked source text")
			}
			assertBundleUnchanged(t, path, before)
		})
	}
}

// A separate process leaves SQLite's real crash artifacts, rather than copying
// an already checkpointed database and pretending it represents WAL recovery.
func TestCrashHelper(t *testing.T) {
	mode := os.Getenv("V2RAYA_SQLITESTORE_CRASH")
	if mode == "" {
		return
	}
	path := os.Getenv("V2RAYA_SQLITESTORE_PATH")
	if mode == "wal" {
		database, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.writer.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
		if err := database.Update(func(tx store.WriteTx) error {
			return tx.Put([]byte("after-crash"), []byte("committed"), bytes.Repeat([]byte{0, 255, 42}, 4096))
		}); err != nil {
			t.Fatal(err)
		}
		os.Exit(73)
	}
	if mode == "journal" {
		info, err := regularInfo(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := openDatabase(path, info, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec("PRAGMA journal_mode=DELETE; PRAGMA cache_size=2; PRAGMA cache_spill=ON"); err != nil {
			t.Fatal(err)
		}
		tx, err := raw.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("UPDATE kv_entries SET data=? WHERE kind=1", bytes.Repeat([]byte{77}, 8192)); err != nil {
			t.Fatal(err)
		}
		os.Exit(73)
	}
	t.Fatal("unknown crash mode")
}

func runCrash(t *testing.T, path, mode string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	cmd.Env = append(os.Environ(), "V2RAYA_SQLITESTORE_CRASH="+mode, "V2RAYA_SQLITESTORE_PATH="+path)
	output, err := cmd.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 73 {
		t.Fatalf("crash process did not reach durable stage: %v %s", err, output)
	}
}

func TestCommittedWALRecoveredWithoutTouchingSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	original := contractArchive(t)
	if err := Create(path, original); err != nil {
		t.Fatal(err)
	}
	runCrash(t, path, "wal")
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() <= 32 {
		t.Fatalf("fixture did not leave committed WAL: %v", err)
	}
	before := inspectBundle(t, path)
	actual, err := SnapshotReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets := append(original.Buckets, backup.Bucket{Name: []byte("after-crash"), Values: []backup.Value{{Key: []byte("committed"), Data: bytes.Repeat([]byte{0, 255, 42}, 4096)}}})
	want, err := backup.NewSnapshot("sqlite", wantBuckets)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Manifest != want.Manifest || !reflect.DeepEqual(actual.Buckets, want.Buckets) {
		t.Fatal("snapshot lost committed WAL data")
	}
	assertBundleUnchanged(t, path, before)
	// A real service restart must recover the same data and remain writable.
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	reopened, err := database.Snapshot()
	if err != nil || reopened.Manifest != want.Manifest {
		t.Fatalf("runtime reopen differs from backup recovery: %v", err)
	}
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put([]byte("post-recovery"), []byte("ok"), nil) }); err != nil {
		t.Fatal(err)
	}
}

func TestHotJournalRecoveryIsPrivateAndRollsBackUncommittedWrites(t *testing.T) {
	buckets := contractArchive(t).Buckets
	bulk := backup.Bucket{Name: []byte("bulk")}
	for n := 0; n < 64; n++ {
		bulk.Values = append(bulk.Values, backup.Value{Key: []byte{byte(n + 1)}, Data: bytes.Repeat([]byte{byte(n)}, 8192)})
	}
	original, err := backup.NewSnapshot("sqlite", append(buckets, bulk))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	if err := Create(path, original); err != nil {
		t.Fatal(err)
	}
	runCrash(t, path, "journal")
	journal, err := os.ReadFile(path + "-journal")
	if err != nil || len(journal) <= 512 || bytes.Equal(journal[:8], make([]byte, 8)) {
		t.Fatalf("fixture did not leave hot journal: %v", err)
	}
	before := inspectBundle(t, path)
	actual, err := SnapshotReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Manifest != original.Manifest {
		t.Fatal("hot journal snapshot included uncommitted writes")
	}
	assertBundleUnchanged(t, path, before)
}
