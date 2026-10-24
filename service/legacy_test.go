package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/backup"
	"go.etcd.io/bbolt"
)

func makeLegacyFixture(t *testing.T, dir, servers, subscriptions string) string {
	t.Helper()
	path := filepath.Join(dir, "boltv4.db")
	database, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	err = database.Update(func(tx *bbolt.Tx) error {
		for bucket, pairs := range map[string]map[string]string{
			"system":   {"running": "false", "future": `{"keep":1234567890123456789}`},
			"accounts": {"旧用户": `"opaque-password-hash"`},
			"touch":    {"servers_v2": servers, "subscriptions_v2": subscriptions},
		} {
			b, err := tx.CreateBucket([]byte(bucket))
			if err != nil {
				return err
			}
			for key, value := range pairs {
				if err = b.Put([]byte(key), []byte(value)); err != nil {
					return err
				}
			}
		}
		b, err := tx.CreateBucket([]byte("unknown"))
		if err != nil {
			return err
		}
		if err = b.SetSequence(9007199254740993); err != nil {
			return err
		}
		b, err = b.CreateBucket([]byte("nested"))
		if err != nil {
			return err
		}
		return b.Put([]byte{0, 255}, []byte{0, 254, 253})
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLegacySnapshotKeepsSourceAndAllValues(t *testing.T) {
	dir := t.TempDir()
	sourcePath := makeLegacyFixture(t, dir, `[{"serverObj":{"protocol":"socks5","server":"127.0.0.1","port":1080,"name":"旧节点"}}]`, `[{"address":"https://example.invalid/subscription","servers":[]}]`)
	before, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := openLegacyDatabase(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	configuration, err := readLegacyConfiguration(source)
	if err != nil || configuration == nil || len(configuration.servers) != 1 || len(configuration.subscriptions) != 1 {
		t.Fatalf("read legacy configuration: %#v, %v", configuration, err)
	}
	target := filepath.Join(dir, "bolt.db")
	if err := publishLegacySnapshot(source, target); err != nil {
		t.Fatal(err)
	}
	old, err := backup.Export(sourcePath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := backup.Export(target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if old.Manifest != restored.Manifest {
		t.Fatal("migration snapshot changed logical data or bucket sequences")
	}
	if err := publishLegacySnapshot(source, target); err == nil {
		t.Fatal("migration snapshot overwrote existing destination")
	}
	after, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy source changed: %v", err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot permissions: %v, %v", info, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temporary snapshot was not removed: %v", entries)
	}
}

func TestLegacyInvalidDataFailsBeforePublishing(t *testing.T) {
	for _, raw := range []string{`{}`, `[null]`, `[{"serverObj":null}]`, `[{"serverObj":{"protocol":"unknown"}}]`} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			path := makeLegacyFixture(t, dir, raw, `[]`)
			source, err := db.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if _, err := readLegacyConfiguration(source); err == nil {
				t.Fatal("invalid legacy data accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "bolt.db")); !os.IsNotExist(err) {
				t.Fatalf("invalid legacy source published a new database: %v", err)
			}
		})
	}
}

func TestMissingLegacyDoesNotCreateFile(t *testing.T) {
	dir := t.TempDir()
	source, err := openLegacyDatabase(dir)
	if err != nil || source != nil {
		t.Fatalf("missing legacy source: %v, %v", source, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("created files while checking missing legacy source: %v, %v", entries, err)
	}
}

func TestLegacyDuplicateKeysCannotInvokeOldEnvironmentParser(t *testing.T) {
	for _, raw := range []string{
		`[{"serverObj":{"protocol":"unknown","protocol":"socks5","server":"127.0.0.1","port":1080}}]`,
		`[{"serverObj":{"protocol":"unknown"},"serverObj":{"protocol":"socks5","server":"127.0.0.1","port":1080}}]`,
	} {
		dir := t.TempDir()
		path := makeLegacyFixture(t, dir, raw, `[]`)
		source, err := db.OpenReadOnly(path)
		if err != nil {
			t.Fatal(err)
		}
		configuration, err := readLegacyConfiguration(source)
		source.Close()
		if err != nil || configuration == nil || len(configuration.servers) != 1 || configuration.servers[0] != "socks5://127.0.0.1:1080" {
			t.Fatalf("legacy decoder used an unvalidated duplicate field: %#v, %v", configuration, err)
		}
	}
}
