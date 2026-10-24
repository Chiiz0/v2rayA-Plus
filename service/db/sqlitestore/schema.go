// Package sqlitestore implements the mutable runtime bucket store. Its schema
// is independent of the frozen migration candidate in db/sqlitebackup.
package sqlitestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/v2rayA/v2rayA/db/backup"
)

const ApplicationID = 0x56324152
const SchemaVersion = 1
const FormatName = "v2raya-sqlite-runtime"

var schema = []struct{ name, kind, table, ddl string }{
	{"runtime_meta", "table", "runtime_meta", `CREATE TABLE runtime_meta (
 id INTEGER PRIMARY KEY CHECK(id = 1),
 format TEXT NOT NULL,
 schema_version INTEGER NOT NULL
) STRICT`},
	{"kv_entries", "table", "kv_entries", `CREATE TABLE kv_entries (
 id INTEGER PRIMARY KEY CHECK(id > 0),
 parent_id INTEGER,
 parent_kind INTEGER NOT NULL DEFAULT 0 CHECK(parent_kind = 0),
 name BLOB NOT NULL CHECK(length(name) BETWEEN 1 AND 32768),
 kind INTEGER NOT NULL CHECK(kind IN (0, 1)),
 sequence BLOB,
 data BLOB,
 UNIQUE(id, kind),
 UNIQUE(parent_id, name),
 CHECK(parent_id IS NOT NULL OR kind = 0),
 CHECK(parent_id IS NULL OR parent_id < id),
 CHECK((kind = 0 AND typeof(sequence) = 'blob' AND length(sequence) = 8 AND data IS NULL)
    OR (kind = 1 AND sequence IS NULL AND typeof(data) = 'blob')),
 FOREIGN KEY(parent_id, parent_kind) REFERENCES kv_entries(id, kind) ON DELETE CASCADE
) STRICT`},
	{"sqlite_autoindex_kv_entries_1", "index", "kv_entries", ""},
	{"sqlite_autoindex_kv_entries_2", "index", "kv_entries", ""},
	{"kv_root_names", "index", "kv_entries", `CREATE UNIQUE INDEX kv_root_names ON kv_entries(name) WHERE parent_id IS NULL`},
	{"schema_migrations", "table", "schema_migrations", `CREATE TABLE schema_migrations (
 version INTEGER PRIMARY KEY CHECK(version > 0),
 schema_sha256 TEXT NOT NULL CHECK(length(schema_sha256) = 64)
) STRICT`},
}

func schemaDigest() string {
	h := sha256.New()
	for _, object := range schema {
		if object.ddl != "" {
			_, _ = h.Write([]byte(object.ddl + ";\n"))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func initialize(ctx context.Context, tx *sql.Tx, archive *backup.Archive) error {
	for _, object := range schema {
		if object.ddl != "" {
			if _, err := tx.ExecContext(ctx, object.ddl); err != nil {
				return sqlError("create runtime schema", err)
			}
		}
	}
	for _, statement := range []string{fmt.Sprintf("PRAGMA application_id = %d", ApplicationID), fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return sqlError("mark runtime schema", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO runtime_meta VALUES(1, ?, ?)", FormatName, SchemaVersion); err != nil {
		return sqlError("record runtime format", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations VALUES(?, ?)", SchemaVersion, schemaDigest()); err != nil {
		return sqlError("record runtime migration", err)
	}
	statement, err := tx.PrepareContext(ctx, "INSERT INTO kv_entries(id, parent_id, name, kind, sequence, data) VALUES(?, ?, ?, ?, ?, ?)")
	if err != nil {
		return sqlError("prepare runtime import", err)
	}
	defer statement.Close()
	var id int64
	var insert func(backup.Bucket, any) error
	insert = func(bucket backup.Bucket, parent any) error {
		id++
		current := id
		sequence := make([]byte, 8)
		binary.BigEndian.PutUint64(sequence, bucket.Sequence)
		if _, err := statement.ExecContext(ctx, current, parent, bucket.Name, 0, sequence, nil); err != nil {
			return sqlError("import runtime bucket", err)
		}
		for _, value := range bucket.Values {
			id++
			if _, err := statement.ExecContext(ctx, id, current, value.Key, 1, nil, nonNil(value.Data)); err != nil {
				return sqlError("import runtime value", err)
			}
		}
		for _, child := range bucket.Buckets {
			if err := insert(child, current); err != nil {
				return err
			}
		}
		return nil
	}
	for _, bucket := range archive.Buckets {
		if err := insert(bucket, nil); err != nil {
			return err
		}
	}
	return nil
}

func checkSchema(ctx context.Context, tx *sql.Tx) error {
	var application, version, count int
	var size int64
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&application); err != nil {
		return sqlError("read application ID", err)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return sqlError("read runtime version", err)
	}
	if application != ApplicationID || version != SchemaVersion {
		return errors.New("unsupported SQLite runtime format or version")
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*), coalesce(sum(length(CAST(name AS BLOB)) + length(CAST(tbl_name AS BLOB)) + coalesce(length(CAST(sql AS BLOB)), 0)), 0) FROM sqlite_schema").Scan(&count, &size); err != nil {
		return sqlError("inspect runtime schema", err)
	}
	if count != len(schema) || size > 64<<10 {
		return errors.New("unexpected SQLite runtime schema objects")
	}
	rows, err := tx.QueryContext(ctx, "SELECT name, type, tbl_name, sql FROM sqlite_schema")
	if err != nil {
		return sqlError("read runtime schema", err)
	}
	defer rows.Close()
	seen := make(map[string]bool)
	for rows.Next() {
		var name, kind, table string
		var ddl sql.NullString
		if err := rows.Scan(&name, &kind, &table, &ddl); err != nil {
			return sqlError("read runtime schema object", err)
		}
		matched := false
		for _, object := range schema {
			if name == object.name && kind == object.kind && table == object.table && ddl.String == object.ddl && ddl.Valid == (object.ddl != "") && !seen[name] {
				matched = true
				seen[name] = true
				break
			}
		}
		if !matched {
			return errors.New("SQLite runtime schema does not match its version")
		}
	}
	if err := rows.Err(); err != nil {
		return sqlError("read runtime schema", err)
	}
	if err := rows.Close(); err != nil {
		return sqlError("close schema query", err)
	}
	if len(seen) != len(schema) {
		return errors.New("incomplete SQLite runtime schema")
	}
	var format, digest string
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_meta").Scan(&count); err != nil || count != 1 {
		return errors.New("invalid SQLite runtime metadata")
	}
	if err := tx.QueryRowContext(ctx, "SELECT format, schema_version FROM runtime_meta WHERE id = 1 AND length(CAST(format AS BLOB)) < 100").Scan(&format, &version); err != nil || format != FormatName || version != SchemaVersion {
		return errors.New("invalid SQLite runtime metadata")
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		return errors.New("invalid SQLite runtime migration history")
	}
	if err := tx.QueryRowContext(ctx, "SELECT schema_sha256 FROM schema_migrations WHERE version = ? AND length(CAST(schema_sha256 AS BLOB)) = 64", SchemaVersion).Scan(&digest); err != nil || digest != schemaDigest() {
		return errors.New("invalid SQLite runtime schema checksum")
	}
	return nil
}

func checkBounds(ctx context.Context, tx *sql.Tx) error {
	var count, size int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*), coalesce(sum(length(name) + coalesce(length(data), 0)), 0) FROM kv_entries").Scan(&count, &size); err != nil {
		return sqlError("check runtime data bounds", err)
	}
	if count > backup.MaxEntries || size > backup.MaxDataBytes {
		return errors.New("SQLite runtime exceeds the backup entry or size limit")
	}
	return nil
}

