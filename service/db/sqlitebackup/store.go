package sqlitebackup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/v2rayA/v2rayA/db/backup"
)

// Write creates the complete candidate in one transaction. The caller owns the
// temporary file, driver configuration, close/sync and no-overwrite publication.
// Existing schema or application metadata is never replaced, even if empty.
func Write(ctx context.Context, database *sql.DB, archive *backup.Archive) error {
	if err := backup.Validate(archive); err != nil {
		return fmt.Errorf("invalid source archive: %w", err)
	}
	if backup.Audit(archive).HasErrors() {
		return errors.New("source archive has application-data audit errors")
	}
	connection, err := candidateConnection(ctx, database)
	if err != nil {
		return err
	}
	defer connection.Close()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return operationError(ctx, "begin SQLite conversion", err)
	}
	defer tx.Rollback()
	var count, application, version int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&count); err != nil {
		return operationError(ctx, "inspect SQLite destination", err)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&application); err != nil {
		return operationError(ctx, "inspect SQLite application ID", err)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return operationError(ctx, "inspect SQLite schema version", err)
	}
	if count != 0 || application != 0 || version != 0 {
		return errors.New("SQLite destination already contains schema or application metadata")
	}
	for _, object := range schemaObjects {
		if object.statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, object.statement); err != nil {
			return operationError(ctx, "create SQLite candidate schema", err)
		}
	}
	// These statements contain only compile-time constants, never source data.
	for _, statement := range []string{
		fmt.Sprintf("PRAGMA application_id = %d", ApplicationID),
		fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion),
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return operationError(ctx, "mark SQLite candidate schema", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, schema_sha256) VALUES(?, ?)", SchemaVersion, SchemaSHA256()); err != nil {
		return operationError(ctx, "record SQLite schema migration", err)
	}
	insert, err := tx.PrepareContext(ctx, "INSERT INTO kv_entries(id, parent_id, name, kind, sequence, data, value_sha256) VALUES(?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return operationError(ctx, "prepare SQLite data insertion", err)
	}
	defer insert.Close()
	var nextID int64
	var putBucket func(backup.Bucket, any) error
	putBucket = func(bucket backup.Bucket, parent any) error {
		nextID++
		id := nextID
		sequence := make([]byte, 8)
		binary.BigEndian.PutUint64(sequence, bucket.Sequence)
		if _, err := insert.ExecContext(ctx, id, parent, bucket.Name, 0, sequence, nil, nil); err != nil {
			return operationError(ctx, "insert SQLite bucket", err)
		}
		for _, value := range bucket.Values {
			nextID++
			if _, err := insert.ExecContext(ctx, nextID, id, value.Key, 1, nil, nonNilBytes(value.Data), value.SHA256); err != nil {
				return operationError(ctx, "insert SQLite value", err)
			}
		}
		for _, child := range bucket.Buckets {
			if err := putBucket(child, id); err != nil {
				return err
			}
		}
		return nil
	}
	for _, bucket := range archive.Buckets {
		if err := putBucket(bucket, nil); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO archive_meta(id, format, schema_version, state, archive_format, archive_version, created_at, source_backend, source_sha256, bucket_count, value_count, value_bytes, logical_sha256)
 VALUES(1, ?, ?, 'ready', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		FormatName, SchemaVersion, archive.Format, archive.Version, archive.CreatedAt, archive.Source.Backend, archive.Source.SHA256,
		archive.Manifest.Buckets, archive.Manifest.Values, archive.Manifest.ValueBytes, archive.Manifest.LogicalSHA256)
	if err != nil {
		return operationError(ctx, "record SQLite archive manifest", err)
	}
	// Read back the actual SQL values before committing, rather than validating
	// only the input or comparing counts. This also checks all schema invariants.
	if _, err := readArchive(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return operationError(ctx, "commit SQLite conversion", err)
	}
	return nil
}

// Read returns the original archive from a frozen migration candidate. All
// checks and reads share a single snapshot; no application runtime is loaded.
func Read(ctx context.Context, database *sql.DB) (*backup.Archive, error) {
	connection, err := candidateConnection(ctx, database)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	tx, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, operationError(ctx, "begin SQLite verification", err)
	}
	defer tx.Rollback()
	archive, err := readArchive(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, operationError(ctx, "finish SQLite verification", err)
	}
	return archive, nil
}

