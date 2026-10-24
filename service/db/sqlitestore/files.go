package sqlitestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/v2rayA/v2rayA/db/backup"
	"modernc.org/sqlite"
)

const maxFileBytes int64 = 512 << 20

var sidecars = []string{"-wal", "-journal", "-shm"}

// Open opens an existing runtime database. The caller must hold the instance
// lock. A frozen migration candidate is never promoted or changed in place.
func Open(path string) (*Store, error) {
	path, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	// Validate a recovered private copy before any SQLite connection can write
	// the original (including hot-journal recovery or enabling WAL).
	if _, err := SnapshotReadOnly(path); err != nil {
		return nil, err
	}
	info, err := regularInfo(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	writer, err := openDatabase(path, info, false)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = writer.Close()
		}
	}()
	if _, err := snapshotDatabase(ctx, writer); err != nil {
		return nil, err
	}
	var journal string
	if err := writer.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journal); err != nil || journal != "wal" {
		return nil, sqlError("enable runtime WAL", nonNilError(err))
	}
	reader, err := openDatabase(path, info, true)
	if err != nil {
		return nil, err
	}
	result := &Store{writer: writer, reader: reader}
	if err := checkConnection(ctx, writer, false); err != nil {
		_ = reader.Close()
		return nil, err
	}
	if err := checkConnection(ctx, reader, true); err != nil {
		_ = reader.Close()
		return nil, err
	}
	ok = true
	return result, nil
}

