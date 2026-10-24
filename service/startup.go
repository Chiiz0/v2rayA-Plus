package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/v2rayA/v2rayA/common/instancelock"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/db"
	"go.etcd.io/bbolt"
)

// Keep ownership through runtime cleanup and database close, including startup
// failures. The lock file is persistent: removing it could admit two owners.
func withInstanceLock(configDir string, start func() error) (err error) {
	lock, err := instancelock.Acquire(configDir)
	if err != nil {
		return fmt.Errorf("configuration directory %q: %w", configDir, err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return start()
}

func start(config *conf.Params) error {
	if config.PrintReport != "" && !config.ReportNeedsDatabase() {
		config.Report()
		return nil
	}
	return withInstanceLock(config.Config, func() (err error) {
		if config.PrintReport != "" {
			db.SetReadOnly()
		}
		defer func() { err = errors.Join(err, db.Close()) }()
		legacy, err := openLegacyDatabase(config.Config)
		if err != nil {
			return err
		}
		if legacy != nil {
			defer func() { err = errors.Join(err, legacy.Close()) }()
		}
		// Older releases do not know the directory lock. Keep their Bolt lock
		// as well, before password reset, module probing or DNS recovery.
		if err = db.OpenExisting(); err != nil {
			if errors.Is(err, bbolt.ErrTimeout) {
				return fmt.Errorf("database is in use; stop other v2rayA instances, including older versions: %w", err)
			}
			return err
		}
		if config.PrintReport != "" {
			if err = db.Init(); err != nil {
				return err
			}
		}
		if done, err := checkEnvironment(); done || err != nil {
			return err
		}
		if err = initConfigure(legacy); err != nil {
			return err
		}
		if runtime.GOOS == "linux" {
			checkTProxySupportability()
		}
		checkUpdate()
		hello()
		return run()
	})
}

func openLegacyDatabase(configDir string) (*bbolt.DB, error) {
	path := filepath.Join(configDir, "boltv4.db")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect legacy database: %w", err)
	}
	// Hold the source lock for the entire instance lifetime. The lib4 singleton
	// would reopen this file without a timeout; migration uses this handle instead.
	database, err := db.OpenReadOnly(path)
	if err != nil {
		return nil, fmt.Errorf("open legacy database (stop any older v2rayA instance): %w", err)
	}
	return database, nil
}
