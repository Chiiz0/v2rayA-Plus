package sqlitebackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/v2rayA/v2rayA/common/instancelock"
	"github.com/v2rayA/v2rayA/db/backup"
	"go.etcd.io/bbolt"
	_ "modernc.org/sqlite"
)

const DriverVersion = "v1.61.0"

// Bound the physical input before SQLite parses it. Logical limits are checked
// separately by Read, including keys, payloads, tree depth and row count.
const maxDatabaseBytes int64 = 512 << 20

type EngineInfo struct {
	DriverVersion  string `json:"driverVersion"`
	SQLiteVersion  string `json:"sqliteVersion"`
	SQLiteSourceID string `json:"sqliteSourceId"`
}

type Result struct {
	Database       string          `json:"database"`
	AlreadyPresent bool            `json:"alreadyPresent"`
	SHA256         string          `json:"sha256"`
	Manifest       backup.Manifest `json:"manifest"`
	Engine         EngineInfo      `json:"engine"`
	SchemaVersion  int             `json:"schemaVersion"`
	SchemaSHA256   string          `json:"schemaSha256"`
}

// Engine reports the engine actually linked into this binary, without opening
// application configuration or a persistent database.
func Engine(ctx context.Context) (EngineInfo, error) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return EngineInfo{}, operationError(ctx, "open SQLite engine", err)
	}
	defer database.Close()
	return engineInfo(ctx, database)
}

func engineInfo(ctx context.Context, database *sql.DB) (EngineInfo, error) {
	info := EngineInfo{DriverVersion: DriverVersion}
	err := database.QueryRowContext(ctx, "SELECT sqlite_version(), sqlite_source_id()").Scan(&info.SQLiteVersion, &info.SQLiteSourceID)
	if err != nil {
		return EngineInfo{}, operationError(ctx, "identify SQLite engine", err)
	}
	return info, nil
}

func openDatabase(path string, readOnly bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "rw")
	if readOnly {
		query.Set("mode", "ro")
		query.Add("_pragma", "query_only(1)")
	}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "trusted_schema(0)")
	query.Add("_pragma", "busy_timeout(2000)")
	u.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, errors.New("open SQLite candidate failed")
	}
	// These offline operations never nest queries on separate connections.
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	return database, nil
}

// Import publishes a closed, fully verified candidate without overwriting any
// existing file. Retrying the exact same archive verifies and reuses the target.
// It never changes backend selection and does not open a live SQLite database.
func Import(ctx context.Context, path string, archive *backup.Archive) (Result, error) {
	return importCandidate(ctx, path, archive, nil)
}

// stageHook is only supplied by failure/crash tests, never by production callers.
func importCandidate(ctx context.Context, path string, archive *backup.Archive, stageHook func(string) error) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := backup.Validate(archive); err != nil {
		return Result{}, fmt.Errorf("invalid source archive: %w", err)
	}
	if archive.Format != backup.FormatName || archive.Version != backup.FormatVersion {
		return Result{}, errors.New("frozen SQLite candidates require an original version 1 Bolt archive")
	}
	if backup.Audit(archive).HasErrors() {
		return Result{}, errors.New("source archive has application-data audit errors")
	}
	path, unlock, err := lockDirectory(path)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	if _, err := os.Lstat(path); err == nil {
		actual, result, err := verifyCandidate(ctx, path)
		if err != nil {
			return Result{}, err
		}
		if !sameArchive(actual, archive) {
			return Result{}, errors.New("existing SQLite candidate belongs to a different archive; choose a new destination")
		}
		// A previous process may have exited after link but before directory
		// fsync. A successful retry must finish durability, not merely validate.
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return Result{}, err
		}
		result.AlreadyPresent = true
		return result, nil
	} else if !os.IsNotExist(err) {
		return Result{}, err
	}
	if err := rejectSidecars(path); err != nil {
		return Result{}, err
	}
	stage, cleanup, err := privateFile(filepath.Dir(path))
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	if err := writeCandidate(ctx, stage, archive); err != nil {
		return Result{}, err
	}
	checkpoint := func(name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stageHook != nil {
			return stageHook(name)
		}
		return nil
	}
	if err := checkpoint("committed"); err != nil {
		return Result{}, err
	}
	actual, result, err := verifyCandidate(ctx, stage)
	if err != nil {
		return Result{}, err
	}
	if !sameArchive(actual, archive) {
		return Result{}, errors.New("SQLite conversion readback does not match the source archive")
	}
	file, err := os.OpenFile(stage, os.O_RDWR, 0)
	if err != nil {
		return Result{}, err
	}
	syncErr, closeErr := file.Sync(), file.Close()
	if syncErr != nil || closeErr != nil {
		return Result{}, errors.New("sync SQLite candidate failed")
	}
	if err := checkpoint("synced"); err != nil {
		return Result{}, err
	}
	if err := rejectSidecars(path); err != nil {
		return Result{}, err
	}
	// Link is atomic and fails if any destination appeared, including a symlink.
	if err := os.Link(stage, path); err != nil {
		return Result{}, fmt.Errorf("publish SQLite candidate without overwrite: %w", err)
	}
	if err := checkpoint("published"); err != nil {
		return Result{}, fmt.Errorf("SQLite candidate is published; verify the destination before retrying: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return Result{}, err
	}
	result.Database = path
	return result, nil
}

func writeCandidate(ctx context.Context, path string, archive *backup.Archive) error {
	database, err := openDatabase(path, false)
	if err != nil {
		return err
	}
	defer database.Close()
	var journal string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journal); err != nil || journal != "delete" {
		return operationError(ctx, "set SQLite rollback journal mode", err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA synchronous = FULL"); err != nil {
		return operationError(ctx, "set SQLite durability", err)
	}
	var synchronous int
	if err := database.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		return operationError(ctx, "verify SQLite durability", err)
	}
	if err := Write(ctx, database, archive); err != nil {
		return err
	}
	if err := database.Close(); err != nil {
		return errors.New("close SQLite candidate failed")
	}
	return rejectSidecars(path)
}

