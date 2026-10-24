package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

const databaseOpenTimeout = 2 * time.Second

var databaseMu sync.Mutex
var database databaseState

type databaseState struct {
	handle        *bbolt.DB
	backend       store.Store
	initialized   bool
	readOnly      bool
	temporaryPath string
	initErr       error
}

// SetReadOnly selects an isolated writable snapshot for subsequent DB accesses.
// Call it before Init or OpenExisting, while holding the instance lock.
func SetReadOnly() {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	database.readOnly = true
}

// OpenExisting retains the existing database's Bolt lock before startup changes
// network or configuration state. An absent file is neither created nor marked
// initialized, so legacy database migration can still run before Init.
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
	path := filepath.Join(conf.GetEnvironmentConfig().Config, "bolt.db")
	return database.open(path, existingOnly, databaseOpenTimeout)
}

func (state *databaseState) open(path string, existingOnly bool, timeout time.Duration) error {
	if state.initialized {
		return state.initErr
	}
	if existingOnly {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("inspect database: %w", err)
		}
	}
	state.initialized = true
	state.handle, state.temporaryPath, state.initErr = openDatabase(path, state.readOnly, !existingOnly, timeout)
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
	if state.handle != nil {
		err = state.handle.Close()
		state.handle = nil
		state.backend = nil
		state.initErr = bbolt.ErrDatabaseNotOpen
	}
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
