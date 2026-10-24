package configure

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/v2rayA/v2rayA/db/store"
)

func outboundConnectionsFixture(t *testing.T, backend string) store.Store {
	t.Helper()
	if backend == "sqlite" {
		s, _ := sqliteReferenceFixture(t)
		return s
	}
	database := referenceTestDB(t)
	nodes := []json.RawMessage{referenceTestNode(t, "a"), referenceTestNode(t, "b"), referenceTestNode(t, "c")}
	referenceTestPut(t, database, "touch", "servers", marshalRaw(t, nodes))
	referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, nodes...)}))
	referenceTestNames(t, database, "中", "x")
	for _, name := range []string{"proxy", "中", "x", "orphan"} {
		referenceTestConnections(t, database, name, Which{TYPE: ServerType, ID: 2, Outbound: name}, Which{TYPE: SubscriptionServerType, Sub: 0, ID: 2, Outbound: name})
	}
	return store.NewBolt(database)
}

func TestReplaceOutboundConnectionsAtomic(t *testing.T) {
	for _, backend := range []string{"bolt", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			s := outboundConnectionsFixture(t, backend)
			other := sqliteReferenceGet(t, s, "outbound.中", "connectedServers")
			checked := &referenceCheckedStore{Store: s}
			previous, err := replaceOutboundConnections(checked, "", []*Which{
				{TYPE: ServerType, ID: 1, Sub: 99, Latency: "123ms", Link: "ignored"},
				{TYPE: ServerType, ID: 1, Outbound: "proxy"},
				{TYPE: SubscriptionServerType, ID: 3, Sub: 0},
			}, 0)
			if err != nil || previous.Len() != 2 {
				t.Fatalf("replace: previous=%v err=%v", previous, err)
			}
			if checked.views != 0 || checked.updates != 1 || checked.writes != 1 {
				t.Fatalf("must validate and write once in one transaction: %+v", checked)
			}
			var got Whiches
			if err := json.Unmarshal(sqliteReferenceGet(t, s, "outbound.proxy", "connectedServers"), &got); err != nil {
				t.Fatal(err)
			}
			want := []*Which{{TYPE: ServerType, ID: 1, Outbound: "proxy"}, {TYPE: SubscriptionServerType, ID: 3, Sub: 0, Outbound: "proxy"}}
			if !reflect.DeepEqual(got.Get(), want) {
				t.Fatalf("normalization/dedup failed: %+v", got)
			}
			if _, err := replaceOutboundConnections(s, "proxy", []*Which{}, 1); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(sqliteReferenceGet(t, s, "outbound.proxy", "connectedServers"), &got); err != nil || got.Len() != 0 {
				t.Fatalf("explicit empty selection failed: %v", err)
			}
			if !reflect.DeepEqual(other, sqliteReferenceGet(t, s, "outbound.中", "connectedServers")) {
				t.Fatal("replacement modified another group")
			}
		})
	}
}

func TestReplaceOutboundConnectionsRejectsWholeRequest(t *testing.T) {
	for _, backend := range []string{"bolt", "sqlite"} {
		for _, tc := range []struct {
			name, outbound string
			touches        []*Which
			limit          int
		}{
			{name: "missing group", outbound: "missing"},
			{name: "orphan group", outbound: "orphan"},
			{name: "null member", touches: []*Which{{TYPE: ServerType, ID: 1}, nil}},
			{name: "mixed invalid", touches: []*Which{{TYPE: ServerType, ID: 1}, {TYPE: ServerType, ID: 4}}},
			{name: "negative sub", touches: []*Which{{TYPE: SubscriptionServerType, ID: 1, Sub: -1}}},
			{name: "subscription itself", touches: []*Which{{TYPE: SubscriptionType, ID: 1}}},
			{name: "cross group", touches: []*Which{{TYPE: ServerType, ID: 1, Outbound: "中"}}},
			{name: "single member core", touches: []*Which{{TYPE: ServerType, ID: 1}, {TYPE: ServerType, ID: 2}}, limit: 1},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				s := outboundConnectionsFixture(t, backend)
				before := sqliteReferenceGet(t, s, "outbound.proxy", "connectedServers")
				checked := &referenceCheckedStore{Store: s}
				if _, err := replaceOutboundConnections(checked, tc.outbound, tc.touches, tc.limit); err == nil {
					t.Fatal("invalid replacement accepted")
				}
				if checked.writes != 0 || !reflect.DeepEqual(before, sqliteReferenceGet(t, s, "outbound.proxy", "connectedServers")) {
					t.Fatal("rejected request changed the selection")
				}
			})
		}
	}
}

func TestReplaceOutboundConnectionsPropagatesStoreFailure(t *testing.T) {
	for _, backend := range []string{"bolt", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			s := outboundConnectionsFixture(t, backend)
			before := sqliteReferenceGet(t, s, "outbound.proxy", "connectedServers")
			injected := errors.New("write failure")
			checked := &referenceCheckedStore{Store: s, fault: func(op, _, _ string) error {
				if op == "put" {
					return injected
				}
				return nil
			}}
			if _, err := replaceOutboundConnections(checked, "proxy", []*Which{{TYPE: ServerType, ID: 1}}, 1); !errors.Is(err, injected) {
				t.Fatalf("lost write failure: %v", err)
			}
			if !reflect.DeepEqual(before, sqliteReferenceGet(t, s, "outbound.proxy", "connectedServers")) {
				t.Fatal("write failure changed selection")
			}
		})
	}
}

func TestOutboundConnectionsSnapshotRestoresOriginalBytes(t *testing.T) {
	for _, backend := range []string{"bolt", "sqlite"} {
		for _, raw := range [][]byte{nil, []byte("null"), []byte(`{"touches":[],"future":9007199254740993}`), []byte(`{"touches":[{"_type":"server","id":2,"outbound":"proxy","future":true}],"future":false}`)} {
			t.Run(backend+"/"+string(raw), func(t *testing.T) {
				s := outboundConnectionsFixture(t, backend)
				if err := s.Update(func(tx store.WriteTx) error {
					if raw == nil {
						return tx.Delete([]byte("outbound.proxy"), []byte("connectedServers"))
					}
					return tx.Put([]byte("outbound.proxy"), []byte("connectedServers"), raw)
				}); err != nil {
					t.Fatal(err)
				}
				snapshot, err := replaceOutboundConnections(s, "proxy", []*Which{{TYPE: ServerType, ID: 1}}, 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := snapshot.restore(s); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(raw, sqliteReferenceGet(t, s, "outbound.proxy", "connectedServers")) {
					t.Fatal("rollback changed the original group bytes")
				}
			})
		}
	}
}