// Create atomically publishes a new 0600 runtime database. It preserves every
// bucket, byte and unsigned sequence. Existing targets and sidecars are refused.
// The caller holds the destination instance lock; this function never replaces
// the source archive or selects a runtime backend.
func Create(path string, archive *backup.Archive) error {
	if err := backup.Validate(archive); err != nil {
		return fmt.Errorf("invalid runtime import archive: %w", err)
	}
	path, err := canonicalPath(path)
	if err != nil {
		return err
	}
	if err := requireAbsent(path); err != nil {
		return err
	}
	directory, err := os.MkdirTemp(filepath.Dir(path), ".v2raya-runtime-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory) // Exclusively owned staging directory.
	stage := filepath.Join(directory, "runtime.sqlite")
	file, err := os.OpenFile(stage, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	info, err := regularInfo(stage)
	if err != nil {
		return err
	}
	database, err := openDatabase(stage, info, false)
	if err != nil {
		return err
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	var journal string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journal); err != nil || journal != "delete" {
		return sqlError("set runtime import journal", nonNilError(err))
	}
	if err := checkConnection(ctx, database, false); err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return sqlError("begin runtime import", err)
	}
	defer tx.Rollback()
	if err := initialize(ctx, tx, archive); err != nil {
		return err
	}
	actual, err := snapshotTx(ctx, tx)
	if err != nil {
		return err
	}
	if actual.Manifest != archive.Manifest {
		return errors.New("runtime import differs from source archive")
	}
	if err := tx.Commit(); err != nil {
		return sqlError("commit runtime import", err)
	}
	if err := database.Close(); err != nil {
		return sqlError("close runtime import", err)
	}
	// Recovery and verification use a new connection after commit/close.
	actual, err = SnapshotReadOnly(stage)
	if err != nil {
		return err
	}
	if actual.Manifest != archive.Manifest {
		return errors.New("runtime import readback differs from source archive")
	}
	file, err = os.OpenFile(stage, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr, closeErr := file.Sync(), file.Close()
	if syncErr != nil || closeErr != nil {
		return errors.New("sync runtime import failed")
	}
	if err := requireAbsent(path); err != nil {
		return err
	}
	if err := os.Link(stage, path); err != nil {
		return fmt.Errorf("publish runtime without overwrite: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

// SnapshotReadOnly snapshots a stopped runtime without opening the source with
// SQLite. The caller must hold the instance lock. Main/WAL/journal files are
// copied and checked twice; recovery happens only on the private copy. This
// preserves committed WAL changes without creating or changing source sidecars.
func SnapshotReadOnly(path string) (*backup.Archive, error) {
	path, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	copyPath, cleanup, err := copyBundle(ctx, path)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	info, err := regularInfo(copyPath)
	if err != nil {
		return nil, err
	}
	database, err := openDatabase(copyPath, info, false)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	return snapshotDatabase(ctx, database)
}

func snapshotDatabase(ctx context.Context, database *sql.DB) (*backup.Archive, error) {
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, sqlError("begin runtime verification", err)
	}
	defer tx.Rollback()
	archive, err := snapshotTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, sqlError("finish runtime verification", err)
	}
	return archive, nil
}

type checkedConnector struct {
	driver.Connector
	path string
	info os.FileInfo
}

func (c checkedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := checkPathIdentity(c.path, c.info); err != nil {
		return nil, err
	}
	connection, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkPathIdentity(c.path, c.info); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func openDatabase(path string, info os.FileInfo, readOnly bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Set("mode", "rw")
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	for _, value := range []string{"foreign_keys(1)", "trusted_schema(0)", "busy_timeout(2000)", "synchronous(FULL)", "ignore_check_constraints(0)"} {
		q.Add("_pragma", value)
	}
	u.RawQuery = q.Encode()
	connector, err := sqlite.NewConnector(u.String())
	if err != nil {
		return nil, sqlError("configure runtime connection", err)
	}
	database := sql.OpenDB(checkedConnector{Connector: connector, path: path, info: info})
	limit := 1
	if readOnly {
		limit = 4
	}
	database.SetMaxOpenConns(limit)
	database.SetMaxIdleConns(limit)
	return database, nil
}

func checkConnection(ctx context.Context, database *sql.DB, readOnly bool) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		return sqlError("open runtime connection", err)
	}
	defer connection.Close()
	wantQueryOnly := 0
	if readOnly {
		wantQueryOnly = 1
	}
	for _, setting := range []struct {
		name string
		want int
	}{{"foreign_keys", 1}, {"trusted_schema", 0}, {"busy_timeout", 2000}, {"synchronous", 2}, {"ignore_check_constraints", 0}, {"query_only", wantQueryOnly}} {
		var actual int
		if err := connection.QueryRowContext(ctx, "PRAGMA "+setting.name).Scan(&actual); err != nil || actual != setting.want {
			return sqlError("verify runtime connection settings", nonNilError(err))
		}
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("SQLite runtime path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func regularInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("SQLite runtime requires regular files without symlinks")
	}
	if info.Size() > maxFileBytes {
		return nil, errors.New("SQLite runtime file exceeds size limit")
	}
	return info, nil
}

func checkPathIdentity(path string, expected os.FileInfo) error {
	actual, err := regularInfo(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, actual) {
		return errors.New("SQLite runtime file was replaced")
	}
	for _, suffix := range sidecars {
		if _, err := regularInfo(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func requireAbsent(path string) error {
	for _, suffix := range append([]string{""}, sidecars...) {
		if _, err := os.Lstat(path + suffix); err == nil {
			return errors.New("SQLite runtime destination or sidecar already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

type copiedSource struct {
	suffix string
	info   os.FileInfo
	digest []byte
}

func copyBundle(ctx context.Context, path string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "v2raya-runtime-snapshot-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	copyPath := filepath.Join(directory, "runtime.sqlite")
	var sources []copiedSource
	var total int64
	for _, suffix := range append([]string{""}, sidecars...) {
		info, err := regularInfo(path + suffix)
		if err != nil {
			if suffix != "" && os.IsNotExist(err) {
				sources = append(sources, copiedSource{suffix: suffix})
				continue
			}
			return "", nil, err
		}
		total += info.Size()
		if total > 2*maxFileBytes {
			return "", nil, errors.New("SQLite runtime bundle exceeds size limit")
		}
		file, err := openRegularReadOnly(path + suffix)
		if err != nil {
			return "", nil, err
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			_ = file.Close()
			return "", nil, errors.New("SQLite runtime source changed before copy")
		}
		if suffix == "" {
			header := make([]byte, 100)
			if _, err := file.ReadAt(header, 0); err != nil || !bytes.Equal(header[:16], []byte("SQLite format 3\x00")) {
				_ = file.Close()
				return "", nil, errors.New("invalid SQLite runtime header")
			}
		}
		hash := sha256.New()
		var destination *os.File
		var output io.Writer = hash
		if suffix != "-shm" {
			destination, err = os.OpenFile(copyPath+suffix, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				_ = file.Close()
				return "", nil, err
			}
			output = io.MultiWriter(destination, hash)
		}
		n, copyErr := io.Copy(output, io.LimitReader(contextReader{ctx, file}, info.Size()+1))
		if destination != nil {
			copyErr = errors.Join(copyErr, destination.Close())
		}
		copyErr = errors.Join(copyErr, file.Close())
		if copyErr != nil || n != info.Size() {
			return "", nil, sqlError("copy runtime snapshot", nonNilError(copyErr))
		}
		sources = append(sources, copiedSource{suffix: suffix, info: info, digest: hash.Sum(nil)})
	}
	// Re-open only before SQLite opens the private copy; closing an arbitrary
	// fd for a live SQLite database can release POSIX locks held by this process.
	for _, source := range sources {
		info, err := regularInfo(path + source.suffix)
		if source.info == nil {
			if !os.IsNotExist(err) {
				return "", nil, errors.New("SQLite runtime sidecars changed during copy")
			}
			continue
		}
		if err != nil || !os.SameFile(source.info, info) || source.info.Size() != info.Size() || !source.info.ModTime().Equal(info.ModTime()) {
			return "", nil, errors.New("SQLite runtime source changed during copy")
		}
		file, err := openRegularReadOnly(path + source.suffix)
		if err != nil {
			return "", nil, err
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, info.Size()+1))
		after, statErr := file.Stat()
		closeErr := file.Close()
		current, pathErr := regularInfo(path + source.suffix)
		if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil || n != info.Size() || !bytes.Equal(hash.Sum(nil), source.digest) || !os.SameFile(info, after) || !os.SameFile(info, current) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
			return "", nil, errors.New("SQLite runtime source changed during verification")
		}
	}
	ok = true
	return copyPath, cleanup, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader contextReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(data)
}
func nonNilError(err error) error {
	if err == nil {
		return errors.New("unexpected result")
	}
	return err
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("runtime database published but directory sync failed; verify before retry")
	}
	return nil
}
