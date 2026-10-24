// Package sqlitebackup stores an immutable, reversible migration candidate.
// It does not select or implement the application's runtime database backend.
package sqlitebackup

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	ApplicationID = 0x56324153
	SchemaVersion = 1
	FormatName    = "v2raya-sqlite-candidate"
)

type schemaObject struct {
	name, kind, table, statement string
}

// Both bucket and value names occupy one namespace, as they do in Bolt. The
// composite foreign key only permits bucket parents. Parent-first IDs enforce
// an acyclic tree without triggers; IDs are internal, never application IDs.
var schemaObjects = []schemaObject{
	{"archive_meta", "table", "archive_meta", `CREATE TABLE archive_meta (
 id INTEGER PRIMARY KEY CHECK(id = 1),
 format TEXT NOT NULL,
 schema_version INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state = 'ready'),
 archive_format TEXT NOT NULL,
 archive_version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 source_backend TEXT NOT NULL,
 source_sha256 TEXT NOT NULL,
 bucket_count INTEGER NOT NULL CHECK(bucket_count >= 0),
 value_count INTEGER NOT NULL CHECK(value_count >= 0),
 value_bytes INTEGER NOT NULL CHECK(value_bytes >= 0),
 logical_sha256 TEXT NOT NULL
) STRICT`},
	{"kv_entries", "table", "kv_entries", `CREATE TABLE kv_entries (
 id INTEGER PRIMARY KEY CHECK(id > 0),
 parent_id INTEGER,
 parent_kind INTEGER NOT NULL DEFAULT 0 CHECK(parent_kind = 0),
 name BLOB NOT NULL CHECK(length(name) BETWEEN 1 AND 32768),
 kind INTEGER NOT NULL CHECK(kind IN (0, 1)),
 sequence BLOB,
 data BLOB,
 value_sha256 TEXT,
 UNIQUE(id, kind),
 UNIQUE(parent_id, name),
 CHECK(parent_id IS NOT NULL OR kind = 0),
 CHECK(parent_id IS NULL OR parent_id < id),
 CHECK((kind = 0 AND typeof(sequence) = 'blob' AND length(sequence) = 8 AND data IS NULL AND value_sha256 IS NULL)
    OR (kind = 1 AND sequence IS NULL AND typeof(data) = 'blob' AND typeof(value_sha256) = 'text' AND length(value_sha256) = 64)),
 FOREIGN KEY(parent_id, parent_kind) REFERENCES kv_entries(id, kind)
) STRICT`},
	{"sqlite_autoindex_kv_entries_1", "index", "kv_entries", ""},
	{"sqlite_autoindex_kv_entries_2", "index", "kv_entries", ""},
	{"kv_root_names", "index", "kv_entries", `CREATE UNIQUE INDEX kv_root_names ON kv_entries(name) WHERE parent_id IS NULL`},
	{"schema_migrations", "table", "schema_migrations", `CREATE TABLE schema_migrations (
 version INTEGER PRIMARY KEY CHECK(version > 0),
 schema_sha256 TEXT NOT NULL CHECK(length(schema_sha256) = 64)
) STRICT`},
}

// SchemaSHA256 identifies the exact frozen schema, including indexes. It is
// independent of SQLite page layout and is recorded in schema_migrations.
func SchemaSHA256() string {
	hash := sha256.New()
	for _, object := range schemaObjects {
		if object.statement != "" {
			_, _ = hash.Write([]byte(strings.TrimSpace(object.statement) + ";\n"))
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}
