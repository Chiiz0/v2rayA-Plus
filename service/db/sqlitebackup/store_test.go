package sqlitebackup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
	"go.etcd.io/bbolt"
	_ "modernc.org/sqlite"
)

func storeTestArchive(t *testing.T, additional func(*bbolt.Tx) error) *backup.Archive {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	database, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		root, err := tx.CreateBucket([]byte{0, 255, 1})
		if err != nil {
			return err
		}
		if err := root.SetSequence(math.MaxUint64); err != nil {
			return err
		}
		if err := root.Put([]byte{0, 254}, []byte{}); err != nil {
			return err
		}
		child, err := root.CreateBucket([]byte("nested\x00bucket"))
		if err != nil {
			return err
		}
		if err := child.SetSequence(1 << 63); err != nil {
			return err
		}
		if err := child.Put([]byte{255, 0, 2}, []byte{0, 1, 255, 0}); err != nil {
			return err
		}
		if err := child.Put([]byte("'); DROP TABLE archive_meta; --"), []byte("bound as bytes")); err != nil {
			return err
		}
		if _, err := child.CreateBucket([]byte("empty")); err != nil {
			return err
		}
		for name, values := range map[string]map[string]string{
			"accounts":      {"用户": `"sensitive-password-hash-must-not-appear"`},
			"touch":         {"servers": `[{"serverObj":{"protocol":"vless","net":"xhttp","encryption":"none","xhttpExtra":{"large":9007199254740993,"huge":18446744073709551615,"zero":0,"disabled":false,"negative":-1,"decimal":1.2300}}}]`, "subscriptions": `[]`},
			"dockerProxies": {"19080": `{"port":19080,"which":{"_type":"server","id":1},"core":"xray","status":"paused","future":9007199254740993}`},
		} {
			bucket, err := tx.CreateBucket([]byte(name))
			if err != nil {
				return err
			}
			for key, value := range values {
				if err := bucket.Put([]byte(key), []byte(value)); err != nil {
					return err
				}
			}
		}
		if additional != nil {
			return additional(tx)
		}
		return nil
	})
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("fixture failed: %v, close: %v", err, closeErr)
	}
	archive, err := backup.Export(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func storeTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	// These tests never open configured application paths or use the
	// application's DB singleton.
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func storeExec(t *testing.T, database *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(), statement, args...); err != nil {
		t.Fatal(err)
	}
}

func requireReadFailure(t *testing.T, database *sql.DB) error {
	t.Helper()
	archive, err := Read(context.Background(), database)
	if err == nil || archive != nil {
		t.Fatal("invalid SQLite candidate was accepted")
	}
	if strings.Contains(err.Error(), "sensitive-password-hash-must-not-appear") {
		t.Fatalf("verification leaked credentials: %v", err)
	}
	return err
}

func TestStoreFullByteRoundTrip(t *testing.T) {
	archive := storeTestArchive(t, nil)
	database := storeTestDatabase(t)
	if err := Write(context.Background(), database, archive); err != nil {
		t.Fatal(err)
	}
	actual, err := Read(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, archive) {
		t.Fatal("SQLite changed archive bytes, metadata, sequence, hierarchy or ordering")
	}
	var sequence []byte
	if err := database.QueryRow("SELECT sequence FROM kv_entries WHERE parent_id IS NULL AND name = ?", []byte{0, 255, 1}).Scan(&sequence); err != nil || !bytes.Equal(sequence, bytes.Repeat([]byte{255}, 8)) {
		t.Fatalf("uint64 sequence was not preserved as an 8-byte blob: %x, %v", sequence, err)
	}
	var kind string
	var size int
	if err := database.QueryRow("SELECT typeof(data), length(data) FROM kv_entries WHERE name = ? AND kind = 1", []byte{0, 254}).Scan(&kind, &size); err != nil || kind != "blob" || size != 0 {
		t.Fatalf("empty value became NULL or text: %s, %d, %v", kind, size, err)
	}
	// Recovery uses the existing archive contract and restores every original
	// Bolt byte, including unknown values and values that are not JSON at all.
	restored := filepath.Join(t.TempDir(), "restored.db")
	if err := backup.Restore(restored, actual); err != nil {
		t.Fatal(err)
	}
	again, err := backup.Export(restored, time.Second)
	if err != nil || again.Manifest != archive.Manifest || !reflect.DeepEqual(again.Buckets, archive.Buckets) {
		t.Fatalf("reverse conversion differs: %v", err)
	}
	if err := Write(context.Background(), database, archive); err == nil {
		t.Fatal("conversion overwrote an existing candidate")
	}
	after, err := Read(context.Background(), database)
	if err != nil || !reflect.DeepEqual(after, archive) {
		t.Fatal("refused repeated conversion changed the candidate")
	}
}

