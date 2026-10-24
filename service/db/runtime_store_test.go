package db

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

// These tests replace the private TestMain state only while no transactions are
// active. They never resolve or open a configured application directory.
func useRuntimeState(t *testing.T, state databaseState) {
	t.Helper()
	databaseMu.Lock()
	previous := database
	database = state
	databaseMu.Unlock()
	t.Cleanup(func() {
		databaseMu.Lock()
		database = previous
		databaseMu.Unlock()
	})
}

type failingRuntimeStore struct {
	err            error
	views, updates int
}

func (s *failingRuntimeStore) View(func(store.ReadTx) error) error {
	s.views++
	return s.err
}

func (s *failingRuntimeStore) Update(func(store.WriteTx) error) error {
	s.updates++
	return s.err
}

func TestRuntimeDispatchesThroughConfiguredStore(t *testing.T) {
	failure := errors.New("injected backend failure")
	backend := &failingRuntimeStore{err: failure}
	useRuntimeState(t, databaseState{initialized: true, backend: backend})
	actual, err := CurrentStore()
	if err != nil || actual != backend {
		t.Fatalf("configured backend was bypassed: %v", err)
	}
	if err := View(func(store.ReadTx) error { t.Fatal("unexpected callback"); return nil }); !errors.Is(err, failure) {
		t.Fatalf("View lost the backend error: %v", err)
	}
	if err := Update(func(store.WriteTx) error { t.Fatal("unexpected callback"); return nil }); !errors.Is(err, failure) {
		t.Fatalf("Update lost the backend error: %v", err)
	}
	if backend.views != 1 || backend.updates != 1 {
		t.Fatalf("backend calls = %d/%d", backend.views, backend.updates)
	}
}

func TestRuntimeInitializationFailureReturnsWithoutCallbacks(t *testing.T) {
	failure := errors.New("injected initialization failure")
	for _, tc := range []struct {
		name  string
		state databaseState
		want  error
	}{
		{"failed initialization", databaseState{initialized: true, initErr: failure}, failure},
		{"missing handle", databaseState{initialized: true}, bbolt.ErrDatabaseNotOpen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useRuntimeState(t, tc.state)
			if backend, err := CurrentStore(); backend != nil || !errors.Is(err, tc.want) {
				t.Fatalf("CurrentStore swallowed initialization failure: %v", err)
			}
			if err := View(func(store.ReadTx) error { t.Fatal("callback after failure"); return nil }); !errors.Is(err, tc.want) {
				t.Fatalf("View swallowed initialization failure: %v", err)
			}
			if err := Update(func(store.WriteTx) error { t.Fatal("callback after failure"); return nil }); !errors.Is(err, tc.want) {
				t.Fatalf("Update swallowed initialization failure: %v", err)
			}
		})
	}
}

func TestRuntimeStoreLifetimeFollowsPhysicalHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bolt.db")
	state := databaseState{}
	t.Cleanup(func() { _ = state.close() })
	if err := state.open(path, false, testDatabaseTimeout); err != nil {
		t.Fatal(err)
	}
	backend, err := state.currentStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Update(func(tx store.WriteTx) error {
		return tx.Put([]byte("test"), []byte("value"), []byte("committed"))
	}); err != nil {
		t.Fatal(err)
	}
	if got := string(readTestBolt(t, state.handle)); got != "committed" {
		t.Fatal("adapter did not share the startup-owned handle")
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	if state.backend != nil {
		t.Fatal("closed lifecycle retained a backend")
	}
	if _, err := state.currentStore(); !errors.Is(err, bbolt.ErrDatabaseNotOpen) {
		t.Fatalf("closed lifecycle exposed a new backend: %v", err)
	}
	if err := backend.View(func(store.ReadTx) error { t.Fatal("closed View callback"); return nil }); !errors.Is(err, bbolt.ErrDatabaseNotOpen) {
		t.Fatalf("stale adapter reopened the database: %v", err)
	}
	if err := backend.Update(func(store.WriteTx) error { t.Fatal("closed Update callback"); return nil }); !errors.Is(err, bbolt.ErrDatabaseNotOpen) {
		t.Fatalf("stale adapter started a write: %v", err)
	}
}
