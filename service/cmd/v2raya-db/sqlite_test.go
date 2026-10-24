package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
	"go.etcd.io/bbolt"
)

func sqliteCommandFixture(t *testing.T) (string, string, *backup.Archive) {
	t.Helper()
	source := fixtureDatabase(t)
	database, err := bbolt.Open(source, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte{255, 0, 1}).SetSequence(^uint64(0))
	})
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("set unsigned sequence: %v; close: %v", err, closeErr)
	}
	archivePath := filepath.Join(t.TempDir(), "original.json")
	invoke(t, 0, "export", "--db", source, "--out", archivePath)
	archive, err := backup.Read(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	return source, archivePath, archive
}

func readCommandFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSQLiteCommandsPreserveCompleteArchiveAndRetry(t *testing.T) {
	source, archivePath, archive := sqliteCommandFixture(t)
	sourceBefore, archiveBefore := readCommandFile(t, source), readCommandFile(t, archivePath)
	candidate := filepath.Join(t.TempDir(), "candidate # 中文?.sqlite")
	created := invoke(t, 0, "sqlite-import", "--in", archivePath, "--db", candidate)
	if created.SQLite == nil || created.SQLite.AlreadyPresent || created.SQLite.SchemaVersion < 1 || len(created.SQLite.SHA256) != 64 || created.SQLite.Engine.SQLiteVersion == "" || created.Manifest == nil || *created.Manifest != archive.Manifest {
		t.Fatalf("missing SQLite migration result: %+v", created)
	}
	before, infoErr := os.Stat(candidate)
	if infoErr != nil || before.Mode().Perm() != 0600 {
		t.Fatalf("candidate permissions: %v", infoErr)
	}
	bytesBefore := readCommandFile(t, candidate)
	retried := invoke(t, 0, "sqlite-import", "--in", archivePath, "--db", candidate)
	if retried.SQLite == nil || !retried.SQLite.AlreadyPresent || retried.SQLite.SHA256 != created.SQLite.SHA256 {
		t.Fatalf("identical retry was not verified: %+v", retried)
	}
	verified := invoke(t, 0, "sqlite-verify", "--db", candidate, "--in", archivePath)
	if verified.Comparison == nil || !verified.Comparison.Match || verified.Comparison.ArchiveMetadataMatch == nil || !*verified.Comparison.ArchiveMetadataMatch {
		t.Fatalf("archive provenance did not compare equal: %+v", verified)
	}
	invoke(t, 0, "sqlite-verify", "--db", candidate)
	output := filepath.Join(t.TempDir(), "returned.json")
	exported := invoke(t, 0, "sqlite-export", "--db", candidate, "--out", output)
	if exported.Archive != output || exported.SQLite == nil {
		t.Fatal("SQLite export omitted its output or verification result")
	}
	got, err := backup.Read(output)
	if err != nil || !reflect.DeepEqual(got, archive) || !bytes.Equal(readCommandFile(t, output), archiveBefore) {
		t.Fatalf("SQLite round trip changed raw data or original provenance: %v", err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	invoke(t, 0, "restore", "--in", output, "--db", restored)
	if report := invoke(t, 0, "verify", "--in", archivePath, "--db", restored); report.Comparison == nil || !report.Comparison.Match {
		t.Fatal("SQLite round trip no longer restores an equivalent Bolt database")
	}
	after, err := os.Stat(candidate)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || !bytes.Equal(bytesBefore, readCommandFile(t, candidate)) {
		t.Fatal("retry/verify/export changed the existing candidate")
	}
	if !bytes.Equal(sourceBefore, readCommandFile(t, source)) || !bytes.Equal(archiveBefore, readCommandFile(t, archivePath)) {
		t.Fatal("SQLite commands modified original Bolt or archive bytes")
	}
}

func TestSQLiteCommandsRejectDifferentProvenanceAndData(t *testing.T) {
	source, input, archive := sqliteCommandFixture(t)
	candidate := filepath.Join(t.TempDir(), "candidate.sqlite")
	invoke(t, 0, "sqlite-import", "--in", input, "--db", candidate)
	original := readCommandFile(t, candidate)
	createdAt, err := time.Parse(time.RFC3339Nano, archive.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	archive.CreatedAt = createdAt.Add(time.Second).Format(time.RFC3339Nano)
	metadataInput := filepath.Join(t.TempDir(), "different-provenance.json")
	if err := backup.Write(metadataInput, archive); err != nil {
		t.Fatal(err)
	}
	compared := invoke(t, 2, "sqlite-verify", "--db", candidate, "--in", metadataInput)
	if compared.Comparison == nil || compared.Comparison.Match || compared.Comparison.ArchiveMetadataMatch == nil || *compared.Comparison.ArchiveMetadataMatch {
		t.Fatal("identical logical data concealed changed archive provenance")
	}
	invoke(t, 1, "sqlite-import", "--in", metadataInput, "--db", candidate)
	database, err := bbolt.Open(source, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error { return tx.Bucket([]byte("system")).Put([]byte("future"), []byte{1, 255, 0}) })
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("change fixture: %v; close: %v", err, closeErr)
	}
	changedInput := filepath.Join(t.TempDir(), "changed-data.json")
	invoke(t, 0, "export", "--db", source, "--out", changedInput)
	changed, err := backup.Read(changedInput)
	if err != nil {
		t.Fatal(err)
	}
	// Hold provenance constant to prove that the comparison checks data, not
	// merely the independently changing creation time or source file digest.
	changed.CreatedAt, changed.Source = createdAt.Format(time.RFC3339Nano), archive.Source
	comparableInput := filepath.Join(t.TempDir(), "changed-data-same-provenance.json")
	if err := backup.Write(comparableInput, changed); err != nil {
		t.Fatal(err)
	}
	if report := invoke(t, 2, "sqlite-verify", "--db", candidate, "--in", comparableInput); report.Comparison == nil || report.Comparison.Match || report.Comparison.ArchiveMetadataMatch == nil || !*report.Comparison.ArchiveMetadataMatch {
		t.Fatal("equal row counts concealed changed raw data")
	}
	invoke(t, 1, "sqlite-import", "--in", comparableInput, "--db", candidate)
	invoke(t, 1, "sqlite-export", "--db", candidate, "--out", input)
	if !bytes.Equal(original, readCommandFile(t, candidate)) {
		t.Fatal("conflicting import changed the candidate")
	}
}

func TestSQLiteImportRefusesApplicationAuditErrors(t *testing.T) {
	source := fixtureDatabase(t)
	database, err := bbolt.Open(source, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("dockerProxies")).Put([]byte("19080"), []byte(`{"port":19080,"which":{"_type":"server","id":99},"core":"xray"}`))
	})
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("change reference: %v; close: %v", err, closeErr)
	}
	input := filepath.Join(t.TempDir(), "invalid-reference.json")
	invoke(t, 2, "export", "--db", source, "--out", input)
	candidate := filepath.Join(t.TempDir(), "rejected.sqlite")
	report := invoke(t, 2, "sqlite-import", "--in", input, "--db", candidate)
	if report.Audit == nil || !report.Audit.HasErrors() || report.SQLite != nil {
		t.Fatal("application audit failure was not reported before SQLite import")
	}
	if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
		t.Fatal("invalid application data created a SQLite candidate")
	}
}

