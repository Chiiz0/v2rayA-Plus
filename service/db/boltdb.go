package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/pkg/util/log"
	"go.etcd.io/bbolt"
)

const databaseOpenTimeout = 2 * time.Second

var databaseMu sync.Mutex
var database databaseState

type databaseState struct {
	handle        *bbolt.DB
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
	return state.initErr
}

func DB() *bbolt.DB {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	if err := initDatabaseLocked(false); err != nil {
		log.Fatal("bbolt.Open: %v", err)
	}
	return database.handle
}

// Close only closes an already opened database; it never creates one during
// cleanup. The caller must have stopped users of DB before invoking Close.
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
		// Existing getters may create buckets. Run them against a consistent
		// private snapshot, never against the original reporting database.
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

// The function should return a dirty flag.
// If the dirty flag is true and there is no error then the transaction is commited.
// Otherwise, the transaction is rolled back.
func Transaction(db *bbolt.DB, fn func(*bbolt.Tx) (bool, error)) error {
	tx, err := db.Begin(true)
	if err != nil {
		return err
	}
	dirty, err := fn(tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if !dirty {
		return tx.Rollback()
	}
	return tx.Commit()
}

// If the bucket does not exist, the dirty flag is setted
func CreateBucketIfNotExists(tx *bbolt.Tx, name []byte, dirty *bool) (*bbolt.Bucket, error) {
	bkt := tx.Bucket(name)
	if bkt != nil {
		return bkt, nil
	}
	bkt, err := tx.CreateBucket(name)
	if err != nil {
		return nil, err
	}
	*dirty = true
	return bkt, nil
}
