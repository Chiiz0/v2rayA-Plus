package service

import (
	"fmt"
	"os"
	"testing"

	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/runtimebackend"
	"github.com/v2rayA/v2rayA/db/store"
)

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "v2raya-service-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Initialize the configuration and singleton DB only in this private
	// directory. These tests never read a running installation's database.
	_ = os.Setenv("V2RAYA_CONFIG", directory)
	conf.GetEnvironmentConfig().Config = directory
	if os.Getenv("V2RAYA_TEST_DATABASE_BACKEND") == "sqlite" {
		if _, err := runtimebackend.InitSQLite(directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			_ = os.RemoveAll(directory)
			os.Exit(1)
		}
		conf.GetEnvironmentConfig().DatabaseBackend = "sqlite"
	}
	if err := db.Init(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(directory)
		os.Exit(1)
	}
	code := m.Run()
	_ = db.Close()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

func resetNodeTestDB(t *testing.T) {
	t.Helper()
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	if err := db.Update(func(tx store.WriteTx) error {
		var names [][]byte
		if err := tx.ForEachBucket(func(name []byte) error {
			names = append(names, append([]byte(nil), name...))
			return nil
		}); err != nil {
			return err
		}
		for _, name := range names {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
