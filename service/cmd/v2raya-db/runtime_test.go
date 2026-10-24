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

	"github.com/v2rayA/v2rayA/common/instancelock"
	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/runtimebackend"
	"github.com/v2rayA/v2rayA/db/sqlitestore"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

func invokeRuntime(t *testing.T, expected int, args ...string) runtimeCommandReport {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != expected {
		t.Fatalf("%v: exit %d, want %d; stdout=%s stderr=%s", args, code, expected, &stdout, &stderr)
	}
	if strings.Contains(stdout.String()+stderr.String(), "credential-hash-must-not-be-printed") {
		t.Fatal("runtime command leaked stored credentials")
	}
	var report runtimeCommandReport
	if stdout.Len() != 0 {
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("invalid runtime command JSON: %v", err)
		}
	}
	if expected == 0 && (!report.Success || report.Operation != args[0]) {
		t.Fatal("runtime command omitted success or operation identity")
	}
	return report
}

func runtimeFixture(t *testing.T) (string, *backup.Archive) {
	t.Helper()
	source := fixtureDatabase(t)
	archive, err := backup.Export(source, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := backup.Restore(filepath.Join(directory, "bolt.db"), archive); err != nil {
		t.Fatal(err)
	}
	return directory, archive
}

func TestRuntimeCommandsMigrateCurrentWritesExportAndRollback(t *testing.T) {
	directory, original := runtimeFixture(t)
	boltPath := filepath.Join(directory, "bolt.db")
	before := readCommandFile(t, boltPath)
	migrated := invokeRuntime(t, 0, "runtime-migrate", "--config-dir", directory)
	if migrated.Runtime == nil || migrated.Runtime.Backend != "sqlite" || migrated.Runtime.AlreadyActive || migrated.Runtime.Manifest != original.Manifest || !filepath.IsAbs(migrated.Runtime.Database) || migrated.Runtime.Archive == "" {
		t.Fatal("migration omitted its verified target or source archive")
	}
	if !bytes.Equal(before, readCommandFile(t, boltPath)) {
		t.Fatal("migration modified the original Bolt file")
	}
	selection, err := runtimebackend.ReadSelection(directory)
	if err != nil || selection.Backend != "sqlite" || filepath.Join(directory, selection.Database) != migrated.Runtime.Database {
		t.Fatalf("migration did not activate its reported database: %v", err)
	}
	// Simulate an application's committed write while retaining the same
	// directory lock required by the runtime. No service or external resource runs.
	lock, err := instancelock.Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sqlitestore.Open(migrated.Runtime.Database)
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	err = database.Update(func(tx store.WriteTx) error {
		return tx.Put([]byte("system"), []byte("new-after-migration"), []byte(`{"large":18446744073709551615,"disabled":false,"zero":0,"negative":-1}`))
	})
	current, snapshotErr := database.Snapshot()
	closeErr, unlockErr := database.Close(), lock.Close()
	if err != nil || snapshotErr != nil || closeErr != nil || unlockErr != nil {
		t.Fatalf("runtime write/snapshot/close/unlock: %v, %v, %v, %v", err, snapshotErr, closeErr, unlockErr)
	}
	if current.Manifest.LogicalSHA256 == original.Manifest.LogicalSHA256 {
		t.Fatal("runtime fixture did not change")
	}
	retried := invokeRuntime(t, 0, "runtime-migrate", "--config-dir", directory)
	if retried.Runtime == nil || !retried.Runtime.AlreadyActive || retried.Runtime.Manifest != current.Manifest {
		t.Fatal("migration retry reset current SQLite data")
	}
	verified := invokeRuntime(t, 0, "runtime-verify", "--config-dir", directory)
	if verified.Runtime == nil || verified.Runtime.Manifest != current.Manifest {
		t.Fatal("runtime verification omitted current writes")
	}
	output := filepath.Join(t.TempDir(), "runtime-v2.json")
	exported := invokeRuntime(t, 0, "runtime-export", "--config-dir", directory, "--out", output)
	if exported.Manifest == nil || *exported.Manifest != current.Manifest || exported.Audit == nil || exported.Audit.HasErrors() {
		t.Fatal("runtime export omitted its verified manifest or audit")
	}
	archive, err := backup.Read(output)
	if err != nil || archive.Version != backup.SnapshotFormatVersion || archive.Source.Backend != "sqlite" || archive.Source.DigestKind != backup.LogicalDigestKind || !reflect.DeepEqual(archive.Buckets, current.Buckets) {
		t.Fatalf("runtime export lost v2 source metadata or raw data: %v", err)
	}
	invokeRuntime(t, 1, "runtime-export", "--config-dir", directory, "--out", output)
	frozen := filepath.Join(t.TempDir(), "not-a-frozen-candidate.sqlite")
	invoke(t, 1, "sqlite-import", "--in", output, "--db", frozen)
	if _, err := os.Lstat(frozen); !os.IsNotExist(err) {
		t.Fatal("runtime archive created a frozen candidate")
	}
	rolledBack := invokeRuntime(t, 0, "runtime-rollback", "--config-dir", directory)
	if rolledBack.Runtime == nil || rolledBack.Runtime.Backend != "bolt" || rolledBack.Runtime.Database == boltPath || rolledBack.Runtime.Manifest != current.Manifest || rolledBack.Runtime.Archive == "" {
		t.Fatal("rollback selected stale Bolt data instead of preserving runtime writes")
	}
	if report := invoke(t, 0, "verify", "--in", output, "--db", rolledBack.Runtime.Database); report.Comparison == nil || !report.Comparison.Match {
		t.Fatal("rolled-back Bolt does not match current SQLite export")
	}
	if !bytes.Equal(before, readCommandFile(t, boltPath)) {
		t.Fatal("rollback overwrote the original Bolt file")
	}
	retried = invokeRuntime(t, 0, "runtime-rollback", "--config-dir", directory)
	if retried.Runtime == nil || !retried.Runtime.AlreadyActive || retried.Runtime.Manifest != current.Manifest {
		t.Fatal("rollback retry did not verify the active Bolt data")
	}
	invokeRuntime(t, 0, "runtime-verify", "--config-dir", directory)
}

func TestRuntimeCommandsRestoreBothArchiveVersionsAndInit(t *testing.T) {
	_, legacy := runtimeFixture(t)
	snapshot, err := backup.NewSnapshot("sqlite", legacy.Buckets)
	if err != nil {
		t.Fatal(err)
	}
	for name, archive := range map[string]*backup.Archive{"v1": legacy, "v2": snapshot} {
		t.Run(name, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "archive.json")
			if err := backup.Write(input, archive); err != nil {
				t.Fatal(err)
			}
			originalBytes := readCommandFile(t, input)
			directory := t.TempDir()
			restored := invokeRuntime(t, 0, "runtime-restore", "--config-dir", directory, "--in", input)
			if restored.Runtime == nil || restored.Runtime.Backend != "sqlite" || restored.Runtime.Manifest != archive.Manifest || restored.Runtime.AlreadyActive {
				t.Fatal("runtime restore lost archive data")
			}
			retried := invokeRuntime(t, 0, "runtime-restore", "--config-dir", directory, "--in", input)
			if retried.Runtime == nil || !retried.Runtime.AlreadyActive {
				t.Fatal("identical runtime restore retry was not verified")
			}
			output := filepath.Join(t.TempDir(), "exported.json")
			invokeRuntime(t, 0, "runtime-export", "--config-dir", directory, "--out", output)
			exported, err := backup.Read(output)
			if err != nil || exported.Manifest != archive.Manifest || !reflect.DeepEqual(exported.Buckets, archive.Buckets) {
				t.Fatalf("runtime restore/export changed complete raw tree: %v", err)
			}
			invokeRuntime(t, 1, "runtime-init", "--config-dir", directory)
			if !bytes.Equal(originalBytes, readCommandFile(t, input)) {
				t.Fatal("runtime restore changed its archive source")
			}
		})
	}
	directory := t.TempDir()
	initialized := invokeRuntime(t, 0, "runtime-init", "--config-dir", directory)
	if initialized.Runtime == nil || initialized.Runtime.Backend != "sqlite" || initialized.Runtime.Manifest.Buckets != 1 || initialized.Runtime.Manifest.Values != 1 {
		t.Fatal("fresh runtime did not contain only its defaults sentinel")
	}
	selection, err := runtimebackend.ReadSelection(directory)
	if err != nil || !selection.Initialize {
		t.Fatalf("fresh runtime did not request initial defaults: %v", err)
	}
	invokeRuntime(t, 0, "runtime-verify", "--config-dir", directory)
	retried := invokeRuntime(t, 0, "runtime-init", "--config-dir", directory)
	if retried.Runtime == nil || !retried.Runtime.AlreadyActive {
		t.Fatal("fresh-init retry did not verify the existing initial state")
	}
	// A backup can be taken between runtime-init and the first service start.
	// Restoring that precise empty-installation snapshot must still request
	// application defaults instead of leaving the service with empty ports.
	output := filepath.Join(t.TempDir(), "before-first-start.json")
	invokeRuntime(t, 0, "runtime-export", "--config-dir", directory, "--out", output)
	pending, err := backup.Read(output)
	if err != nil || !pending.Source.InitializeDefaults {
		t.Fatalf("fresh backup omitted explicit initialization metadata: %v", err)
	}
	restoredDirectory := t.TempDir()
	invokeRuntime(t, 0, "runtime-restore", "--config-dir", restoredDirectory, "--in", output)
	restoredSelection, err := runtimebackend.ReadSelection(restoredDirectory)
	if err != nil || !restoredSelection.Initialize {
		t.Fatalf("restored fresh snapshot lost its pending defaults: %v", err)
	}
}