func TestSQLiteCommandsRejectSidecarsAndDamagedFiles(t *testing.T) {
	_, input, _ := sqliteCommandFixture(t)
	candidate := filepath.Join(t.TempDir(), "candidate.sqlite")
	invoke(t, 0, "sqlite-import", "--in", input, "--db", candidate)
	original := readCommandFile(t, candidate)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			if err := os.WriteFile(candidate+suffix, []byte("credential-hash-must-not-be-printed"), 0600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(candidate + suffix)
			invoke(t, 1, "sqlite-verify", "--db", candidate)
			invoke(t, 1, "sqlite-import", "--in", input, "--db", candidate)
			output := filepath.Join(t.TempDir(), "not-exported.json")
			invoke(t, 1, "sqlite-export", "--db", candidate, "--out", output)
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("sidecar-bearing candidate was exported")
			}
		})
	}
	damaged := filepath.Join(t.TempDir(), "damaged.sqlite")
	if err := os.WriteFile(damaged, []byte("credential-hash-must-not-be-printed"), 0600); err != nil {
		t.Fatal(err)
	}
	invoke(t, 1, "sqlite-verify", "--db", damaged)
	invoke(t, 1, "sqlite-import", "--in", input, "--db", damaged)
	if !bytes.Equal(original, readCommandFile(t, candidate)) || string(readCommandFile(t, damaged)) != "credential-hash-must-not-be-printed" {
		t.Fatal("failed verification/import changed an existing file")
	}
}

func TestSQLiteCommandsRejectInvalidInputArchives(t *testing.T) {
	_, input, archive := sqliteCommandFixture(t)
	candidate := filepath.Join(t.TempDir(), "candidate.sqlite")
	invoke(t, 0, "sqlite-import", "--in", input, "--db", candidate)
	original := readCommandFile(t, candidate)
	archive.Manifest.LogicalSHA256 = strings.Repeat("0", 64)
	badChecksum, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{
		"malformed":     []byte(`{"credential-hash-must-not-be-printed":`),
		"unknown-field": []byte(`{"credential-hash-must-not-be-printed":"private-value"}`),
		"checksum":      badChecksum,
		"missing":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.json")
			if contents != nil {
				if err := os.WriteFile(path, contents, 0600); err != nil {
					t.Fatal(err)
				}
			}
			destination := filepath.Join(t.TempDir(), "not-created.sqlite")
			invoke(t, 1, "sqlite-import", "--in", path, "--db", destination)
			if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatal("invalid input archive created a candidate")
			}
			invoke(t, 1, "sqlite-verify", "--db", candidate, "--in", path)
			if !bytes.Equal(original, readCommandFile(t, candidate)) {
				t.Fatal("invalid comparison input changed the candidate")
			}
		})
	}
}

func TestSQLiteCommandInfoAndArguments(t *testing.T) {
	report := invoke(t, 0, "sqlite-info")
	if report.Engine == nil || report.Engine.DriverVersion == "" || report.Engine.SQLiteVersion == "" || report.Engine.SQLiteSourceID == "" || report.Build == nil || report.Build.GoVersion == "" || report.Build.CGOEnabled == "" || report.Database != "" {
		t.Fatalf("engine/build identity missing: %+v", report)
	}
	for _, args := range [][]string{
		{"sqlite-import"}, {"sqlite-import", "--in", "missing"}, {"sqlite-verify"},
		{"sqlite-export", "--db", "missing"}, {"sqlite-info", "unexpected"},
		{"sqlite-info", "--db", "unexpected"}, {"sqlite-verify", "--db", "missing", "--docker"},
	} {
		var output bytes.Buffer
		if code := run(args, &output, &output); code != 2 {
			t.Fatalf("invalid SQLite arguments %v exited %d", args, code)
		}
	}
	var help bytes.Buffer
	if code := run([]string{"--help"}, &help, &help); code != 0 || !strings.Contains(help.String(), "not runtime databases") || !strings.Contains(help.String(), "sqlite-info") {
		t.Fatal("help omitted the frozen-candidate boundary")
	}
}