func candidateConnection(ctx context.Context, database *sql.DB) (*sql.Conn, error) {
	if database == nil {
		return nil, errors.New("SQLite database handle is nil")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, operationError(ctx, "open SQLite connection", err)
	}
	// Connection-local settings must be established before BEGIN: SQLite ignores
	// foreign_keys changes inside a transaction. Check the values, not just Exec.
	for _, setting := range []struct {
		statement, query string
		want             int
	}{
		{"PRAGMA foreign_keys = ON", "PRAGMA foreign_keys", 1},
		{"PRAGMA trusted_schema = OFF", "PRAGMA trusted_schema", 0},
		{"PRAGMA ignore_check_constraints = OFF", "PRAGMA ignore_check_constraints", 0},
		{"PRAGMA busy_timeout = 2000", "PRAGMA busy_timeout", 2000},
	} {
		var actual int
		_, err = connection.ExecContext(ctx, setting.statement)
		if err == nil {
			err = connection.QueryRowContext(ctx, setting.query).Scan(&actual)
		}
		if err != nil || actual != setting.want {
			_ = connection.Close()
			return nil, operationError(ctx, "configure SQLite connection", err)
		}
	}
	return connection, nil
}

func readArchive(ctx context.Context, tx *sql.Tx) (*backup.Archive, error) {
	if err := checkSchema(ctx, tx); err != nil {
		return nil, err
	}
	if err := checkLimits(ctx, tx); err != nil {
		return nil, err
	}
	if err := checkIntegrity(ctx, tx); err != nil {
		return nil, err
	}
	archive := new(backup.Archive)
	var format, state string
	var schemaVersion int
	err := tx.QueryRowContext(ctx, `SELECT format, schema_version, state, archive_format, archive_version, created_at, source_backend, source_sha256,
 bucket_count, value_count, value_bytes, logical_sha256 FROM archive_meta WHERE id = 1`).Scan(
		&format, &schemaVersion, &state, &archive.Format, &archive.Version, &archive.CreatedAt, &archive.Source.Backend, &archive.Source.SHA256,
		&archive.Manifest.Buckets, &archive.Manifest.Values, &archive.Manifest.ValueBytes, &archive.Manifest.LogicalSHA256)
	if err != nil || format != FormatName || schemaVersion != SchemaVersion || state != "ready" {
		return nil, errors.New("SQLite archive metadata is missing or unsupported")
	}
	archive.Buckets, err = readEntries(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := backup.Validate(archive); err != nil {
		return nil, errors.New("SQLite contents do not match the original archive manifest")
	}
	if backup.Audit(archive).HasErrors() {
		return nil, errors.New("SQLite archive has application-data audit errors")
	}
	return archive, nil
}

func checkSchema(ctx context.Context, tx *sql.Tx) error {
	var application, version int
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&application); err != nil {
		return operationError(ctx, "read SQLite application ID", err)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return operationError(ctx, "read SQLite schema version", err)
	}
	if application != ApplicationID || version != SchemaVersion {
		return errors.New("unsupported SQLite application ID or schema version")
	}
	var count, size int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*), coalesce(sum(length(name) + length(tbl_name) + coalesce(length(sql), 0)), 0) FROM sqlite_schema").Scan(&count, &size); err != nil {
		return operationError(ctx, "inspect SQLite schema", err)
	}
	if count != int64(len(schemaObjects)) || size > 64<<10 {
		return errors.New("SQLite candidate contains unexpected schema objects")
	}
	rows, err := tx.QueryContext(ctx, "SELECT name, type, tbl_name, sql FROM sqlite_schema")
	if err != nil {
		return operationError(ctx, "read SQLite schema", err)
	}
	defer rows.Close()
	expected := make(map[string]schemaObject, len(schemaObjects))
	for _, object := range schemaObjects {
		expected[object.name] = object
	}
	for rows.Next() {
		var name, kind, table string
		var statement sql.NullString
		if err := rows.Scan(&name, &kind, &table, &statement); err != nil {
			return operationError(ctx, "read SQLite schema object", err)
		}
		object, ok := expected[name]
		if !ok || object.kind != kind || object.table != table || strings.TrimSpace(statement.String) != object.statement || statement.Valid != (object.statement != "") {
			return errors.New("SQLite candidate schema does not match its declared version")
		}
		delete(expected, name)
	}
	if err := rows.Err(); err != nil {
		return operationError(ctx, "read SQLite schema", err)
	}
	if err := rows.Close(); err != nil {
		return operationError(ctx, "close SQLite schema query", err)
	}
	if len(expected) != 0 {
		return errors.New("SQLite candidate schema is incomplete")
	}
	var migrationCount int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&migrationCount); err != nil || migrationCount != 1 {
		return errors.New("SQLite migration history is incomplete or unsupported")
	}
	var schemaHash string
	if err := tx.QueryRowContext(ctx, "SELECT schema_sha256 FROM schema_migrations WHERE version = ? AND length(schema_sha256) = 64", SchemaVersion).Scan(&schemaHash); err != nil || schemaHash != SchemaSHA256() {
		return errors.New("SQLite schema migration checksum is invalid")
	}
	return nil
}

