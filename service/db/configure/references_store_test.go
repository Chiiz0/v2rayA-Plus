package configure

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/v2rayA/v2rayA/db/store"
)

// The wrapper exposes only ReadTx during View and rejects nested transactions.
// Faults can occur after successful writes to exercise the Store rollback contract.
type referenceCheckedStore struct {
	store.Store
	active         bool
	views, updates int
	writes         int
	fault          func(operation, bucket, key string) error
}

func (s *referenceCheckedStore) View(fn func(store.ReadTx) error) error {
	if s.active {
		return errors.New("nested reference transaction")
	}
	s.active = true
	defer func() { s.active = false }()
	s.views++
	return s.Store.View(func(tx store.ReadTx) error {
		return fn(referenceCheckedReadTx{ReadTx: tx, parent: s})
	})
}

func (s *referenceCheckedStore) Update(fn func(store.WriteTx) error) error {
	if s.active {
		return errors.New("nested reference transaction")
	}
	s.active = true
	defer func() { s.active = false }()
	s.updates++
	return s.Store.Update(func(tx store.WriteTx) error {
		return fn(referenceCheckedWriteTx{referenceCheckedReadTx{ReadTx: tx, parent: s}, tx})
	})
}

type referenceCheckedReadTx struct {
	store.ReadTx
	parent *referenceCheckedStore
}

func (tx referenceCheckedReadTx) check(operation string, bucket, key []byte) error {
	if tx.parent.fault != nil {
		return tx.parent.fault(operation, string(bucket), string(key))
	}
	return nil
}

func (tx referenceCheckedReadTx) Get(bucket, key []byte) ([]byte, error) {
	if err := tx.check("get", bucket, key); err != nil {
		return nil, err
	}
	return tx.ReadTx.Get(bucket, key)
}

func (tx referenceCheckedReadTx) ForEach(bucket []byte, fn func([]byte, []byte) error) error {
	if err := tx.check("foreach", bucket, nil); err != nil {
		return err
	}
	return tx.ReadTx.ForEach(bucket, fn)
}

func (tx referenceCheckedReadTx) ForEachBucket(fn func([]byte) error) error {
	if err := tx.check("buckets", nil, nil); err != nil {
		return err
	}
	return tx.ReadTx.ForEachBucket(fn)
}

type referenceCheckedWriteTx struct {
	referenceCheckedReadTx
	writer store.WriteTx
}

func (tx referenceCheckedWriteTx) Put(bucket, key, value []byte) error {
	if err := tx.check("put", bucket, key); err != nil {
		return err
	}
	err := tx.writer.Put(bucket, key, value)
	if err == nil {
		tx.parent.writes++
	}
	return err
}

func (tx referenceCheckedWriteTx) Delete(bucket, key []byte) error {
	if err := tx.check("delete", bucket, key); err != nil {
		return err
	}
	return tx.writer.Delete(bucket, key)
}

func (tx referenceCheckedWriteTx) DeleteBucket(bucket []byte) error {
	if err := tx.check("deletebucket", bucket, nil); err != nil {
		return err
	}
	return tx.writer.DeleteBucket(bucket)
}