func checkIntegrity(ctx context.Context, tx *sql.Tx) error {
	for _, query := range []string{"PRAGMA integrity_check", "PRAGMA foreign_key_check"} {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return sqlError("check runtime integrity", err)
		}
		valid, count := true, 0
		for rows.Next() {
			count++
			if query == "PRAGMA foreign_key_check" {
				valid = false
				break
			}
			var result string
			if err := rows.Scan(&result); err != nil || result != "ok" {
				valid = false
			}
		}
		err = rows.Err()
		closeErr := rows.Close()
		if !valid || err != nil || closeErr != nil || (query == "PRAGMA integrity_check" && count != 1) {
			return errors.New("SQLite runtime integrity check failed")
		}
	}
	return nil
}

type treeEntry struct {
	bucket   backup.Bucket
	children []*treeEntry
	depth    int
}

func snapshotTx(ctx context.Context, tx *sql.Tx) (*backup.Archive, error) {
	if err := checkSchema(ctx, tx); err != nil {
		return nil, err
	}
	if err := checkBounds(ctx, tx); err != nil {
		return nil, err
	}
	if err := checkIntegrity(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, parent_id, name, kind, sequence, data FROM kv_entries ORDER BY id")
	if err != nil {
		return nil, sqlError("read runtime snapshot", err)
	}
	defer rows.Close()
	buckets := make(map[int64]*treeEntry)
	var roots []*treeEntry
	for rows.Next() {
		var id int64
		var parent sql.NullInt64
		var name, sequence, data []byte
		var kind int
		if err := rows.Scan(&id, &parent, &name, &kind, &sequence, &data); err != nil {
			return nil, sqlError("read runtime entry", err)
		}
		var ancestor *treeEntry
		if parent.Valid {
			ancestor = buckets[parent.Int64]
			if ancestor == nil || parent.Int64 >= id {
				return nil, errors.New("invalid SQLite runtime parent")
			}
		}
		if kind == 0 {
			if len(sequence) != 8 {
				return nil, errors.New("invalid SQLite runtime sequence")
			}
			entry := &treeEntry{bucket: backup.Bucket{Name: name, Sequence: binary.BigEndian.Uint64(sequence), Buckets: []backup.Bucket{}, Values: []backup.Value{}}, depth: 1}
			if ancestor == nil {
				roots = append(roots, entry)
			} else {
				entry.depth = ancestor.depth + 1
				ancestor.children = append(ancestor.children, entry)
			}
			if entry.depth > backup.MaxDepth {
				return nil, errors.New("SQLite runtime exceeds bucket depth limit")
			}
			buckets[id] = entry
		} else if kind == 1 && ancestor != nil {
			ancestor.bucket.Values = append(ancestor.bucket.Values, backup.Value{Key: name, Data: nonNil(data)})
		} else {
			return nil, errors.New("invalid SQLite runtime entry")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError("read runtime snapshot", err)
	}
	var flatten func([]*treeEntry) []backup.Bucket
	flatten = func(entries []*treeEntry) []backup.Bucket {
		sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].bucket.Name, entries[j].bucket.Name) < 0 })
		result := make([]backup.Bucket, 0, len(entries))
		for _, entry := range entries {
			entry.bucket.Buckets = flatten(entry.children)
			sort.Slice(entry.bucket.Values, func(i, j int) bool { return bytes.Compare(entry.bucket.Values[i].Key, entry.bucket.Values[j].Key) < 0 })
			result = append(result, entry.bucket)
		}
		return result
	}
	return backup.NewSnapshot("sqlite", flatten(roots))
}
