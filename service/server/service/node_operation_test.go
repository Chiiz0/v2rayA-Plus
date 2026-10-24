package service

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/configure"
	"go.etcd.io/bbolt"
)

func nodeOperationServer(name string) configure.ServerRaw {
	return configure.ServerRaw{ServerObj: &serverObj.SOCKS{
		Name: name, Protocol: "socks5", Server: "127.0.0.1", Port: 19080,
	}, Latency: "123ms"}
}

func seedNodeOperationDB(t *testing.T) {
	t.Helper()
	resetNodeTestDB(t)
	servers := make([]*configure.ServerRaw, 3)
	subscriptions := make([]*configure.SubscriptionRaw, 3)
	for i := range servers {
		s := nodeOperationServer(fmt.Sprintf("standalone-%d", i+1))
		servers[i] = &s
		subscriptions[i] = &configure.SubscriptionRaw{
			Address: fmt.Sprintf("https://example.invalid/sub-%d", i+1),
			Remarks: fmt.Sprintf("subscription-%d", i+1),
			Servers: []configure.ServerRaw{nodeOperationServer(fmt.Sprintf("sub-%d-node-1", i+1)), nodeOperationServer(fmt.Sprintf("sub-%d-node-2", i+1))},
		}
	}
	if err := configure.AppendServers(servers); err != nil {
		t.Fatal(err)
	}
	if err := configure.AppendSubscriptions(subscriptions); err != nil {
		t.Fatal(err)
	}
}

func saveNodeOperationProxy(t *testing.T, proxy configure.DockerProxy) {
	t.Helper()
	if err := configure.SaveDockerProxy(proxy); err != nil {
		t.Fatal(err)
	}
}