func TestRuntimeRestoreDoesNotInferInitializationFromUnknownData(t *testing.T) {
	archive, err := backup.NewSnapshot("sqlite", []backup.Bucket{{
		Name:   []byte(backup.InitializationBucket),
		Values: []backup.Value{{Key: []byte(backup.InitializationKey), Data: []byte(backup.InitializationValue)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(t.TempDir(), "source-bolt.db")
	if err := backup.Restore(legacyPath, archive); err != nil {
		t.Fatal(err)
	}
	legacy, err := backup.Export(legacyPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for name, plain := range map[string]*backup.Archive{"v1": legacy, "v2": archive} {
		t.Run(name, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "unknown-marker.json")
			if err := backup.Write(input, plain); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			invokeRuntime(t, 0, "runtime-restore", "--config-dir", directory, "--in", input)
			selection, err := runtimebackend.ReadSelection(directory)
			if err != nil || selection.Initialize {
				t.Fatalf("unknown data was interpreted as initialization authority: %v", err)
			}
			output := filepath.Join(t.TempDir(), "exported.json")
			invokeRuntime(t, 0, "runtime-export", "--config-dir", directory, "--out", output)
			exported, err := backup.Read(output)
			if err != nil || exported.Source.InitializeDefaults || exported.Manifest != plain.Manifest || !reflect.DeepEqual(exported.Buckets, plain.Buckets) {
				t.Fatalf("unknown marker data or initialization intent changed: %v", err)
			}
		})
	}
}

func TestRuntimeCommandsRequireStoppedInstallationAndExplicitArguments(t *testing.T) {
	directory, archive := runtimeFixture(t)
	input := filepath.Join(t.TempDir(), "input.json")
	if err := backup.Write(input, archive); err != nil {
		t.Fatal(err)
	}
	lock, err := instancelock.Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, operation := range []string{"runtime-migrate", "runtime-init", "runtime-restore", "runtime-rollback", "runtime-export", "runtime-verify"} {
		t.Run(operation, func(t *testing.T) {
			args := []string{operation, "--config-dir", directory}
			if operation == "runtime-restore" {
				args = append(args, "--in", input)
			}
			output := filepath.Join(t.TempDir(), "not-exported.json")
			if operation == "runtime-export" {
				args = append(args, "--out", output)
			}
			invokeRuntime(t, 1, args...)
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("locked installation produced an export")
			}
			invokeRuntime(t, 2, operation)
		})
	}
	for _, args := range [][]string{
		{"runtime-restore", "--config-dir", directory},
		{"runtime-export", "--config-dir", directory},
		{"runtime-migrate", "--db", "unsupported"},
		{"runtime-verify", "--config-dir", directory, "--docker"},
	} {
		invokeRuntime(t, 2, args...)
	}
	if _, err := os.Lstat(filepath.Join(directory, runtimebackend.SelectionFilename)); !os.IsNotExist(err) {
		t.Fatal("rejected commands activated a backend")
	}
	var help bytes.Buffer
	if code := run([]string{"--help"}, &help, &help); code != 0 || !strings.Contains(help.String(), "Stop its service first") || !strings.Contains(help.String(), "runtime-restore") {
		t.Fatal("help omitted runtime maintenance boundaries")
	}
}

func TestRuntimeAuditErrorsAreExitTwoAndExportRemainsRecoverable(t *testing.T) {
	directory, _ := runtimeFixture(t)
	path := filepath.Join(directory, "bolt.db")
	database, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("touch")).Put([]byte("servers"), []byte(`{"credential-hash-must-not-be-printed":`))
	}); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	original := readCommandFile(t, path)
	for _, operation := range []string{"runtime-migrate", "runtime-rollback", "runtime-verify"} {
		invokeRuntime(t, 2, operation, "--config-dir", directory)
	}
	output := filepath.Join(t.TempDir(), "damaged-application-data.json")
	report := invokeRuntime(t, 2, "runtime-export", "--config-dir", directory, "--out", output)
	if report.Success || report.Audit == nil || !report.Audit.HasErrors() || report.Manifest == nil {
		t.Fatal("failed application audit was not reported after preserving the data")
	}
	archive, err := backup.Read(output)
	if err != nil || archive.Manifest != *report.Manifest {
		t.Fatalf("audit failure discarded the recoverable archive: %v", err)
	}
	target := t.TempDir()
	invokeRuntime(t, 2, "runtime-restore", "--config-dir", target, "--in", output)
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected audit created restore files: %v", err)
	}
	if !bytes.Equal(original, readCommandFile(t, path)) {
		t.Fatal("auditing or exporting changed the malformed source database")
	}
	if _, err := os.Lstat(filepath.Join(directory, runtimebackend.SelectionFilename)); !os.IsNotExist(err) {
		t.Fatal("audit failure activated another backend")
	}
}

