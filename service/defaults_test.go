package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/runtimebackend"
	"github.com/v2rayA/v2rayA/db/sqlitestore"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

// Failure on the final sentinel deletion exercises rollback of all defaults,
// rather than merely rejecting the operation before any writes have occurred.
type defaultsFailingStore struct{ store.Store }
type defaultsFailingTx struct{ store.WriteTx }

var errDefaultsInjected = errors.New("defaults final write failed")

func (s defaultsFailingStore) Update(fn func(store.WriteTx) error) error {
	return s.Store.Update(func(tx store.WriteTx) error { return fn(defaultsFailingTx{tx}) })
}
func (tx defaultsFailingTx) Delete(bucket, key []byte) error { return errDefaultsInjected }

func defaultsFixture(t *testing.T, backend string) (store.Store, func() *backup.Archive) {
	t.Helper()
	dir := t.TempDir()
	archive, err := backup.NewSnapshot("bbolt", []backup.Bucket{
		{Name: []byte(runtimebackend.DefaultsBucket), Values: []backup.Value{{Key: []byte(runtimebackend.DefaultsKey), Data: []byte(runtimebackend.DefaultsValue)}}},
		{Name: []byte("accounts"), Values: []backup.Value{{Key: []byte("preserved"), Data: []byte(`"password-hash"`)}}},
		{Name: []byte("system"), Values: []backup.Value{{Key: []byte("routingA"), Data: []byte(`"custom routing"`)}}},
		{Name: []byte("unknown"), Sequence: ^uint64(0), Buckets: []backup.Bucket{{Name: []byte("nested"), Sequence: 1 << 63}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend == "sqlite" {
		path := filepath.Join(dir, "runtime.sqlite")
		if err := sqlitestore.Create(path, archive); err != nil {
			t.Fatal(err)
		}
		sqlite, err := sqlitestore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlite.Close() })
		return sqlite, func() *backup.Archive {
			a, err := sqlite.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			return a
		}
	}
	path := filepath.Join(dir, "bolt.db")
	if err := backup.Restore(path, archive); err != nil {
		t.Fatal(err)
	}
	bolt, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bolt.Close() })
	return store.NewBolt(bolt), func() *backup.Archive {
		// Export uses a shared lock, so export a consistent private Bolt copy.
		target := filepath.Join(t.TempDir(), "snapshot.db")
		if err := bolt.View(func(tx *bbolt.Tx) error { return tx.CopyFile(target, 0600) }); err != nil {
			t.Fatal(err)
		}
		a, err := backup.Export(target, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
}

func TestRuntimeDefaultsAreAtomicPreservingAndOneTime(t *testing.T) {
	for _, kind := range []string{"bolt", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			backend, snapshot := defaultsFixture(t, kind)
			before := snapshot()
			if err := initializeRequestedDefaults(defaultsFailingStore{backend}); !errors.Is(err, errDefaultsInjected) {
				t.Fatalf("failure not propagated: %v", err)
			}
			if got := snapshot(); got.Manifest != before.Manifest {
				t.Fatal("default failure committed partial data")
			}
			if err := initializeRequestedDefaults(backend); err != nil {
				t.Fatal(err)
			}
			if err := backend.View(func(tx store.ReadTx) error {
				for _, entry := range []struct {
					bucket, key string
					want        []byte
				}{
					{runtimebackend.DefaultsBucket, runtimebackend.DefaultsKey, nil},
					{"accounts", "preserved", []byte(`"password-hash"`)},
					{"system", "routingA", []byte(`"custom routing"`)},
					{"touch", "servers", []byte(`[]`)},
					{"touch", "subscriptions", []byte(`[]`)},
				} {
					got, err := tx.Get([]byte(entry.bucket), []byte(entry.key))
					if err != nil {
						return err
					}
					if !bytes.Equal(got, entry.want) {
						t.Errorf("unexpected default or lost existing value: %s/%s", entry.bucket, entry.key)
					}
				}
				ports, err := tx.Get([]byte("system"), []byte("ports"))
				if err != nil {
					return err
				}
				if gjson.GetBytes(ports, "socks5").Int() != 20170 || gjson.GetBytes(ports, "http").Int() != 20171 {
					t.Error("fresh listener defaults incomplete")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			after := snapshot()
			if err := initializeRequestedDefaults(backend); err != nil {
				t.Fatal(err)
			}
			if snapshot().Manifest != after.Manifest {
				t.Fatal("repeated default initialization changed database")
			}
			var nested backup.Bucket
			for _, b := range after.Buckets {
				if string(b.Name) == "unknown" {
					nested = b
				}
			}
			if nested.Sequence != ^uint64(0) || len(nested.Buckets) != 1 || nested.Buckets[0].Sequence != 1<<63 {
				t.Fatal("defaults lost unknown bucket tree")
			}
		})
	}
}

func TestRuntimeDefaultsRejectInvalidSentinelWithoutChanges(t *testing.T) {
	backend, snapshot := defaultsFixture(t, "sqlite")
	if err := backend.Update(func(tx store.WriteTx) error {
		return tx.Put([]byte(runtimebackend.DefaultsBucket), []byte(runtimebackend.DefaultsKey), []byte("invalid"))
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshot()
	if err := initializeRequestedDefaults(backend); err == nil {
		t.Fatal("invalid marker accepted")
	}
	if snapshot().Manifest != before.Manifest {
		t.Fatal("invalid marker changed data")
	}
}