func TestStorePreservesValidLongArchiveTimestamp(t *testing.T) {
	archive := storeTestArchive(t, nil)
	// RFC3339 accepts fractional seconds beyond nanosecond precision. Preserve
	// the accepted archive string instead of imposing a tighter metadata limit.
	archive.CreatedAt = "2026-10-23T00:00:00." + strings.Repeat("1", 5000) + "Z"
	if err := backup.Validate(archive); err != nil {
		t.Fatal(err)
	}
	database := storeTestDatabase(t)
	if err := Write(context.Background(), database, archive); err != nil {
		t.Fatal(err)
	}
	actual, err := Read(context.Background(), database)
	if err != nil || actual.CreatedAt != archive.CreatedAt {
		t.Fatalf("valid timestamp was not preserved: %v", err)
	}
}

func TestStoreRejectsExistingSchemaAndMarkers(t *testing.T) {
	archive := storeTestArchive(t, nil)
	for _, setup := range []string{"CREATE TABLE existing(value BLOB)", "PRAGMA application_id = 123", "PRAGMA user_version = 2"} {
		t.Run(setup, func(t *testing.T) {
			database := storeTestDatabase(t)
			storeExec(t, database, setup)
			if err := Write(context.Background(), database, archive); err == nil {
				t.Fatal("existing destination was accepted")
			}
			var count int
			if err := database.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name = 'archive_meta'").Scan(&count); err != nil || count != 0 {
				t.Fatal("refused destination gained candidate schema")
			}
		})
	}
}

func TestStoreRejectsModifiedSchemaAndContents(t *testing.T) {
	archive := storeTestArchive(t, nil)
	for _, test := range []struct {
		name, statement string
		args            []any
	}{
		{"application", "PRAGMA application_id = 123", nil},
		{"version", "PRAGMA user_version = 2", nil},
		{"history", "DELETE FROM schema_migrations", nil},
		{"history checksum", "UPDATE schema_migrations SET schema_sha256 = ?", []any{strings.Repeat("0", 64)}},
		{"extra table", "CREATE TABLE extra(value BLOB)", nil},
		{"extra view", "CREATE VIEW extra AS SELECT * FROM kv_entries", nil},
		{"extra index", "CREATE INDEX extra ON kv_entries(kind)", nil},
		{"extra trigger", "CREATE TRIGGER extra AFTER UPDATE ON kv_entries BEGIN SELECT 'sensitive-password-hash-must-not-appear'; END", nil},
		{"missing index", "DROP INDEX kv_root_names", nil},
		{"missing metadata", "DELETE FROM archive_meta", nil},
		{"metadata", "UPDATE archive_meta SET logical_sha256 = ?", []any{strings.Repeat("0", 64)}},
		{"raw value", "UPDATE kv_entries SET data = ? WHERE name = ? AND kind = 1", []any{[]byte("sensitive-password-hash-must-not-appear"), []byte{0, 254}}},
		{"sequence", "UPDATE kv_entries SET sequence = ? WHERE parent_id IS NULL AND name = ?", []any{make([]byte, 8), []byte{0, 255, 1}}},
		{"empty bucket removed", "DELETE FROM kv_entries WHERE kind = 0 AND name = ?", []any{[]byte("empty")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := storeTestDatabase(t)
			if err := Write(context.Background(), database, archive); err != nil {
				t.Fatal(err)
			}
			storeExec(t, database, test.statement, test.args...)
			requireReadFailure(t, database)
		})
	}
}

func TestStoreSchemaEnforcesTreeInvariants(t *testing.T) {
	database := storeTestDatabase(t)
	if err := Write(context.Background(), database, storeTestArchive(t, nil)); err != nil {
		t.Fatal(err)
	}
	var root, value int64
	if err := database.QueryRow("SELECT min(id) FROM kv_entries WHERE kind = 0").Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT min(id) FROM kv_entries WHERE kind = 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, statement string
		args            []any
	}{
		{"missing parent", "INSERT INTO kv_entries VALUES(1001,1000,0,?,0,?,NULL,NULL)", []any{[]byte("x"), make([]byte, 8)}},
		{"value parent", "INSERT INTO kv_entries VALUES(1001,?,0,?,0,?,NULL,NULL)", []any{value, []byte("x"), make([]byte, 8)}},
		{"self cycle", "INSERT INTO kv_entries VALUES(1001,1001,0,?,0,?,NULL,NULL)", []any{[]byte("x"), make([]byte, 8)}},
		{"text name", "INSERT INTO kv_entries VALUES(1001,?,0,?,0,?,NULL,NULL)", []any{root, "text", make([]byte, 8)}},
		{"empty name", "INSERT INTO kv_entries VALUES(1001,?,0,?,0,?,NULL,NULL)", []any{root, []byte{}, make([]byte, 8)}},
		{"bad sequence", "INSERT INTO kv_entries VALUES(1001,?,0,?,0,?,NULL,NULL)", []any{root, []byte("x"), make([]byte, 7)}},
		{"root value", "INSERT INTO kv_entries VALUES(1001,NULL,0,?,1,NULL,?,?)", []any{[]byte("x"), []byte{}, strings.Repeat("0", 64)}},
		{"null value", "INSERT INTO kv_entries VALUES(1001,?,0,?,1,NULL,NULL,?)", []any{root, []byte("x"), strings.Repeat("0", 64)}},
		{"duplicate root", "INSERT INTO kv_entries VALUES(1001,NULL,0,?,0,?,NULL,NULL)", []any{[]byte{0, 255, 1}, make([]byte, 8)}},
		{"bucket value collision", "INSERT INTO kv_entries VALUES(1001,?,0,?,0,?,NULL,NULL)", []any{root, []byte{0, 254}, make([]byte, 8)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := database.Exec(test.statement, test.args...); err == nil {
				t.Fatal("invalid tree entry was accepted")
			}
		})
	}
	if _, err := Read(context.Background(), database); err != nil {
		t.Fatalf("rejected inserts changed the candidate: %v", err)
	}
}