// Read raw bucket contents to detect any partial write after a rejected operation.
func nodeOperationSnapshot(t *testing.T) map[string]string {
	t.Helper()
	contents := make(map[string]string)
	err := db.DB().View(func(tx *bbolt.Tx) error {
		var visit func(string, *bbolt.Bucket) error
		visit = func(prefix string, bucket *bbolt.Bucket) error {
			return bucket.ForEach(func(k, v []byte) error {
				key := prefix + "/" + string(k)
				if v == nil {
					contents[key] = "<bucket>"
					return visit(key, bucket.Bucket(k))
				}
				contents[key] = string(v)
				return nil
			})
		}
		return tx.ForEach(func(name []byte, bucket *bbolt.Bucket) error {
			contents[string(name)] = "<bucket>"
			return visit(string(name), bucket)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func fakeNodeOperationDocker(t *testing.T, gate string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("V2RAYA_NODE_TEST_DIR", dir)
	t.Setenv("V2RAYA_NODE_TEST_GATE", gate)
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$V2RAYA_NODE_TEST_DIR/calls"
if [ "$1" != "$V2RAYA_NODE_TEST_GATE" ]; then exit 99; fi
: > "$V2RAYA_NODE_TEST_DIR/started"
while [ ! -f "$V2RAYA_NODE_TEST_DIR/release" ]; do sleep 0.01; done
case "$1" in
  pause) exit 0 ;;
  inspect) printf 'true\tfalse'; exit 0 ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertNoNodeOperationDockerCalls(t *testing.T, dir string) {
	t.Helper()
	if calls, err := os.ReadFile(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatalf("node deletion must not call Docker: calls=%q, error=%v", calls, err)
	}
}

func TestDeleteWhichRemapsAllStoredReferences(t *testing.T) {
	seedNodeOperationDB(t)
	dir := fakeNodeOperationDocker(t, "")
	first := configure.DockerProxy{
		Port: 19001, Which: configure.Which{TYPE: configure.ServerType, ID: 3},
		FrontWhich: &configure.Which{TYPE: configure.SubscriptionServerType, Sub: 2, ID: 2},
		Core:       configure.DockerProxyCoreXray, Status: "paused", ContainerName: "v2raya-socks-19001",
		ServerName: "standalone-3", FrontServerName: "sub-3-node-2",
	}
	second := configure.DockerProxy{
		Port: 19002, Which: configure.Which{TYPE: configure.SubscriptionServerType, Sub: 1, ID: 1},
		FrontWhich: &configure.Which{TYPE: configure.ServerType, ID: 2},
		Core:       configure.DockerProxyCoreV2Fly, Status: "running", ContainerName: "v2raya-socks-19002",
		ServerName: "sub-2-node-1", FrontServerName: "standalone-2",
	}
	saveNodeOperationProxy(t, first)
	saveNodeOperationProxy(t, second)
	for _, outbound := range []string{"custom", "removed"} {
		if err := configure.AddOutbound(outbound); err != nil {
			t.Fatal(err)
		}
	}
	for _, which := range []configure.Which{
		{TYPE: configure.ServerType, ID: 1, Outbound: "proxy"},
		{TYPE: configure.ServerType, ID: 3, Outbound: "proxy"},
		{TYPE: configure.SubscriptionServerType, Sub: 0, ID: 1, Outbound: "custom"},
		{TYPE: configure.SubscriptionServerType, Sub: 1, ID: 2, Outbound: "custom"},
		{TYPE: configure.ServerType, ID: 1, Outbound: "removed"},
	} {
		if err := configure.AddConnect(which); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteWhich([]*configure.Which{
		{TYPE: configure.ServerType, ID: 1},
		{TYPE: configure.SubscriptionType, ID: 1},
		{TYPE: configure.ServerType, ID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if servers, subscriptions := configure.GetServers(), configure.GetSubscriptions(); len(servers) != 2 || servers[0].ServerObj.GetName() != "standalone-2" || len(subscriptions) != 2 || subscriptions[0].Remarks != "subscription-2" {
		t.Fatal("wrong server or subscription was removed")
	}
	first.Which.ID = 2
	first.FrontWhich.Sub = 1
	second.Which.Sub = 0
	second.FrontWhich.ID = 1
	for _, want := range []configure.DockerProxy{first, second} {
		got, ok, err := findDockerProxy(want.Port)
		if err != nil || !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("Docker proxy metadata or references changed incorrectly: got=%+v, want=%+v, error=%v", got, want, err)
		}
	}
	for outbound, want := range map[string][]*configure.Which{
		"proxy":  {{TYPE: configure.ServerType, ID: 2, Outbound: "proxy"}},
		"custom": {{TYPE: configure.SubscriptionServerType, Sub: 0, ID: 2, Outbound: "custom"}},
	} {
		if got := configure.GetConnectedServersByOutbound(outbound).Get(); !reflect.DeepEqual(got, want) {
			t.Fatalf("outbound %q connections = %+v, want %+v", outbound, got, want)
		}
	}
	if configure.GetConnectedServersByOutbound("removed").Len() != 0 {
		t.Fatal("the last removed connection survived in its outbound")
	}
	assertNoNodeOperationDockerCalls(t, dir)
}

func TestDeleteWhichRejectsDockerDependenciesAtomically(t *testing.T) {
	for _, tt := range []struct {
		name  string
		which configure.Which
		front bool
	}{
		{name: "target standalone", which: configure.Which{TYPE: configure.ServerType, ID: 3}},
		{name: "front standalone", which: configure.Which{TYPE: configure.ServerType, ID: 3}, front: true},
		{name: "target subscription", which: configure.Which{TYPE: configure.SubscriptionServerType, Sub: 2, ID: 2}},
		{name: "front subscription", which: configure.Which{TYPE: configure.SubscriptionServerType, Sub: 2, ID: 2}, front: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seedNodeOperationDB(t)
			dir := fakeNodeOperationDocker(t, "")
			proxy := configure.DockerProxy{Port: 19001, Which: tt.which, Status: "paused"}
			if tt.front {
				proxy.Which = configure.Which{TYPE: configure.ServerType, ID: 2}
				front := tt.which
				proxy.FrontWhich = &front
			}
			saveNodeOperationProxy(t, proxy)
			before := nodeOperationSnapshot(t)
			if err := DeleteWhich([]*configure.Which{
				{TYPE: configure.ServerType, ID: 1}, {TYPE: configure.ServerType, ID: 3},
				{TYPE: configure.SubscriptionType, ID: 1}, {TYPE: configure.SubscriptionType, ID: 3},
			}); err == nil {
				t.Fatal("deletion accepted a Docker target/front dependency")
			}
			if after := nodeOperationSnapshot(t); !reflect.DeepEqual(before, after) {
				t.Fatal("a rejected mixed deletion changed persistent state")
			}
			assertNoNodeOperationDockerCalls(t, dir)
		})
	}
}

func TestDeleteWhichRejectsMalformedDockerJSONAtomically(t *testing.T) {
	seedNodeOperationDB(t)
	dir := fakeNodeOperationDocker(t, "")
	if err := db.DB().Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("dockerProxies"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("19001"), []byte(`{"port":19001,"which":`))
	}); err != nil {
		t.Fatal(err)
	}
	before := nodeOperationSnapshot(t)
	if err := DeleteWhich([]*configure.Which{{TYPE: configure.ServerType, ID: 1}}); err == nil {
		t.Fatal("deletion ignored unreadable Docker references")
	}
	if after := nodeOperationSnapshot(t); !reflect.DeepEqual(before, after) {
		t.Fatal("malformed Docker JSON allowed a partial deletion")
	}
	assertNoNodeOperationDockerCalls(t, dir)
}

func TestDockerStatusSaveCannotUndoNodeReferenceRemapping(t *testing.T) {
	for _, tt := range []struct {
		name, command, status string
		operation             func(int) error
	}{
		{name: "pause", command: "pause", status: "paused", operation: PauseDockerProxy},
		{name: "resume", command: "inspect", status: "running", operation: ResumeDockerProxy},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seedNodeOperationDB(t)
			dir := fakeNodeOperationDocker(t, tt.command)
			saveNodeOperationProxy(t, configure.DockerProxy{
				Port: 19001, Which: configure.Which{TYPE: configure.ServerType, ID: 3},
				FrontWhich: &configure.Which{TYPE: configure.ServerType, ID: 2},
				Core:       configure.DockerProxyCoreXray, Status: "running",
			})
			statusDone := make(chan error, 1)
			statusStopped := make(chan struct{})
			go func() {
				defer close(statusStopped)
				statusDone <- tt.operation(19001)
			}()
			release := func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600) }
			var deleteStopped chan struct{}
			t.Cleanup(func() {
				release()
				for _, stopped := range []chan struct{}{statusStopped, deleteStopped} {
					if stopped == nil {
						continue
					}
					select {
					case <-stopped:
					case <-time.After(3 * time.Second):
						t.Error("node operation did not finish during test cleanup")
					}
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
					break
				}
				select {
				case err := <-statusDone:
					t.Fatalf("Docker operation returned before its gated command: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("Docker operation did not reach its gated command")
				}
				time.Sleep(5 * time.Millisecond)
			}
			deleteDone := make(chan error, 1)
			deleteStopped = make(chan struct{})
			go func() {
				defer close(deleteStopped)
				deleteDone <- DeleteWhich([]*configure.Which{{TYPE: configure.ServerType, ID: 1}})
			}()
			select {
			case err := <-deleteDone:
				t.Fatalf("deletion escaped the in-flight Docker save: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			release()
			for name, done := range map[string]<-chan error{"Docker status": statusDone, "node deletion": deleteDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("%s failed: %v", name, err)
					}
				case <-time.After(3 * time.Second):
					t.Fatalf("%s deadlocked", name)
				}
			}
			proxy, ok, err := findDockerProxy(19001)
			if err != nil || !ok || proxy.Which.ID != 2 || proxy.FrontWhich == nil || proxy.FrontWhich.ID != 1 || proxy.Status != tt.status {
				t.Fatalf("Docker save restored stale references: %+v, error=%v", proxy, err)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil || strings.TrimSpace(string(calls)) != tt.command {
				t.Fatalf("unexpected runtime command during node deletion: %q, error=%v", calls, err)
			}
		})
	}
}
