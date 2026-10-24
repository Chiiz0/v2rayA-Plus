package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/db/runtimebackend"
	"github.com/v2rayA/v2rayA/db/sqlitestore"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

const databaseOpenTimeout = 2 * time.Second

var databaseMu sync.Mutex
var database databaseState

type databaseState struct {
	handle        *bbolt.DB
	sqlite        *sqlitestore.Store
	retired       []*bbolt.DB
	backend       store.Store
	selection     runtimebackend.Selection
	selected      bool
	sourcePath    string
	initialized   bool
	readOnly      bool
	temporaryPath string
	temporaryDir  string
	initErr       error
}

// SetReadOnly selects an isolated writable snapshot for subsequent DB accesses.
// Call it before Init or OpenExisting, while holding the instance lock.
func SetReadOnly() {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	database.readOnly = true
}

// OpenExisting opens the selected backend and retains compatibility Bolt locks
// before startup changes network or configuration state. Only an unselected,
// absent default Bolt file is left uninitialized for legacy migration.
func OpenExisting() error {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	return initDatabaseLocked(true)
}

// Init opens the database once, returning errors so startup can release its
// instance lock and other resources normally.
func Init() error {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	return initDatabaseLocked(false)
}

func initDatabaseLocked(existingOnly bool) error {
	if database.initialized {
		return database.initErr
	}
	config := conf.GetEnvironmentConfig()
	return database.openSelected(config.Config, config.DatabaseBackend, existingOnly, databaseOpenTimeout)
}

func (state *databaseState) selectBackend(configDir, expected string) error {
	if state.selected {
		return nil
	}
	selection, err := runtimebackend.ReadSelection(configDir)
	if err != nil {
		return err
	}
	if expected != "" && expected != "auto" && expected != selection.Backend {
		return fmt.Errorf("database backend %q does not match the selected backend %q; use the offline database tool to change backends", expected, selection.Backend)
	}
	state.selection = selection
	state.sourcePath = filepath.Join(configDir, selection.Database)
	state.selected = true
	return nil
}

func (state *databaseState) openSelected(configDir, expected string, existingOnly bool, timeout time.Duration) error {
	if state.initialized {
		return state.initErr
	}
	if err := state.selectBackend(configDir, expected); err != nil {
		state.initialized, state.initErr = true, err
		return err
	}
	// An old binary always opens bolt.db. Hold its shared lock even when the
	// selected runtime is SQLite or a separately restored Bolt generation.
	retiredPath := filepath.Join(configDir, "bolt.db")
	if state.sourcePath != retiredPath && len(state.retired) == 0 {
		if _, err := os.Lstat(retiredPath); err == nil {
			handle, err := openReadOnly(retiredPath, timeout)
			if err != nil {
				state.initialized = true
				state.initErr = fmt.Errorf("lock retired Bolt database (stop any older v2rayA instance): %w", err)
				return state.initErr
			}
			state.retired = append(state.retired, handle)
		} else {
			state.initialized = true
			state.initErr = fmt.Errorf("retired Bolt compatibility lock is unavailable; repair the offline backend selection before starting: %w", err)
			return state.initErr
		}
	}
	if state.selection.Backend == "bolt" {
		return state.openBolt(state.sourcePath, existingOnly, state.selection.Present, timeout)
	}
	state.initialized = true
	state.initErr = state.openSQLite(state.sourcePath)
	return state.initErr
}