func checkLimits(ctx context.Context, tx *sql.Tx) error {
	var count, size int64
	err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(length(CAST(format AS BLOB)) + length(CAST(state AS BLOB))
 + length(CAST(archive_format AS BLOB)) + length(CAST(created_at AS BLOB)) + length(CAST(source_backend AS BLOB))
 + length(CAST(source_sha256 AS BLOB)) + length(CAST(logical_sha256 AS BLOB))), 0) FROM archive_meta`).Scan(&count, &size)
	if err != nil || count != 1 || size > backup.MaxArchiveBytes {
		return errors.New("SQLite archive metadata exceeds its limits or is incomplete")
	}
	var auxiliary int64
	err = tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(length(name) + coalesce(length(data), 0)), 0),
 coalesce(sum(coalesce(length(sequence), 0) + coalesce(length(value_sha256), 0)), 0) FROM kv_entries`).Scan(&count, &size, &auxiliary)
	if err != nil || count > backup.MaxEntries || size > backup.MaxDataBytes || auxiliary > backup.MaxEntries*64 {
		return errors.New("SQLite archive exceeds the entry or raw-data size limit")
	}
	return nil
}

func checkIntegrity(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return operationError(ctx, "check SQLite integrity", err)
	}
	count := 0
	valid := true
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil || result != "ok" {
			valid = false
		}
		count++
	}
	queryErr := rows.Err()
	closeErr := rows.Close()
	if !valid || count != 1 || queryErr != nil || closeErr != nil {
		return errors.New("SQLite integrity check failed")
	}
	rows, err = tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return operationError(ctx, "check SQLite foreign keys", err)
	}
	invalid := rows.Next()
	queryErr = rows.Err()
	closeErr = rows.Close()
	if invalid || queryErr != nil || closeErr != nil {
		return errors.New("SQLite foreign-key check failed")
	}
	return nil
}

type bucketEntry struct {
	bucket   backup.Bucket
	children []*bucketEntry
	depth    int
}

func readEntries(ctx context.Context, tx *sql.Tx) ([]backup.Bucket, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, parent_id, name, kind, sequence, data, value_sha256 FROM kv_entries ORDER BY id")
	if err != nil {
		return nil, operationError(ctx, "read SQLite entries", err)
	}
	defer rows.Close()
	buckets := make(map[int64]*bucketEntry)
	var roots []*bucketEntry
	for rows.Next() {
		var id int64
		var parent sql.NullInt64
		var name, sequence, data []byte
		var kind int
		var checksum sql.NullString
		if err := rows.Scan(&id, &parent, &name, &kind, &sequence, &data, &checksum); err != nil {
			return nil, operationError(ctx, "read SQLite entry", err)
		}
		var ancestor *bucketEntry
		if parent.Valid {
			ancestor = buckets[parent.Int64]
			if ancestor == nil || parent.Int64 >= id {
				return nil, errors.New("SQLite archive contains an invalid parent bucket")
			}
		}
		switch kind {
		case 0:
			if len(sequence) != 8 {
				return nil, errors.New("SQLite bucket has an invalid unsigned sequence")
			}
			entry := &bucketEntry{bucket: backup.Bucket{Name: name, Sequence: binary.BigEndian.Uint64(sequence), Buckets: []backup.Bucket{}, Values: []backup.Value{}}, depth: 1}
			if ancestor != nil {
				entry.depth = ancestor.depth + 1
				ancestor.children = append(ancestor.children, entry)
			} else {
				roots = append(roots, entry)
			}
			if entry.depth > backup.MaxDepth {
				return nil, errors.New("SQLite archive exceeds the bucket depth limit")
			}
			buckets[id] = entry
		case 1:
			if ancestor == nil || !checksum.Valid {
				return nil, errors.New("SQLite value has no parent bucket or checksum")
			}
			ancestor.bucket.Values = append(ancestor.bucket.Values, backup.Value{Key: name, Data: nonNilBytes(data), SHA256: checksum.String})
		default:
			return nil, errors.New("SQLite archive contains an unknown entry kind")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, operationError(ctx, "read SQLite entries", err)
	}
	var flatten func([]*bucketEntry) []backup.Bucket
	flatten = func(entries []*bucketEntry) []backup.Bucket {
		sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].bucket.Name, entries[j].bucket.Name) < 0 })
		result := make([]backup.Bucket, 0, len(entries))
		for _, entry := range entries {
			entry.bucket.Buckets = flatten(entry.children)
			sort.Slice(entry.bucket.Values, func(i, j int) bool { return bytes.Compare(entry.bucket.Values[i].Key, entry.bucket.Values[j].Key) < 0 })
			result = append(result, entry.bucket)
		}
		return result
	}
	return flatten(roots), nil
}

func nonNilBytes(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func operationError(ctx context.Context, operation string, _ error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	// SQLite diagnostics can include table contents or SQL expressions. Reports
	// must not expose credentials from an untrusted candidate's schema or values.
	return fmt.Errorf("%s failed", operation)
}