func TestStoreDetectsDisabledForeignKeyAndCheckWrites(t *testing.T) {
	for _, corrupt := range []string{
		"PRAGMA foreign_keys = OFF; UPDATE kv_entries SET parent_id = 999999 WHERE kind = 1",
		"PRAGMA ignore_check_constraints = ON; UPDATE kv_entries SET data = NULL WHERE kind = 1",
	} {
		t.Run(corrupt, func(t *testing.T) {
			database := storeTestDatabase(t)
			if err := Write(context.Background(), database, storeTestArchive(t, nil)); err != nil {
				t.Fatal(err)
			}
			// For an orphan, also disable the ordering CHECK to emulate a file
			// produced by a connection that did not enforce our constraints.
			storeExec(t, database, "PRAGMA ignore_check_constraints = ON")
			storeExec(t, database, corrupt)
			requireReadFailure(t, database)
		})
	}
}

func TestStoreRollsBackSchemaAndDataOnFullDatabase(t *testing.T) {
	archive := storeTestArchive(t, func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte{0, 255, 1}).Put([]byte("large"), bytes.Repeat([]byte{255}, 512<<10))
	})
	database := storeTestDatabase(t)
	storeExec(t, database, "PRAGMA page_size = 4096")
	storeExec(t, database, "PRAGMA max_page_count = 16")
	if err := Write(context.Background(), database, archive); err == nil {
		t.Fatal("conversion unexpectedly fit within the page limit")
	}
	for _, query := range []string{"SELECT count(*) FROM sqlite_schema", "PRAGMA application_id", "PRAGMA user_version"} {
		var value int
		if err := database.QueryRow(query).Scan(&value); err != nil || value != 0 {
			t.Fatalf("failed conversion left partial state: %s: %d, %v", query, value, err)
		}
	}
}

func TestStoreContextAndAuditRejection(t *testing.T) {
	archive := storeTestArchive(t, nil)
	database := storeTestDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Write(ctx, database, archive); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled conversion: %v", err)
	}
	if _, err := Read(ctx, database); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	invalid := storeTestArchive(t, func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("touch")).Put([]byte("servers"), []byte("["))
	})
	if err := Write(context.Background(), database, invalid); err == nil {
		t.Fatal("malformed known application data was accepted")
	}
	var count int
	if err := database.QueryRow("SELECT count(*) FROM sqlite_schema").Scan(&count); err != nil || count != 0 {
		t.Fatal("preflight failure created candidate schema")
	}
	if err := Write(context.Background(), nil, archive); err == nil {
		t.Fatal("nil database handle was accepted")
	}
}

func TestStoreReadEnforcesCountAndDepthLimits(t *testing.T) {
	archive := storeTestArchive(t, nil)
	t.Run("count", func(t *testing.T) {
		database := storeTestDatabase(t)
		if err := Write(context.Background(), database, archive); err != nil {
			t.Fatal(err)
		}
		storeExec(t, database, `WITH RECURSIVE entries(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM entries WHERE n < ?)
 INSERT INTO kv_entries(id, parent_id, name, kind, sequence)
 SELECT 1000+n, NULL, CAST('generated-' || n AS BLOB), 0, zeroblob(8) FROM entries`, backup.MaxEntries)
		if err := requireReadFailure(t, database); !strings.Contains(err.Error(), "limit") {
			t.Fatalf("entry limit was not enforced before decoding: %v", err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		database := storeTestDatabase(t)
		if err := Write(context.Background(), database, archive); err != nil {
			t.Fatal(err)
		}
		parent := int64(1)
		for i := 0; i < backup.MaxDepth; i++ {
			id := int64(1000 + i)
			storeExec(t, database, "INSERT INTO kv_entries(id, parent_id, name, kind, sequence) VALUES(?, ?, ?, 0, ?)", id, parent, []byte("depth"), make([]byte, 8))
			parent = id
		}
		if err := requireReadFailure(t, database); !strings.Contains(err.Error(), "depth") {
			t.Fatalf("depth limit was not enforced before flattening: %v", err)
		}
	})
}
