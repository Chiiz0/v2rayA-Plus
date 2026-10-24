package runtimebackend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/v2rayA/v2rayA/common/instancelock"
	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/sqlitestore"
	"go.etcd.io/bbolt"
)

const lockTimeout = 2 * time.Second

var ErrAudit = errors.New("database has application-data audit errors")

type Result struct {
	Backend       string          `json:"backend"`
	Database      string          `json:"database"`
	Manifest      backup.Manifest `json:"manifest"`
	AlreadyActive bool            `json:"alreadyActive"`
	Archive       string          `json:"archive,omitempty"`
}

// Migrate selects SQLite only after preserving the full source archive and
// validating a separately published runtime database. Repeated calls on an
// active SQLite installation verify current data instead of restoring old data.
func Migrate(configDir string) (Result, error) {
	return transition(configDir, "sqlite", nil)
}

// Rollback converts the latest SQLite snapshot, including committed WAL data,
// to a new Bolt file. The original pre-migration Bolt is never reused or edited.
func Rollback(configDir string) (Result, error) {
	return transition(configDir, "bolt", nil)
}

// hook is supplied only by tests to simulate process failure at durable stages.
func transition(configDir, target string, hook func(string) error) (result Result, err error) {
	return locked(configDir, func(dir string) (Result, error) {
		selection, err := ReadSelection(dir)
		if err != nil {
			return Result{}, err
		}
		archive, err := snapshot(dir, selection)
		if err != nil {
			return Result{}, err
		}
		if backup.Audit(archive).HasErrors() {
			return Result{}, fmt.Errorf("export and inspect the database before migration: %w", ErrAudit)
		}
		if selection.Backend == target {
			if err := syncDirectory(dir); err != nil {
				return Result{}, err
			}
			return Result{Backend: target, Database: filepath.Join(dir, selection.Database), Manifest: archive.Manifest, AlreadyActive: true}, nil
		}
		return install(dir, archive, target, selection.Initialize, hook)
	})
}

// InitSQLite creates a fresh optional SQLite installation. A sentinel requests
// atomic, application-owned defaults on its first service startup.
func InitSQLite(configDir string) (Result, error) {
	archive, err := initialArchive()
	if err != nil {
		return Result{}, err
	}
	return initialize(configDir, archive, true)
}

// Restore installs a complete archive into a fresh SQLite configuration
// directory. It does not overwrite an existing application's data or restore
// external Docker volumes, DNS state or deployment files.
func Restore(configDir, archivePath string) (Result, error) {
	archive, err := backup.Read(archivePath)
	if err != nil {
		return Result{}, err
	}
	fresh, err := initialArchive()
	if err != nil {
		return Result{}, err
	}
	// Preserve the meaning of a backup made between runtime-init and the
	// first service startup. Only the exact empty-installation snapshot is
	// recognized; an unknown sentinel among application data is left alone.
	return initialize(configDir, archive, archive.Manifest == fresh.Manifest)
}

func initialArchive() (*backup.Archive, error) {
	return backup.NewSnapshot("sqlite", []backup.Bucket{{
		Name: []byte(DefaultsBucket), Values: []backup.Value{{Key: []byte(DefaultsKey), Data: []byte(DefaultsValue)}},
	}})
}