func (state *databaseState) openSQLite(path string) (err error) {
	defer func() {
		if err != nil && state.temporaryDir != "" {
			err = errors.Join(err, os.RemoveAll(state.temporaryDir))
			state.temporaryDir = ""
		}
	}()
	if state.readOnly {
		archive, err := sqlitestore.SnapshotReadOnly(path)
		if err != nil {
			return fmt.Errorf("snapshot report SQLite database: %w", err)
		}
		state.temporaryDir, err = os.MkdirTemp("", "v2raya_tmp_sqlite_")
		if err != nil {
			return err
		}
		path = filepath.Join(state.temporaryDir, "report.sqlite")
		if err = sqlitestore.Create(path, archive); err != nil {
			return err
		}
	}
	state.sqlite, err = sqlitestore.Open(path)
	if err != nil {
		return err
	}
	if err = state.sqlite.Check(); err != nil {
		closeErr := state.sqlite.Close()
		state.sqlite = nil
		return errors.Join(err, closeErr)
	}
	state.backend = state.sqlite
	return nil
}

// DatabasePath returns the selected source, without creating a database. The
// selection is cached under the instance lock so initialization uses the same
// generation that was checked before any network side effects.
func DatabasePath() (string, error) {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	config := conf.GetEnvironmentConfig()
	if err := database.selectBackend(config.Config, config.DatabaseBackend); err != nil {
		return "", err
	}
	return database.sourcePath, nil
}

// Backend is available after OpenExisting or DatabasePath has selected a backend.
func Backend() string {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	return database.selection.Backend
}

func DefaultsRequested() bool {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	return database.selection.Initialize
}

func (state *databaseState) open(path string, existingOnly bool, timeout time.Duration) error {
	return state.openBolt(path, existingOnly, false, timeout)
}

func (state *databaseState) openBolt(path string, existingOnly, required bool, timeout time.Duration) error {
	if state.initialized {
		return state.initErr
	}
	if existingOnly && !required {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("inspect database: %w", err)
		}
	}
	state.initialized = true
	state.handle, state.temporaryPath, state.initErr = openDatabase(path, state.readOnly, !existingOnly && !required, timeout)
	if state.initErr == nil {
		state.backend = store.NewBolt(state.handle)
	}
	return state.initErr
}

// CurrentStore returns the configured transaction interface. Callers must not
// open a second global transaction from inside a transaction callback: reuse the
// callback's ReadTx/WriteTx so multi-object updates share one commit boundary.
// Startup retains ownership of the physical handle and closes it after users
// have stopped. This accessor propagates initialization errors without exiting.
func CurrentStore() (store.Store, error) {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	if err := initDatabaseLocked(false); err != nil {
		return nil, err
	}
	return database.currentStore()
}

func (state *databaseState) currentStore() (store.Store, error) {
	if state.initErr != nil {
		return nil, state.initErr
	}
	if state.backend == nil {
		if state.handle == nil {
			return nil, bbolt.ErrDatabaseNotOpen
		}
		state.backend = store.NewBolt(state.handle)
	}
	return state.backend, nil
}

// View runs a consistent read-only snapshot without creating buckets or taking
// the database's writer lock. Returned values belong to the caller.
func View(fn func(store.ReadTx) error) error {
	backend, err := CurrentStore()
	if err != nil {
		return err
	}
	return backend.View(fn)
}

// Update commits all callback changes together, or rolls them back on error or
// panic. Do not perform network, Docker or other external work in the callback.
func Update(fn func(store.WriteTx) error) error {
	backend, err := CurrentStore()
	if err != nil {
		return err
	}
	return backend.Update(fn)
}

// Close only closes an already opened database; it never creates one during
// cleanup. The caller must have stopped transaction users before invoking Close.
func Close() error {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	return database.close()
}

func (state *databaseState) close() error {
	var err error
	if state.sqlite != nil {
		err = state.sqlite.Close()
		state.sqlite = nil
		state.backend = nil
		state.initErr = bbolt.ErrDatabaseNotOpen
	}
	if state.handle != nil {
		err = errors.Join(err, state.handle.Close())
		state.handle = nil
		state.backend = nil
		state.initErr = bbolt.ErrDatabaseNotOpen
	}
	for _, handle := range state.retired {
		err = errors.Join(err, handle.Close())
	}
	state.retired = nil
	if state.temporaryPath != "" {
		removeErr := os.Remove(state.temporaryPath)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		err = errors.Join(err, removeErr)
		if removeErr == nil {
			state.temporaryPath = ""
		}
	}
	if state.temporaryDir != "" {
		removeErr := os.RemoveAll(state.temporaryDir)
		err = errors.Join(err, removeErr)
		if removeErr == nil {
			state.temporaryDir = ""
		}
	}
	return err
}