func TestRuntimeRestoreRefusesDifferentOrCorruptInputsWithoutChangingSelection(t *testing.T) {
	directory, archive := runtimeFixture(t)
	migrated := invokeRuntime(t, 0, "runtime-migrate", "--config-dir", directory)
	marker := filepath.Join(directory, runtimebackend.SelectionFilename)
	markerBefore := readCommandFile(t, marker)
	databaseBefore := readCommandFile(t, migrated.Runtime.Database)
	input := filepath.Join(t.TempDir(), "different.json")
	different, err := backup.NewSnapshot("sqlite", []backup.Bucket{{Name: []byte("different-data")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.Write(input, different); err != nil {
		t.Fatal(err)
	}
	invokeRuntime(t, 1, "runtime-restore", "--config-dir", directory, "--in", input)
	for _, raw := range []string{
		`{"credential-hash-must-not-be-printed":`,
		`{"credential-hash-must-not-be-printed":"private-value"}`,
	} {
		if err := os.WriteFile(input, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		invokeRuntime(t, 1, "runtime-restore", "--config-dir", directory, "--in", input)
	}
	if !bytes.Equal(markerBefore, readCommandFile(t, marker)) || !bytes.Equal(databaseBefore, readCommandFile(t, migrated.Runtime.Database)) {
		t.Fatal("rejected restoration modified the active selection or data")
	}
	verified := invokeRuntime(t, 0, "runtime-verify", "--config-dir", directory)
	if verified.Runtime.Manifest != archive.Manifest {
		t.Fatal("rejected restoration changed the logical data")
	}
}
