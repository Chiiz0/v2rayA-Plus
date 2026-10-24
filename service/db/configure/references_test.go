package configure

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"go.etcd.io/bbolt"
)

func referenceTestDB(t *testing.T) *bbolt.DB {
	t.Helper()
	database, err := bbolt.Open(filepath.Join(t.TempDir(), "bolt.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func referenceTestNode(t *testing.T, name string) json.RawMessage {
	t.Helper()
	return marshalRaw(t, ServerRaw{ServerObj: &serverObj.HTTP{Name: name, Server: "example.invalid", Port: 443, Protocol: "http"}, Latency: "10ms"})
}

func referenceTestSub(t *testing.T, nodes ...json.RawMessage) json.RawMessage {
	t.Helper()
	return marshalRaw(t, map[string]interface{}{"address": "https://example.invalid/sub", "remarks": "keep", "autoSelect": true, "servers": nodes, "future": json.RawMessage(`9007199254740993`)})
}

func referenceTestPut(t *testing.T, database *bbolt.DB, bucket, key string, value []byte) {
	t.Helper()
	if err := database.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), value)
	}); err != nil {
		t.Fatal(err)
	}
}

func referenceTestGet(t *testing.T, database *bbolt.DB, bucket, key string) []byte {
	t.Helper()
	var result []byte
	if err := database.View(func(tx *bbolt.Tx) error {
		if b := tx.Bucket([]byte(bucket)); b != nil {
			result = append([]byte(nil), b.Get([]byte(key))...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func referenceTestContents(t *testing.T, database *bbolt.DB) map[string]map[string]string {
	t.Helper()
	result := map[string]map[string]string{}
	if err := database.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(name []byte, bucket *bbolt.Bucket) error {
			values := map[string]string{}
			result[string(name)] = values
			return bucket.ForEach(func(key, value []byte) error { values[string(key)] = string(value); return nil })
		})
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func referenceTestNames(t *testing.T, database *bbolt.DB, names ...string) {
	t.Helper()
	set := map[[32]byte]interface{}{}
	for i, name := range names {
		set[[32]byte{byte(i)}] = name
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(set); err != nil {
		t.Fatal(err)
	}
	referenceTestPut(t, database, "outbounds", "names", buf.Bytes())
}

func referenceTestConnections(t *testing.T, database *bbolt.DB, name string, whiches ...Which) {
	t.Helper()
	referenceTestPut(t, database, "outbound."+name, "connectedServers", marshalRaw(t, map[string]interface{}{"touches": whiches, "future": json.RawMessage(`9007199254740993`)}))
}

func referenceTestProxy(t *testing.T, database *bbolt.DB, port int, which Which, front *Which) {
	t.Helper()
	raw := marshalRaw(t, DockerProxy{Port: port, Which: which, FrontWhich: front, Core: DockerProxyCoreXray, ContainerName: fmt.Sprintf("v2raya-socks-%d", port), Status: "paused"})
	raw, err := sjson.SetRawBytes(raw, "future", []byte(`{"large":9007199254740993,"enabled":false}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = sjson.SetRawBytes(raw, "which.extension", []byte(`9007199254740995`))
	if err != nil {
		t.Fatal(err)
	}
	referenceTestPut(t, database, "dockerProxies", fmt.Sprint(port), raw)
}

func TestDeleteTouchesAtomicRemapsAllReferences(t *testing.T) {
	database := referenceTestDB(t)
	var nodes []json.RawMessage
	for i := 1; i <= 5; i++ {
		node, _ := sjson.SetRawBytes(referenceTestNode(t, fmt.Sprint(i)), "future", []byte(`9007199254740993`))
		nodes = append(nodes, node)
	}
	referenceTestPut(t, database, "touch", "servers", marshalRaw(t, nodes))
	sub := referenceTestSub(t, nodes[0], nodes[1])
	referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{sub, sub, sub}))
	front := Which{TYPE: SubscriptionServerType, Sub: 2, ID: 2}
	referenceTestProxy(t, database, 1080, Which{TYPE: ServerType, ID: 4}, &front)
	front = Which{TYPE: ServerType, ID: 3}
	referenceTestProxy(t, database, 1081, Which{TYPE: ServerType, ID: 4}, &front)
	referenceTestNames(t, database, "中", "x")
	referenceTestConnections(t, database, "proxy", Which{TYPE: ServerType, ID: 1}, Which{TYPE: ServerType, ID: 4})
	referenceTestConnections(t, database, "中", Which{TYPE: SubscriptionServerType, Sub: 2, ID: 2, Outbound: "中"})
	referenceTestConnections(t, database, "x", Which{TYPE: ServerType, ID: 3, Outbound: "x"})
	referenceTestConnections(t, database, "orphan", Which{TYPE: ServerType, ID: 4, Outbound: "orphan"})
	removed, err := deleteTouchesAtomic(database, []*Which{{TYPE: ServerType, ID: 5}, {TYPE: ServerType, ID: 2}, {TYPE: ServerType, ID: 1}, {TYPE: SubscriptionType, ID: 1}, {TYPE: ServerType, ID: 2}})
	if err != nil || !removed {
		t.Fatalf("delete = %v, %v", removed, err)
	}
	for port, wantFront := range map[int]int{1080: 2, 1081: 1} {
		raw := referenceTestGet(t, database, "dockerProxies", fmt.Sprint(port))
		if gjson.GetBytes(raw, "which.id").Int() != 2 || gjson.GetBytes(raw, "frontWhich.id").Int() != int64(wantFront) || gjson.GetBytes(raw, "status").String() != "paused" {
			t.Fatalf("wrong proxy remap: %s", raw)
		}
		if port == 1080 && gjson.GetBytes(raw, "frontWhich.sub").Int() != 1 {
			t.Fatalf("subscription index not remapped: %s", raw)
		}
		if gjson.GetBytes(raw, "future.large").Raw != "9007199254740993" || gjson.GetBytes(raw, "which.extension").Raw != "9007199254740995" {
			t.Fatal("unknown proxy fields lost precision")
		}
	}
	for name, expected := range map[string]string{"proxy": "2", "x": "1", "中": "2", "orphan": "2"} {
		raw := referenceTestGet(t, database, "outbound."+name, "connectedServers")
		if gjson.GetBytes(raw, "touches.#").Int() != 1 || gjson.GetBytes(raw, "touches.0.id").Raw != expected || gjson.GetBytes(raw, "future").Raw != "9007199254740993" {
			t.Fatalf("bad outbound remap %s: %s", name, raw)
		}
	}
	got := referenceTestGet(t, database, "touch", "servers")
	if gjson.GetBytes(got, "#").Int() != 2 || gjson.GetBytes(got, "0.serverObj.name").String() != "3" || gjson.GetBytes(got, "1.future").Raw != "9007199254740993" {
		t.Fatalf("wrong surviving nodes: %s", got)
	}
}

func TestDeleteTouchesAtomicRefusesAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"target", "front", "subscription", "invalid target", "bad JSON", "bad outbound names", "bad connection", "invalid deletion"} {
		t.Run(scenario, func(t *testing.T) {
			database := referenceTestDB(t)
			nodes := []json.RawMessage{referenceTestNode(t, "first"), referenceTestNode(t, "second")}
			referenceTestPut(t, database, "touch", "servers", marshalRaw(t, nodes))
			referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, nodes...)}))
			deletion := []*Which{{TYPE: ServerType, ID: 1}}
			which, front := Which{TYPE: ServerType, ID: 2}, (*Which)(nil)
			switch scenario {
			case "target":
				which.ID = 1
			case "front":
				front = &Which{TYPE: ServerType, ID: 1}
			case "subscription":
				front = &Which{TYPE: SubscriptionServerType, Sub: 0, ID: 2}
				deletion = []*Which{{TYPE: SubscriptionType, ID: 1}}
			case "invalid target":
				which.ID = 3
			case "invalid deletion":
				deletion[0].ID = -1
			}
			referenceTestProxy(t, database, 1080, which, front)
			if scenario == "target" {
				referenceTestProxy(t, database, 1081, which, nil)
			}
			if scenario == "bad JSON" {
				referenceTestPut(t, database, "dockerProxies", "1080", []byte(`{"port":1080,"secret":"do-not-report",`))
			}
			if scenario == "bad outbound names" {
				referenceTestPut(t, database, "outbounds", "names", []byte("not gob"))
			}
			if scenario == "bad connection" {
				referenceTestPut(t, database, "outbound.proxy", "connectedServers", []byte(`{"touches":[{"id":99,"_type":"server"}]}`))
			}
			before := referenceTestContents(t, database)
			removed, err := deleteTouchesAtomic(database, deletion)
			if err == nil || removed || strings.Contains(err.Error(), "do-not-report") {
				t.Fatalf("expected clean refusal, got %v, %v", removed, err)
			}
			if scenario == "target" && (!strings.Contains(err.Error(), "1080") || !strings.Contains(err.Error(), "1081")) {
				t.Fatalf("missing affected ports: %v", err)
			}
			if after := referenceTestContents(t, database); !reflect.DeepEqual(before, after) {
				t.Fatal("failed transaction changed stored data")
			}
		})
	}
}

func TestSubscriptionRefreshAtomicRemapAndMetadata(t *testing.T) {
	database := referenceTestDB(t)
	a, b, c := referenceTestNode(t, "a"), referenceTestNode(t, "b"), referenceTestNode(t, "c")
	a, _ = sjson.SetRawBytes(a, "futureWrapper", []byte(`9007199254740995`))
	referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, a, b, c)}))
	front := Which{TYPE: SubscriptionServerType, Sub: 0, ID: 3}
	for _, port := range []int{1080, 1081} {
		referenceTestProxy(t, database, port, Which{TYPE: SubscriptionServerType, Sub: 0, ID: 1}, &front)
	}
	referenceTestNames(t, database, "中", "x")
	for _, name := range []string{"proxy", "中", "x"} {
		referenceTestConnections(t, database, name, Which{TYPE: SubscriptionServerType, Sub: 0, ID: 2, Outbound: name})
	}
	snapshot, err := readSubscriptionSnapshot(database, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fresh []ServerRaw
	for _, raw := range []json.RawMessage{c, a, b} {
		node, err := Bytes2ServerRaw(raw)
		if err != nil {
			t.Fatal(err)
		}
		fresh = append(fresh, *node)
	}
	removed, err := commitSubscriptionRefresh(database, 0, snapshot, fresh, "new status", "new info", true)
	if err != nil || removed {
		t.Fatalf("refresh = %v, %v", removed, err)
	}
	for _, port := range []int{1080, 1081} {
		raw := referenceTestGet(t, database, "dockerProxies", fmt.Sprint(port))
		if gjson.GetBytes(raw, "which.id").Int() != 2 || gjson.GetBytes(raw, "frontWhich.id").Int() != 1 || gjson.GetBytes(raw, "future.large").Raw != "9007199254740993" {
			t.Fatalf("bad Docker refresh mapping: %s", raw)
		}
	}
	for _, name := range []string{"proxy", "中", "x"} {
		if raw := referenceTestGet(t, database, "outbound."+name, "connectedServers"); gjson.GetBytes(raw, "touches.0.id").Int() != 3 {
			t.Fatalf("bad outbound mapping: %s", raw)
		}
	}
	raw := referenceTestGet(t, database, "touch", "subscriptions")
	for path, want := range map[string]string{"0.future": "9007199254740993", "0.servers.1.futureWrapper": "9007199254740995", "0.remarks": `"keep"`, "0.autoSelect": "true", "0.status": `"new status"`, "0.info": `"new info"`} {
		if got := gjson.GetBytes(raw, path).Raw; got != want {
			t.Fatalf("lost metadata %s = %s, want %s", path, got, want)
		}
	}
}

func TestSubscriptionRefreshMissingConnections(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			database := referenceTestDB(t)
			old := referenceTestNode(t, "old")
			old, _ = sjson.SetRawBytes(old, "future", []byte(`9007199254740993`))
			referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, old)}))
			referenceTestNames(t, database, "中")
			for _, name := range []string{"proxy", "中"} {
				referenceTestConnections(t, database, name, Which{TYPE: SubscriptionServerType, Sub: 0, ID: 1, Outbound: name})
			}
			snapshot, _ := readSubscriptionSnapshot(database, 0)
			fresh, _ := Bytes2ServerRaw(referenceTestNode(t, "new"))
			removed, err := commitSubscriptionRefresh(database, 0, snapshot, []ServerRaw{*fresh}, "new", "new", disconnect)
			if err != nil || removed != disconnect {
				t.Fatalf("refresh = %v, %v", removed, err)
			}
			for _, name := range []string{"proxy", "中"} {
				raw := referenceTestGet(t, database, "outbound."+name, "connectedServers")
				if disconnect && gjson.GetBytes(raw, "touches.#").Int() != 0 || !disconnect && gjson.GetBytes(raw, "touches.0.id").Int() != 2 {
					t.Fatalf("bad missing connection policy: %s", raw)
				}
			}
			raw := referenceTestGet(t, database, "touch", "subscriptions")
			if !disconnect && (gjson.GetBytes(raw, "0.servers.#").Int() != 2 || gjson.GetBytes(raw, "0.servers.1").Raw != string(old)) {
				t.Fatalf("old node was not preserved exactly once: %s", raw)
			}
		})
	}
}

func TestSubscriptionRefreshRefusesMissingAmbiguousAndStale(t *testing.T) {
	for _, scenario := range []string{"missing target", "missing front", "duplicate new", "duplicate old", "changed credentials", "unknown field", "stale snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			database := referenceTestDB(t)
			a, b := referenceTestNode(t, "a"), referenceTestNode(t, "b")
			old := []json.RawMessage{a, b}
			fresh := []json.RawMessage{b, a}
			which := Which{TYPE: SubscriptionServerType, Sub: 0, ID: 1}
			var front *Which
			switch scenario {
			case "missing target":
				fresh = []json.RawMessage{b}
			case "missing front":
				which.ID, front = 2, &Which{TYPE: SubscriptionServerType, Sub: 0, ID: 1}
				fresh = []json.RawMessage{b}
			case "duplicate new":
				fresh = []json.RawMessage{a, a, b}
			case "duplicate old":
				old = []json.RawMessage{a, a, b}
			case "changed credentials":
				fresh[1], _ = sjson.SetBytes(a, "serverObj.password", "changed")
			case "unknown field":
				old[0], _ = sjson.SetRawBytes(a, "serverObj.future", []byte(`9007199254740993`))
			}
			referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, old...)}))
			referenceTestProxy(t, database, 1080, which, front)
			snapshot, _ := readSubscriptionSnapshot(database, 0)
			if scenario == "stale snapshot" {
				changed, _ := sjson.SetBytes(snapshot, "remarks", "edited during download")
				referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{changed}))
			}
			var nodes []ServerRaw
			for _, raw := range fresh {
				node, _ := Bytes2ServerRaw(raw)
				nodes = append(nodes, *node)
			}
			before := referenceTestContents(t, database)
			removed, err := commitSubscriptionRefresh(database, 0, snapshot, nodes, "new", "new", false)
			if err == nil || removed {
				t.Fatalf("expected refresh refusal, got %v, %v", removed, err)
			}
			if scenario == "stale snapshot" && !errors.Is(err, ErrSubscriptionChanged) {
				t.Fatalf("not a CAS conflict: %v", err)
			}
			if !reflect.DeepEqual(before, referenceTestContents(t, database)) {
				t.Fatal("rejected refresh wrote data")
			}
		})
	}
}

func TestSubscriptionRefreshConcurrentCAS(t *testing.T) {
	database := referenceTestDB(t)
	node := referenceTestNode(t, "same")
	referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, node)}))
	snapshot, _ := readSubscriptionSnapshot(database, 0)
	parsed, _ := Bytes2ServerRaw(node)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := commitSubscriptionRefresh(database, 0, snapshot, []ServerRaw{*parsed}, fmt.Sprint(i), "new", false)
			errors <- err
		}(i)
	}
	wg.Wait()
	first, second := <-errors, <-errors
	if (first == nil) == (second == nil) || first != nil && first != ErrSubscriptionChanged || second != nil && second != ErrSubscriptionChanged {
		t.Fatalf("expected one commit and one conflict: %v, %v", first, second)
	}
}

func TestReadConnectedServerSnapshotKeepsPositionsAndOrder(t *testing.T) {
	database := referenceTestDB(t)
	first, second := referenceTestNode(t, "first"), referenceTestNode(t, "second")
	referenceTestPut(t, database, "touch", "servers", marshalRaw(t, []json.RawMessage{json.RawMessage(`{"broken":true}`), first, second}))
	referenceTestNames(t, database, "中", "x")
	referenceTestConnections(t, database, "proxy", Which{TYPE: ServerType, ID: 2, Outbound: "proxy"})
	referenceTestConnections(t, database, "x", Which{TYPE: ServerType, ID: 3, Outbound: "x"})
	referenceTestConnections(t, database, "中", Which{TYPE: ServerType, ID: 2, Outbound: "中"})
	referenceTestPut(t, database, "dockerProxies", "1080", []byte("broken unrelated Docker metadata"))
	snapshot, err := readConnectedServerSnapshot(database)
	if err != nil || len(snapshot) != 3 {
		t.Fatalf("read connected = %+v, %v", snapshot, err)
	}
	for i, name := range []string{"proxy", "x", "中"} {
		if snapshot[i].Which.Outbound != name {
			t.Fatalf("outbound order changed: %+v", snapshot)
		}
	}
	if snapshot[0].Server.ServerObj.GetName() != "first" || snapshot[1].Server.ServerObj.GetName() != "second" {
		t.Fatal("bad preceding record shifted the selected nodes")
	}
	referenceTestConnections(t, database, "proxy", Which{TYPE: ServerType, ID: 1})
	if _, err = readConnectedServerSnapshot(database); err == nil {
		t.Fatal("accepted a reference to an invalid node")
	}
}

func TestReadConnectedServerSnapshotConcurrentDelete(t *testing.T) {
	database := referenceTestDB(t)
	first, second := referenceTestNode(t, "first"), referenceTestNode(t, "second")
	referenceTestPut(t, database, "touch", "servers", marshalRaw(t, []json.RawMessage{first, second}))
	referenceTestConnections(t, database, "proxy", Which{TYPE: ServerType, ID: 2, Outbound: "proxy"})
	ready := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(ready)
		for i := 0; i < 1000; i++ {
			snapshot, err := readConnectedServerSnapshot(database)
			if err != nil {
				result <- err
				return
			}
			if len(snapshot) != 1 || snapshot[0].Server.ServerObj.GetName() != "second" || snapshot[0].Which.ID < 1 || snapshot[0].Which.ID > 2 {
				result <- fmt.Errorf("connected snapshot crossed database versions: %+v", snapshot)
				return
			}
		}
		result <- nil
	}()
	<-ready
	if removed, err := deleteTouchesAtomic(database, []*Which{{TYPE: ServerType, ID: 1}}); err != nil || removed {
		t.Fatalf("renumber-only delete = %v, %v", removed, err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestReadConnectedServerSnapshotIgnoresUnusedBrokenSubscriptions(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(fmt.Sprint(connected), func(t *testing.T) {
			database := referenceTestDB(t)
			referenceTestPut(t, database, "touch", "servers", marshalRaw(t, []json.RawMessage{referenceTestNode(t, "valid")}))
			referenceTestPut(t, database, "touch", "subscriptions", []byte(`[null,{"servers":false}]`))
			if connected {
				referenceTestConnections(t, database, "proxy", Which{TYPE: ServerType, ID: 1})
			}
			snapshot, err := readConnectedServerSnapshot(database)
			if err != nil || !connected && len(snapshot) != 0 || connected && (len(snapshot) != 1 || snapshot[0].Server.ServerObj.GetName() != "valid") {
				t.Fatalf("unused subscription affected connected snapshot: %+v, %v", snapshot, err)
			}
		})
	}
}

func TestReferenceTransactionWriteFailurePreservesData(t *testing.T) {
	database := referenceTestDB(t)
	nodes := []json.RawMessage{referenceTestNode(t, "first"), referenceTestNode(t, "second")}
	referenceTestPut(t, database, "touch", "servers", marshalRaw(t, nodes))
	referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, nodes...)}))
	referenceTestProxy(t, database, 1080, Which{TYPE: ServerType, ID: 2}, nil)
	snapshot, _ := readSubscriptionSnapshot(database, 0)
	before := referenceTestContents(t, database)
	path := database.Path()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	readonly, err := bbolt.Open(path, 0600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if removed, err := deleteTouchesAtomic(readonly, []*Which{{TYPE: ServerType, ID: 1}}); removed || !errors.Is(err, bbolt.ErrDatabaseReadOnly) {
		t.Fatalf("expected write failure: %v, %v", removed, err)
	}
	node, _ := Bytes2ServerRaw(nodes[0])
	if removed, err := commitSubscriptionRefresh(readonly, 0, snapshot, []ServerRaw{*node}, "new", "new", true); removed || !errors.Is(err, bbolt.ErrDatabaseReadOnly) {
		t.Fatalf("expected write failure: %v, %v", removed, err)
	}
	if !reflect.DeepEqual(before, referenceTestContents(t, readonly)) {
		t.Fatal("write failure changed stored data")
	}
}

func TestReferenceIdentityVLESSLegacyXHTTPAndEncryption(t *testing.T) {
	const extra = `{"future":{"large":9007199254740993,"disabled":false},"xmux":{"hKeepAlivePeriod":-1},"scMinPostsIntervalMs":0}`
	query := url.Values{"type": {"xhttp"}, "security": {"none"}, "mode": {"packet-up"}, "encryption": {persistedVlessEncryption}, "extra": {extra}}
	link := "vless://11111111-1111-4111-8111-111111111111@example.invalid:443?" + query.Encode() + "#same"
	node, err := serverObj.ParseVlessURL(link)
	if err != nil {
		t.Fatal(err)
	}
	canonical := marshalRaw(t, ServerRaw{ServerObj: node})
	want, err := nodeReferenceIdentity(canonical)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := sjson.DeleteBytes(canonical, "serverObj.encryption")
	legacy, _ = sjson.DeleteBytes(legacy, "serverObj.xhttpExtra")
	legacy, _ = sjson.SetBytes(legacy, "serverObj.xhttpMode", "auto")
	if got, err := nodeReferenceIdentity(legacy); err != nil || got != want {
		t.Fatalf("legacy URI did not recover equivalent settings: %v", err)
	}
	reorderedLink := "vless://11111111-1111-4111-8111-111111111111@example.invalid:443?extra=" + url.QueryEscape(extra) + "&encryption=" + url.QueryEscape(persistedVlessEncryption) + "&mode=packet-up&security=none&type=xhttp#same"
	reordered, _ := sjson.SetBytes(canonical, "serverObj.v", reorderedLink)
	if got, err := nodeReferenceIdentity(reordered); err != nil || got != want {
		t.Fatalf("URI query order changed identity: %v", err)
	}
	for _, mutation := range []struct{ path, value string }{
		{"serverObj.encryption", `"none"`},
		{"serverObj.xhttpExtra.future.large", `9007199254740994`},
		{"serverObj.xhttpExtra.future.disabled", `true`},
		{"serverObj.xhttpExtra.xmux.hKeepAlivePeriod", `0`},
		{"serverObj.xhttpExtra.scMinPostsIntervalMs", `1`},
	} {
		changed, _ := sjson.SetRawBytes(canonical, mutation.path, []byte(mutation.value))
		if got, err := nodeReferenceIdentity(changed); err != nil || got == want {
			t.Fatalf("configuration change %s was not distinguished: %v", mutation.path, err)
		}
	}
	database := referenceTestDB(t)
	referenceTestPut(t, database, "touch", "subscriptions", marshalRaw(t, []json.RawMessage{referenceTestSub(t, legacy)}))
	referenceTestProxy(t, database, 1080, Which{TYPE: SubscriptionServerType, Sub: 0, ID: 1}, nil)
	snapshot, _ := readSubscriptionSnapshot(database, 0)
	if _, err = commitSubscriptionRefresh(database, 0, snapshot, []ServerRaw{{ServerObj: node}}, "updated", "", false); err != nil {
		t.Fatalf("legacy Docker node failed equivalent refresh: %v", err)
	}
	snapshot, _ = readSubscriptionSnapshot(database, 0)
	node.Encryption = "none"
	before := referenceTestContents(t, database)
	if _, err = commitSubscriptionRefresh(database, 0, snapshot, []ServerRaw{{ServerObj: node}}, "changed", "", false); err == nil {
		t.Fatal("changed encryption unexpectedly matched referenced node")
	}
	if !reflect.DeepEqual(before, referenceTestContents(t, database)) {
		t.Fatal("encryption conflict wrote data")
	}
}

func TestReferenceIdentityKeepsNestedUnknownFieldsAndRejectsNil(t *testing.T) {
	ss := &serverObj.Shadowsocks{Name: "node", Server: "example.invalid", Port: 443, Password: "secret", Cipher: "aes-128-gcm", Protocol: "shadowsocks"}
	before := marshalRaw(t, ServerRaw{ServerObj: ss})
	a, _ := sjson.SetRawBytes(before, "serverObj.plugin.opts.future", []byte(`9007199254740993`))
	b, _ := sjson.SetRawBytes(before, "serverObj.plugin.opts.future", []byte(`9007199254740994`))
	left, err := nodeReferenceIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := nodeReferenceIdentity(b)
	if err != nil || left == right {
		t.Fatalf("nested unknown configuration was discarded: %v", err)
	}
	database := referenceTestDB(t)
	for _, node := range []ServerRaw{{}, {ServerObj: (*serverObj.V2Ray)(nil)}} {
		if _, err := commitSubscriptionRefresh(database, 0, nil, []ServerRaw{node}, "", "", false); err == nil {
			t.Fatal("nil node must be rejected before starting a transaction")
		}
	}
}