// OpenReadOnly obtains a bounded shared Bolt lock without creating a missing
// source. Symlinks and non-regular source files are rejected. Keep the returned
// handle open for as long as the source must be protected from old writers.
func OpenReadOnly(path string) (*bbolt.DB, error) {
	return openReadOnly(path, databaseOpenTimeout)
}

func openReadOnly(path string, timeout time.Duration) (*bbolt.DB, error) {
	var sourceInfo os.FileInfo
	return bbolt.Open(path, 0600, &bbolt.Options{
		ReadOnly: true,
		Timeout:  timeout,
		OpenFile: func(name string, flag int, mode os.FileMode) (*os.File, error) {
			// CopyFile uses this callback for its destination as well. The
			// caller chooses and secures that destination, as with bbolt itself.
			if name != path {
				return os.OpenFile(name, flag, mode)
			}
			if flag&(os.O_RDWR|os.O_WRONLY|os.O_TRUNC) != 0 {
				return nil, fmt.Errorf("refusing to overwrite read-only database %q", path)
			}
			file, err := openRegularDatabaseFile(path, os.O_RDONLY, 0, true)
			if err != nil {
				return nil, err
			}
			info, err := file.Stat()
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			// WriteTo reopens the source path. Never mix the transaction's
			// metadata with pages from a file substituted after opening it.
			if sourceInfo != nil && !os.SameFile(sourceInfo, info) {
				_ = file.Close()
				return nil, fmt.Errorf("database source changed: %q", path)
			}
			if sourceInfo == nil {
				sourceInfo = info
			}
			return file, nil
		},
	})
}

func openDatabase(path string, readOnly, create bool, timeout time.Duration) (handle *bbolt.DB, temporaryPath string, err error) {
	if readOnly {
		// Reports may invoke initialization or normalization paths. Keep those
		// operations on a consistent private snapshot, never on the source.
		path, err = snapshotDatabase(path, timeout)
		if err != nil {
			return nil, "", err
		}
		temporaryPath = path
		create = false
		defer func() {
			if err != nil {
				err = errors.Join(err, os.Remove(temporaryPath))
				temporaryPath = ""
			}
		}()
	}
	handle, err = bbolt.Open(path, 0600, &bbolt.Options{
		Timeout: timeout,
		OpenFile: func(name string, flag int, mode os.FileMode) (*os.File, error) {
			if !create && name == path {
				flag &^= os.O_CREATE
			}
			return openRegularDatabaseFile(name, flag, mode, false)
		},
	})
	if err != nil {
		return nil, temporaryPath, fmt.Errorf("open database %q: %w", path, err)
	}
	return handle, temporaryPath, nil
}

func snapshotDatabase(path string, timeout time.Duration) (snapshotPath string, err error) {
	source, err := openReadOnly(path, timeout)
	if err != nil {
		return "", fmt.Errorf("open report database: %w", err)
	}
	defer func() {
		err = errors.Join(err, source.Close())
		if err != nil && snapshotPath != "" {
			err = errors.Join(err, os.Remove(snapshotPath))
			snapshotPath = ""
		}
	}()
	file, err := os.CreateTemp("", "v2raya_tmp_bolt_*.db")
	if err != nil {
		return "", err
	}
	snapshotPath = file.Name()
	err = source.View(func(tx *bbolt.Tx) error {
		_, copyErr := tx.WriteTo(file)
		return copyErr
	})
	err = errors.Join(err, file.Close())
	return snapshotPath, err
}