func TestReferenceStoreFaultsRollBackAllReferences(t *testing.T) {
	for _, action := range []string{"delete", "refresh"} {
		for _, fault := range []struct {
			operation, bucket, key string
			afterWrites            bool
		}{
			{"get", "touch", "servers", false},
			{"get", "touch", "subscriptions", false},
			{"foreach", "dockerProxies", "", false},
			{"put", "dockerProxies", "1080", false},
			{"get", "outbounds", "names", true},
			{"buckets", "", "", true},
			{"get", "outbound.proxy", "connectedServers", true},
			{"put", "outbound.proxy", "connectedServers", true},
			{"put", "touch", "", true},
		} {
			t.Run(action+"/"+fault.operation+"/"+fault.bucket+"/"+fault.key, func(t *testing.T) {
				database := referenceTestDB(t)
				nodes := []json.RawMessage{referenceTestNode(t, "first"), referenceTestNode(t, "second")}
				referenceTestPut(t, database, "touch", "servers", marshalRaw(t, nodes))
				referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, nodes...)}))
				which := Which{TYPE: ServerType, ID: 2}
				if action == "refresh" {
					which.TYPE = SubscriptionServerType
				}
				referenceTestProxy(t, database, 1080, which, &which)
				referenceTestConnections(t, database, "proxy", Which{TYPE: which.TYPE, ID: 1}, which)
				backend := store.NewBolt(database)
				snapshot, err := readSubscriptionSnapshot(backend, 0)
				if err != nil {
					t.Fatal(err)
				}
				before := referenceTestContents(t, database)
				injected := errors.New("injected transaction failure")
				checked := &referenceCheckedStore{Store: backend, fault: func(operation, bucket, key string) error {
					if operation == fault.operation && bucket == fault.bucket && (fault.key == "" || key == fault.key) {
						return injected
					}
					return nil
				}}
				var removed bool
				if action == "delete" {
					removed, err = deleteTouchesAtomic(checked, []*Which{{TYPE: ServerType, ID: 1}})
				} else {
					second, decodeErr := Bytes2ServerRaw(nodes[1])
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					removed, err = commitSubscriptionRefresh(checked, 0, snapshot, []ServerRaw{*second}, "updated", "", true)
				}
				if removed || !errors.Is(err, injected) {
					t.Fatalf("operation = %v, %v; want original store error", removed, err)
				}
				if checked.views != 0 || checked.updates != 1 {
					t.Fatalf("operation used %d views and %d updates", checked.views, checked.updates)
				}
				if fault.afterWrites && checked.writes == 0 {
					t.Fatal("fault did not exercise rollback after writing references")
				}
				if !reflect.DeepEqual(before, referenceTestContents(t, database)) {
					t.Fatal("store failure partially updated nodes or references")
				}
			})
		}
	}
}

func TestReferenceSnapshotsUseOnlyOneReadTransaction(t *testing.T) {
	database := referenceTestDB(t)
	node := referenceTestNode(t, "node")
	referenceTestPut(t, database, "touch", "servers", marshalRaw(t, []json.RawMessage{node}))
	referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, node)}))
	referenceTestConnections(t, database, "proxy", Which{TYPE: ServerType, ID: 1})
	referenceTestProxy(t, database, 1080, Which{TYPE: ServerType, ID: 1}, nil)
	before := referenceTestContents(t, database)
	for _, read := range []struct {
		name string
		fn   func(store.Store) error
	}{
		{"subscription", func(s store.Store) error { _, err := readSubscriptionSnapshot(s, 0); return err }},
		{"subscriptions", func(s store.Store) error { _, err := readSubscriptionSnapshots(s); return err }},
		{"connections", func(s store.Store) error { _, err := readConnectedServerSnapshot(s); return err }},
		{"docker", func(s store.Store) error { _, err := getDockerProxies(s); return err }},
	} {
		t.Run(read.name, func(t *testing.T) {
			for _, fail := range []bool{false, true} {
				injected := errors.New("injected read failure")
				checked := &referenceCheckedStore{Store: store.NewBolt(database)}
				if fail {
					checked.fault = func(string, string, string) error { return injected }
				}
				err := read.fn(checked)
				if !fail && err != nil || fail && !errors.Is(err, injected) {
					t.Fatalf("read failure=%v: %v", fail, err)
				}
				if checked.views != 1 || checked.updates != 0 {
					t.Fatalf("snapshot used %d views and %d updates", checked.views, checked.updates)
				}
			}
		})
	}
	if !reflect.DeepEqual(before, referenceTestContents(t, database)) {
		t.Fatal("snapshot changed stored data")
	}
}