// Verify reads a safely opened regular file into a private snapshot. SQLite
// parses only that copy, never reopens a user-controlled source pathname. WAL,
// shared-memory and journal sidecars are rejected, including empty sidecars.
func Verify(ctx context.Context, path string) (*backup.Archive, Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, Result{}, err
	}
	path, unlock, err := lockDirectory(path)
	if err != nil {
		return nil, Result{}, err
	}
	defer unlock()
	return verifyCandidate(ctx, path)
}

func verifyCandidate(ctx context.Context, path string) (*backup.Archive, Result, error) {
	snapshot, digest, cleanup, err := snapshotFile(ctx, path)
	if err != nil {
		return nil, Result{}, err
	}
	defer cleanup()
	database, err := openDatabase(snapshot, true)
	if err != nil {
		return nil, Result{}, err
	}
	defer database.Close()
	archive, err := Read(ctx, database)
	if err != nil {
		return nil, Result{}, err
	}
	engine, err := engineInfo(ctx, database)
	if err != nil {
		return nil, Result{}, err
	}
	return archive, Result{Database: path, SHA256: digest, Manifest: archive.Manifest, Engine: engine, SchemaVersion: SchemaVersion, SchemaSHA256: SchemaSHA256()}, nil
}

func sameArchive(a, b *backup.Archive) bool {
	return a.Format == b.Format && a.Version == b.Version && a.CreatedAt == b.CreatedAt && a.Source == b.Source && a.Manifest == b.Manifest
}

func lockDirectory(path string) (string, func(), error) {
	switch filepath.Base(path) {
	case "bolt.db", "boltv4.db", instancelock.Filename:
		return "", nil, errors.New("SQLite candidate must not use a service database or instance-lock filename")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", nil, err
	}
	lock, err := instancelock.Acquire(parent)
	if err != nil {
		return "", nil, err
	}
	var legacy []*bbolt.DB
	unlock := func() {
		for _, database := range legacy {
			_ = database.Close()
		}
		_ = lock.Close()
	}
	// Older service versions do not know the directory lock. Hold their existing
	// database locks as well; never create a missing legacy database.
	for _, name := range []string{"bolt.db", "boltv4.db"} {
		candidate := filepath.Join(parent, name)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			continue
		} else if err != nil {
			unlock()
			return "", nil, err
		}
		database, err := bbolt.Open(candidate, 0600, &bbolt.Options{ReadOnly: true, Timeout: 2 * time.Second, OpenFile: func(path string, _ int, _ os.FileMode) (*os.File, error) { return openRegularReadOnly(path) }})
		if err != nil {
			unlock()
			return "", nil, errors.New("cannot acquire existing Bolt database lock; stop the service or use an isolated copy")
		}
		legacy = append(legacy, database)
	}
	return filepath.Join(parent, filepath.Base(absolute)), unlock, nil
}

func rejectSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return errors.New("SQLite sidecar exists; only closed offline candidates without WAL or journals are supported")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// The directory is exclusively owned by this operation. Cleanup is bounded to
// its known files; crash orphans are never accepted as published candidates.
func privateFile(parent string) (string, func(), error) {
	directory, err := os.MkdirTemp(parent, ".v2raya-sqlite-*")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(directory, "candidate.sqlite")
	cleanup := func() {
		for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
			_ = os.Remove(path + suffix)
		}
		_ = os.Remove(directory)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func snapshotFile(ctx context.Context, path string) (string, string, func(), error) {
	if err := rejectSidecars(path); err != nil {
		return "", "", nil, err
	}
	source, err := openRegularReadOnly(path)
	if err != nil {
		return "", "", nil, err
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil {
		return "", "", nil, err
	}
	if before.Size() < 100 || before.Size() > maxDatabaseBytes {
		return "", "", nil, errors.New("SQLite candidate has an invalid or unsupported file size")
	}
	header := make([]byte, 100)
	if _, err := source.ReadAt(header, 0); err != nil || !bytes.Equal(header[:16], []byte("SQLite format 3\x00")) || header[18] != 1 || header[19] != 1 {
		return "", "", nil, errors.New("expected a closed SQLite candidate in rollback journal mode")
	}
	snapshot, cleanup, err := privateFile(filepath.Dir(path))
	if err != nil {
		return "", "", nil, err
	}
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	destination, err := os.OpenFile(snapshot, os.O_WRONLY, 0)
	if err != nil {
		return "", "", nil, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(contextReader{ctx, source}, before.Size()+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || n != before.Size() {
		return "", "", nil, operationError(ctx, "snapshot SQLite candidate", copyErr)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", "", nil, err
	}
	hash.Reset()
	n, err = io.Copy(hash, io.LimitReader(contextReader{ctx, source}, before.Size()+1))
	if err != nil || n != before.Size() || digest != hex.EncodeToString(hash.Sum(nil)) {
		return "", "", nil, errors.New("SQLite source changed during verification")
	}
	after, err := source.Stat()
	if err != nil {
		return "", "", nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", "", nil, errors.New("SQLite source changed during verification")
	}
	if err := rejectSidecars(path); err != nil {
		return "", "", nil, err
	}
	ok = true
	return snapshot, digest, cleanup, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("SQLite candidate is published but opening the directory for sync failed; verify before retrying: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("SQLite candidate is published but directory sync failed; verify the destination before retrying")
	}
	return nil
}