func initialize(configDir string, archive *backup.Archive, defaults bool) (Result, error) {
	if backup.Audit(archive).HasErrors() {
		return Result{}, ErrAudit
	}
	return locked(configDir, func(dir string) (Result, error) {
		selection, err := ReadSelection(dir)
		if err != nil {
			return Result{}, err
		}
		if selection.Present {
			actual, err := snapshot(dir, selection)
			if err != nil {
				return Result{}, err
			}
			if selection.Backend != "sqlite" || selection.Initialize != defaults || actual.Manifest != archive.Manifest {
				return Result{}, errors.New("configuration directory already has a selected database; use a fresh directory")
			}
			if err := syncDirectory(dir); err != nil {
				return Result{}, err
			}
			return Result{Backend: "sqlite", Database: filepath.Join(dir, selection.Database), Manifest: actual.Manifest, AlreadyActive: true}, nil
		}
		if _, err := os.Lstat(filepath.Join(dir, "boltv4.db")); !errors.Is(err, os.ErrNotExist) {
			return Result{}, errors.New("configuration directory contains a legacy database or cannot be inspected")
		}
		// A completely empty Bolt file may be a compatibility guard published
		// by an interrupted attempt. Never accept populated application data.
		boltPath := filepath.Join(dir, "bolt.db")
		if _, err := os.Lstat(boltPath); err == nil {
			existing, err := backup.Export(boltPath, lockTimeout)
			if err != nil {
				return Result{}, err
			}
			if existing.Manifest.Buckets != 0 {
				return Result{}, errors.New("configuration directory contains Bolt data; use runtime-migrate")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Result{}, err
		}
		return install(dir, archive, "sqlite", defaults, nil)
	})
}

// Export takes a consistent logical snapshot while the service is stopped.
// SQLite's read transaction includes WAL writes; copying the main file alone
// is never used as a backup. The returned archive can restore either backend.
func Export(configDir string) (archive *backup.Archive, err error) {
	_, err = locked(configDir, func(dir string) (Result, error) {
		selection, err := ReadSelection(dir)
		if err != nil {
			return Result{}, err
		}
		archive, err = snapshot(dir, selection)
		return Result{}, err
	})
	return archive, err
}

func Verify(configDir string) (Result, error) {
	return locked(configDir, func(dir string) (Result, error) {
		selection, err := ReadSelection(dir)
		if err != nil {
			return Result{}, err
		}
		if selection.Database != "bolt.db" {
			guard, err := openRegular(filepath.Join(dir, "bolt.db"))
			if err != nil {
				return Result{}, fmt.Errorf("retired Bolt compatibility lock is unavailable; export to a fresh configuration directory before starting: %w", err)
			}
			if err := guard.Close(); err != nil {
				return Result{}, err
			}
		}
		archive, err := snapshot(dir, selection)
		if err != nil {
			return Result{}, err
		}
		if backup.Audit(archive).HasErrors() {
			return Result{}, ErrAudit
		}
		return Result{Backend: selection.Backend, Database: filepath.Join(dir, selection.Database), Manifest: archive.Manifest}, nil
	})
}

func snapshot(dir string, selection Selection) (*backup.Archive, error) {
	path := filepath.Join(dir, selection.Database)
	if selection.Backend == "sqlite" {
		return sqlitestore.SnapshotReadOnly(path)
	}
	return backup.Export(path, lockTimeout)
}

func install(dir string, archive *backup.Archive, backend string, initialize bool, hook func(string) error) (Result, error) {
	checkpoint := func(stage string) error {
		if hook != nil {
			return hook(stage)
		}
		return nil
	}
	// Content-addressed names make interruption retries deterministic. A name
	// collision is always checked against the entire logical snapshot.
	digest := archive.Manifest.LogicalSHA256
	archivePath := filepath.Join(dir, "migration-"+digest+".json")
	if _, err := os.Lstat(archivePath); err == nil {
		existing, err := backup.Read(archivePath)
		if err != nil {
			return Result{}, err
		}
		if existing.Manifest != archive.Manifest {
			return Result{}, errors.New("existing migration archive differs from the current source")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := backup.Write(archivePath, archive); err != nil {
			return Result{}, err
		}
	} else {
		return Result{}, err
	}
	if err := checkpoint("archived"); err != nil {
		return Result{}, err
	}

	name, err := prepareTarget(dir, archive, backend)
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(dir, name)
	if err := checkpoint("published"); err != nil {
		return Result{}, err
	}
	selection := Selection{Version: 1, Backend: backend, Database: name, Initialize: initialize}
	actual, err := snapshot(dir, selection)
	if err != nil {
		return Result{}, err
	}
	if actual.Manifest != archive.Manifest {
		return Result{}, errors.New("target database differs from the current source; refusing activation")
	}
	if err := syncDirectory(dir); err != nil {
		return Result{}, err
	}
	if err := checkpoint("verified"); err != nil {
		return Result{}, err
	}

	// Old releases ignore the directory lock. Keep a stable Bolt file that
	// current services can shared-lock to exclude those old writers, even for
	// installations created directly as SQLite.
	guardPath := filepath.Join(dir, "bolt.db")
	if _, err := os.Lstat(guardPath); errors.Is(err, os.ErrNotExist) {
		empty, err := backup.NewSnapshot("bbolt", nil)
		if err != nil {
			return Result{}, err
		}
		if err := backup.Restore(guardPath, empty); err != nil {
			return Result{}, err
		}
	} else if err != nil {
		return Result{}, err
	}
	guard, err := lockBolt(guardPath)
	if err != nil {
		return Result{}, err
	}
	defer guard.Close()
	if err := publishSelection(dir, selection); err != nil {
		return Result{}, err
	}
	if err := checkpoint("selected"); err != nil {
		return Result{}, err
	}
	return Result{Backend: backend, Database: path, Manifest: archive.Manifest, Archive: archivePath}, nil
}

func prepareTarget(dir string, archive *backup.Archive, backend string) (string, error) {
	// An earlier target can itself have been a writable runtime. If its data
	// subsequently changed, retain it and publish a new generation. This makes
	// repeated Bolt -> SQLite -> Bolt transitions work even when values return
	// to a previous logical snapshot. Corrupt files are never silently skipped.
	for generation := 0; generation < 1024; generation++ {
		name := backend + "-" + archive.Manifest.LogicalSHA256
		if generation != 0 {
			name += fmt.Sprintf("-%016x", generation)
		}
		name += ".db"
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			actual, err := snapshot(dir, Selection{Backend: backend, Database: name})
			if err != nil {
				return "", err
			}
			if actual.Manifest == archive.Manifest {
				return name, nil
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		var err error
		if backend == "sqlite" {
			err = sqlitestore.Create(path, archive)
		} else {
			err = backup.Restore(path, archive)
		}
		return name, err
	}
	return "", errors.New("too many retained database generations; archive the configuration directory before further migrations")
}

// All public management operations refuse a running service. Also retain
// shared locks on both historical filenames and a selected Bolt rollback file,
// so old binaries cannot change a source between verification and selection.
func locked(configDir string, fn func(string) (Result, error)) (result Result, err error) {
	dir, err := filepath.Abs(configDir)
	if err != nil {
		return Result{}, err
	}
	lock, err := instancelock.Acquire(dir)
	if err != nil {
		return Result{}, fmt.Errorf("stop the service before database maintenance: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	selection, err := ReadSelection(dir)
	if err != nil {
		return Result{}, err
	}
	names := []string{"bolt.db", "boltv4.db"}
	if selection.Backend == "bolt" && selection.Database != "bolt.db" {
		names = append(names, selection.Database)
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return Result{}, err
		}
		guard, err := lockBolt(path)
		if err != nil {
			return Result{}, fmt.Errorf("stop older services before database maintenance: %w", err)
		}
		defer func() { err = errors.Join(err, guard.Close()) }()
	}
	return fn(dir)
}

func lockBolt(path string) (*bbolt.DB, error) {
	return bbolt.Open(path, 0600, &bbolt.Options{ReadOnly: true, Timeout: lockTimeout,
		OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return openRegular(name) },
	})
}
