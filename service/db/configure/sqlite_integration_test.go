package configure

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/sqlitestore"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

func sqliteReferenceFixture(t *testing.T) (*sqlitestore.Store, []json.RawMessage) {
	t.Helper()
	bolt := referenceTestDB(t)
	nodes := []json.RawMessage{referenceTestNode(t, "a"), referenceTestNode(t, "b"), referenceTestNode(t, "c")}
	var err error
	nodes[0], err = sjson.SetRawBytes(nodes[0], "futureWrapper", []byte(`9007199254740995`))
	if err != nil {
		t.Fatal(err)
	}
	referenceTestPut(t, bolt, "touch", "servers", marshalRaw(t, nodes))
	referenceTestPut(t, bolt, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, nodes...)}))
	referenceTestProxy(t, bolt, 1080, Which{TYPE: ServerType, ID: 3}, &Which{TYPE: ServerType, ID: 2})
	referenceTestProxy(t, bolt, 1081, Which{TYPE: SubscriptionServerType, Sub: 0, ID: 1}, &Which{TYPE: SubscriptionServerType, Sub: 0, ID: 3})
	referenceTestNames(t, bolt, "中", "x")
	for _, name := range []string{"proxy", "中", "x", "orphan"} {
		referenceTestConnections(t, bolt, name, Which{TYPE: ServerType, ID: 2, Outbound: name}, Which{TYPE: SubscriptionServerType, Sub: 0, ID: 2, Outbound: name})
	}
	if err := bolt.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte("unknown"))
		if err != nil {
			return err
		}
		if err := bucket.SetSequence(^uint64(0)); err != nil {
			return err
		}
		child, err := bucket.CreateBucket([]byte{0xff, 0})
		if err != nil {
			return err
		}
		return child.Put([]byte("binary"), []byte{0, 0xff, 1})
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "snapshot.db")
	if err := bolt.View(func(tx *bbolt.Tx) error { return tx.CopyFile(copyPath, 0600) }); err != nil {
		t.Fatal(err)
	}
	archive, err := backup.Export(copyPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime.sqlite")
	if err := sqlitestore.Create(path, archive); err != nil {
		t.Fatal(err)
	}
	sqlite, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })
	return sqlite, nodes
}

