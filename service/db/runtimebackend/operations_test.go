package runtimebackend

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/common/instancelock"
	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/sqlitestore"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

func fixture(t *testing.T) (string, *backup.Archive) {
	t.Helper()
	dir := t.TempDir()
	archive, err := backup.NewSnapshot("bbolt", []backup.Bucket{{Name: []byte{0xff, 1}, Sequence: ^uint64(0),
		Buckets: []backup.Bucket{{Name: []byte("nested"), Sequence: 1 << 63, Values: []backup.Value{{Key: []byte("zero"), Data: []byte{}}}}},
		Values:  []backup.Value{{Key: []byte("opaque"), Data: []byte{0, 0xff, 1}}, {Key: []byte("json"), Data: []byte(`{"large":9007199254740993,"unknown":false}`)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(filepath.Join(dir, "bolt.db"), archive); err != nil {
		t.Fatal(err)
	}
	return dir, archive
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestMigrationWritesRollbackAndRestore(t *testing.T) {
	dir, original := fixture(t)
	originalPath := filepath.Join(dir, "bolt.db")
	before := fileHash(t, originalPath)
	migrated, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Backend != "sqlite" || migrated.Manifest != original.Manifest || migrated.Archive == "" {
		t.Fatalf("incomplete migration: %+v", migrated)
	}
	selected, err := ReadSelection(dir)
	if err != nil || !selected.Present || selected.Backend != "sqlite" {
		t.Fatalf("selection: %+v %v", selected, err)
	}
	if err := os.Chmod(originalPath, 0600); err != nil {
		t.Fatal(err)
	}
	sqlite, err := sqlitestore.Open(migrated.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Update(func(tx store.WriteTx) error {
		return tx.Put([]byte("after-migration"), []byte("new-value"), []byte("new durable data"))
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := sqlite.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Close(); err != nil {
		t.Fatal(err)
	}
	retry, err := Migrate(dir)
	if err != nil || !retry.AlreadyActive || retry.Manifest != updated.Manifest {
		t.Fatalf("migration retry must preserve new writes: %+v %v", retry, err)
	}
	if updated.Manifest == original.Manifest {
		t.Fatal("write did not change snapshot")
	}
	exported, err := Export(dir)
	if err != nil || exported.Manifest != updated.Manifest || exported.Source.Backend != "sqlite" {
		t.Fatalf("export: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "latest.json")
	if err := backup.Write(archivePath, exported); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := Rollback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Backend != "bolt" || rolledBack.Database == originalPath || rolledBack.Manifest != updated.Manifest {
		t.Fatalf("rollback did not preserve latest data: %+v", rolledBack)
	}
	actual, err := backup.Export(rolledBack.Database, time.Second)
	if err != nil || actual.Manifest != updated.Manifest {
		t.Fatalf("rollback verify: %v", err)
	}
	if before != fileHash(t, originalPath) {
		t.Fatal("original Bolt changed")
	}
	restored, err := Restore(t.TempDir(), archivePath)
	if err != nil || restored.Manifest != updated.Manifest {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	for _, path := range []string{migrated.Database, migrated.Archive, rolledBack.Database, filepath.Join(dir, SelectionFilename)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe output mode: %s %v", filepath.Base(path), err)
		}
	}
}

func TestFreshInitAndRestoreCannotReplaceExistingData(t *testing.T) {
	dir := t.TempDir()
	result, err := InitSQLite(dir)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := ReadSelection(dir)
	if err != nil || !selection.Initialize {
		t.Fatalf("initialization selection: %+v %v", selection, err)
	}
	guard, err := backup.Export(filepath.Join(dir, "bolt.db"), time.Second)
	if err != nil || guard.Manifest.Buckets != 0 {
		t.Fatalf("compatibility guard: %v", err)
	}
	retry, err := InitSQLite(dir)
	if err != nil || !retry.AlreadyActive || result.Manifest != retry.Manifest {
		t.Fatalf("init retry: %v", err)
	}
	populated, _ := fixture(t)
	before := fileHash(t, filepath.Join(populated, "bolt.db"))
	if _, err := InitSQLite(populated); err == nil {
		t.Fatal("init replaced data")
	}
	if before != fileHash(t, filepath.Join(populated, "bolt.db")) {
		t.Fatal("rejected init changed data")
	}
	if _, err := os.Stat(filepath.Join(populated, SelectionFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected init selected SQLite")
	}
}

func TestSelectionStrictAndMissingNeverFallsBack(t *testing.T) {
	dir, _ := fixture(t)
	good, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, SelectionFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{}`, `null`, `{"version":1,"backend":"bolt","database":"../bolt.db"}`,
		`{"version":1,"backend":"bolt","database":"bolt.db","Backend":"sqlite"}`,
		`{"version":1,"backend":"bolt","database":"bolt.db","backend":"bolt"}`,
		`{"version":1,"backend":"bolt","database":"bolt.db","initialize":null}`,
		`{"version":2,"backend":"bolt","database":"bolt.db"}`,
		`{"version":1,"backend":"sqlite","database":"bolt.db"}`,
		string(data) + ` {}`, strings.Repeat(" ", 4097),
	} {
		if err := os.WriteFile(filepath.Join(dir, SelectionFilename), []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSelection(dir); err == nil {
			t.Fatalf("accepted invalid marker %q", invalid)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, SelectionFilename), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(good.Database); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSelection(dir); err == nil {
		t.Fatal("missing selected DB fell back")
	}
	if err := os.Symlink(filepath.Join(dir, "bolt.db"), good.Database); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSelection(dir); err == nil {
		t.Fatal("selected symlink accepted")
	}
}

func TestManagementLocksExcludeCurrentAndLegacyWriters(t *testing.T) {
	dir, _ := fixture(t)
	lock, err := instancelock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(string) (Result, error){Migrate, Rollback, Verify, InitSQLite} {
		if _, err := operation(dir); !errors.Is(err, instancelock.ErrAlreadyRunning) {
			t.Fatalf("live service not excluded: %v", err)
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := bbolt.Open(filepath.Join(dir, "bolt.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	started := time.Now()
	if _, err := Migrate(dir); !errors.Is(err, bbolt.ErrTimeout) {
		t.Fatalf("legacy writer not excluded: %v", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("legacy lock wait not bounded")
	}
	if _, err := os.Stat(filepath.Join(dir, SelectionFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("blocked migration published selection")
	}
}

func TestTransitionCrashHelper(t *testing.T) {
	dir := os.Getenv("V2RAYA_TEST_TRANSITION_DIR")
	if dir == "" {
		return
	}
	stage := os.Getenv("V2RAYA_TEST_TRANSITION_STAGE")
	_, err := transition(dir, os.Getenv("V2RAYA_TEST_TRANSITION_TARGET"), func(actual string) error {
		if actual == stage {
			os.Exit(79)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash stage not reached")
}

func TestInterruptedTransitionsKeepOneAuthorityAndCanRetry(t *testing.T) {
	for _, target := range []string{"sqlite", "bolt"} {
		for _, stage := range []string{"archived", "published", "verified", "selected"} {
			t.Run(target+"/"+stage, func(t *testing.T) {
				dir, original := fixture(t)
				if target == "bolt" {
					if _, err := Migrate(dir); err != nil {
						t.Fatal(err)
					}
				}
				before, err := ReadSelection(dir)
				if err != nil {
					t.Fatal(err)
				}
				sourceHash := fileHash(t, filepath.Join(dir, "bolt.db"))
				cmd := exec.Command(os.Args[0], "-test.run=^TestTransitionCrashHelper$")
				cmd.Env = append(os.Environ(), "V2RAYA_TEST_TRANSITION_DIR="+dir, "V2RAYA_TEST_TRANSITION_STAGE="+stage, "V2RAYA_TEST_TRANSITION_TARGET="+target)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 79 {
					t.Fatalf("helper: %v %s", err, output)
				}
				after, err := ReadSelection(dir)
				if err != nil {
					t.Fatal(err)
				}
				if stage == "selected" {
					if after.Backend != target {
						t.Fatal("durable selection missing")
					}
				} else if after != before {
					t.Fatal("authority changed before verified activation")
				}
				var result Result
				if target == "sqlite" {
					result, err = Migrate(dir)
				} else {
					result, err = Rollback(dir)
				}
				if err != nil || result.Manifest != original.Manifest {
					t.Fatalf("retry: %+v %v", result, err)
				}
				if sourceHash != fileHash(t, filepath.Join(dir, "bolt.db")) {
					t.Fatal("crash recovery changed original")
				}
			})
		}
	}
}

func TestTargetCollisionDoesNotActivateOrOverwrite(t *testing.T) {
	dir, archive := fixture(t)
	path := filepath.Join(dir, "sqlite-"+archive.Manifest.LogicalSHA256+".db")
	if err := os.WriteFile(path, []byte("unrelated existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	before := fileHash(t, path)
	if _, err := Migrate(dir); err == nil {
		t.Fatal("invalid target accepted")
	}
	if before != fileHash(t, path) {
		t.Fatal("target overwritten")
	}
	selection, err := ReadSelection(dir)
	if err != nil || selection.Present {
		t.Fatalf("failed target changed selection: %v", err)
	}
}

func TestSelectionDoesNotSerializePresence(t *testing.T) {
	data, err := json.Marshal(Selection{Version: 1, Backend: "bolt", Database: "bolt.db", Present: true})
	if err != nil || bytes.Contains(data, []byte("Present")) || bytes.Contains(data, []byte("present")) {
		t.Fatal("presence leaked into schema")
	}
}

func TestRepeatedTransitionsRetainChangedGenerations(t *testing.T) {
	dir, original := fixture(t)
	first, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	firstBolt, err := Rollback(dir)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := bbolt.Open(firstBolt.Database, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.NewBolt(handle).Update(func(tx store.WriteTx) error { return tx.Put([]byte("temporary"), []byte("key"), []byte("changed")) })
	if closeErr := handle.Close(); err != nil || closeErr != nil {
		t.Fatalf("modify Bolt: %v %v", err, closeErr)
	}
	changedBolt := fileHash(t, firstBolt.Database)
	second, err := Migrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Database == first.Database {
		t.Fatal("reused mismatching SQLite generation")
	}
	sqlite, err := sqlitestore.Open(second.Database)
	if err != nil {
		t.Fatal(err)
	}
	err = sqlite.Update(func(tx store.WriteTx) error { return tx.DeleteBucket([]byte("temporary")) })
	if closeErr := sqlite.Close(); err != nil || closeErr != nil {
		t.Fatalf("reset SQLite data: %v %v", err, closeErr)
	}
	last, err := Rollback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if last.Database == firstBolt.Database || last.Manifest != original.Manifest {
		t.Fatal("did not publish new Bolt generation for previous logical data")
	}
	if changedBolt != fileHash(t, firstBolt.Database) {
		t.Fatal("changed previous generation overwritten")
	}
}

func TestRestoreUnknownSentinelDoesNotRequestDefaults(t *testing.T) {
	archive, err := initialArchive()
	if err != nil {
		t.Fatal(err)
	}
	archive.Buckets = append(archive.Buckets, backup.Bucket{Name: []byte("unknown-data"), Values: []backup.Value{{Key: []byte("key"), Data: []byte("preserved")}}})
	archive, err = backup.NewSnapshot("sqlite", archive.Buckets)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.json")
	if err := backup.Write(path, archive); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := Restore(dir, path); err != nil {
		t.Fatal(err)
	}
	selection, err := ReadSelection(dir)
	if err != nil || selection.Initialize {
		t.Fatalf("unknown sentinel interpreted as initialization intent: %v", err)
	}
}

func TestVerifyDetectsMissingCompatibilityGuardButExportCanRecoverData(t *testing.T) {
	dir, original := fixture(t)
	if _, err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "bolt.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(dir); err == nil {
		t.Fatal("verify accepted a directory the service cannot start")
	}
	archive, err := Export(dir)
	if err != nil || archive.Manifest != original.Manifest {
		t.Fatalf("cannot recover data after guard loss: %v", err)
	}
}

func TestRestoreExactUnknownSentinelDoesNotInferInitialization(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			fresh, err := initialArchive()
			if err != nil {
				t.Fatal(err)
			}
			unknown, err := backup.NewSnapshot("bbolt", fresh.Buckets)
			if err != nil {
				t.Fatal(err)
			}
			if version == "v1" {
				boltPath := filepath.Join(t.TempDir(), "source.db")
				if err := backup.Restore(boltPath, unknown); err != nil {
					t.Fatal(err)
				}
				unknown, err = backup.Export(boltPath, time.Second)
				if err != nil {
					t.Fatal(err)
				}
			}
			archivePath := filepath.Join(t.TempDir(), "unknown.json")
			if err := backup.Write(archivePath, unknown); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if _, err := Restore(dir, archivePath); err != nil {
				t.Fatal(err)
			}
			selected, err := ReadSelection(dir)
			if err != nil || selected.Initialize {
				t.Fatalf("inferred initialization from raw bytes: %v", err)
			}
			actual, err := Export(dir)
			if err != nil || actual.Manifest != unknown.Manifest || actual.Source.InitializeDefaults {
				t.Fatalf("raw unknown data changed meaning: %v", err)
			}
		})
	}
}

func TestPendingInitializationSurvivesBoltRollbackExport(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitSQLite(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(dir); err != nil {
		t.Fatal(err)
	}
	archive, err := Export(dir)
	if err != nil || archive.Version != backup.SnapshotFormatVersion || !archive.Source.InitializeDefaults {
		t.Fatalf("pending initialization lost in Bolt export: %v", err)
	}
	path := filepath.Join(t.TempDir(), "pending.json")
	if err := backup.Write(path, archive); err != nil {
		t.Fatal(err)
	}
	newDir := t.TempDir()
	if _, err := Restore(newDir, path); err != nil {
		t.Fatal(err)
	}
	selected, err := ReadSelection(newDir)
	if err != nil || !selected.Initialize {
		t.Fatalf("pending initialization lost during restore: %v", err)
	}
}