func sqliteReferenceSnapshot(t *testing.T, s *sqlitestore.Store) *backup.Archive {
	t.Helper()
	a, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func sqliteReferenceGet(t *testing.T, s store.Store, bucket, key string) []byte {
	t.Helper()
	var value []byte
	if err := s.View(func(tx store.ReadTx) error {
		var err error
		value, err = tx.Get([]byte(bucket), []byte(key))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSQLiteReferenceChangesPreserveAllTargetsAndUnknownData(t *testing.T) {
	sqlite, nodes := sqliteReferenceFixture(t)
	if _, err := deleteTouchesAtomic(sqlite, []*Which{{TYPE: ServerType, ID: 1}}); err != nil {
		t.Fatal(err)
	}
	proxy := sqliteReferenceGet(t, sqlite, "dockerProxies", "1080")
	if gjson.GetBytes(proxy, "which.id").Int() != 2 || gjson.GetBytes(proxy, "frontWhich.id").Int() != 1 {
		t.Fatal("SQLite deletion did not remap main and front Docker nodes")
	}
	for _, name := range []string{"proxy", "中", "x", "orphan"} {
		raw := sqliteReferenceGet(t, sqlite, "outbound."+name, "connectedServers")
		if gjson.GetBytes(raw, "touches.0.id").Int() != 1 {
			t.Fatalf("SQLite deletion failed to remap outbound %s", name)
		}
	}
	expected, err := readSubscriptionSnapshot(sqlite, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fresh []ServerRaw
	for _, raw := range []json.RawMessage{nodes[2], nodes[0], nodes[1]} {
		node, err := Bytes2ServerRaw(raw)
		if err != nil {
			t.Fatal(err)
		}
		fresh = append(fresh, *node)
	}
	if _, err := commitSubscriptionRefresh(sqlite, 0, expected, fresh, "refreshed", "new info", true); err != nil {
		t.Fatal(err)
	}
	proxy = sqliteReferenceGet(t, sqlite, "dockerProxies", "1081")
	if gjson.GetBytes(proxy, "which.id").Int() != 2 || gjson.GetBytes(proxy, "frontWhich.id").Int() != 1 {
		t.Fatal("SQLite refresh did not remap main and front Docker nodes")
	}
	if gjson.GetBytes(proxy, "future.large").Raw != "9007199254740993" || gjson.GetBytes(proxy, "future.enabled").Raw != "false" || gjson.GetBytes(proxy, "which.extension").Raw != "9007199254740995" {
		t.Fatal("SQLite rewrite changed unknown Docker fields")
	}
	for _, name := range []string{"proxy", "中", "x", "orphan"} {
		raw := sqliteReferenceGet(t, sqlite, "outbound."+name, "connectedServers")
		if gjson.GetBytes(raw, "touches.1.id").Int() != 3 || gjson.GetBytes(raw, "future").Raw != "9007199254740993" {
			t.Fatalf("SQLite refresh failed to preserve outbound %s", name)
		}
	}
	subscriptions := sqliteReferenceGet(t, sqlite, "touch", "subscriptions")
	if gjson.GetBytes(subscriptions, "0.future").Raw != "9007199254740993" || gjson.GetBytes(subscriptions, "0.servers.1.futureWrapper").Raw != "9007199254740995" {
		t.Fatal("SQLite refresh lost unknown subscription data")
	}
	before := sqliteReferenceSnapshot(t, sqlite)
	if _, err := commitSubscriptionRefresh(sqlite, 0, expected, fresh, "stale", "", true); !errors.Is(err, ErrSubscriptionChanged) {
		t.Fatalf("stale SQLite subscription write accepted: %v", err)
	}
	if sqliteReferenceSnapshot(t, sqlite).Manifest != before.Manifest {
		t.Fatal("rejected SQLite CAS changed data")
	}
	for _, bucket := range before.Buckets {
		if string(bucket.Name) == "unknown" {
			if bucket.Sequence != ^uint64(0) || len(bucket.Buckets) != 1 || len(bucket.Buckets[0].Values) != 1 {
				t.Fatal("reference update lost unknown nested data")
			}
			return
		}
	}
	t.Fatal("reference update dropped unknown bucket")
}

func TestSQLiteReferenceLateFailuresRollBackAllWrites(t *testing.T) {
	for _, action := range []string{"delete", "refresh"} {
		t.Run(action, func(t *testing.T) {
			sqlite, nodes := sqliteReferenceFixture(t)
			before := sqliteReferenceSnapshot(t, sqlite)
			injected := errors.New("injected last node write failure")
			checked := &referenceCheckedStore{Store: sqlite, fault: func(op, bucket, key string) error {
				if op == "put" && bucket == "touch" {
					return injected
				}
				return nil
			}}
			var err error
			if action == "delete" {
				_, err = deleteTouchesAtomic(checked, []*Which{{TYPE: ServerType, ID: 1}})
			} else {
				expected, e := readSubscriptionSnapshot(sqlite, 0)
				if e != nil {
					t.Fatal(e)
				}
				var fresh []ServerRaw
				for _, raw := range []json.RawMessage{nodes[2], nodes[0], nodes[1]} {
					node, e := Bytes2ServerRaw(raw)
					if e != nil {
						t.Fatal(e)
					}
					fresh = append(fresh, *node)
				}
				_, err = commitSubscriptionRefresh(checked, 0, expected, fresh, "new", "", true)
			}
			if !errors.Is(err, injected) || checked.writes < 2 || checked.updates != 1 || checked.views != 0 {
				t.Fatalf("did not exercise one transaction with late failure: writes=%d, err=%v", checked.writes, err)
			}
			if sqliteReferenceSnapshot(t, sqlite).Manifest != before.Manifest {
				t.Fatal("SQLite reference failure committed partial references")
			}
		})
	}
}

func TestSQLiteDockerCRUDUsesRuntimeStore(t *testing.T) {
	sqlite, _ := sqliteReferenceFixture(t)
	proxies, err := getDockerProxies(sqlite)
	if err != nil || len(proxies) != 2 {
		t.Fatalf("list SQLite proxies: %v", err)
	}
	changed := proxies[0]
	changed.Status = "running"
	if err := saveDockerProxy(sqlite, changed); err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(sqliteReferenceGet(t, sqlite, "dockerProxies", "1080"), "status").String(); got != "running" {
		t.Fatal("SQLite proxy update not persisted")
	}
	if err := removeDockerProxy(sqlite, 1080); err != nil {
		t.Fatal(err)
	}
	proxies, err = getDockerProxies(sqlite)
	if err != nil || len(proxies) != 1 || proxies[0].Port != 1081 {
		t.Fatalf("remove SQLite proxy: %v", err)
	}
}
