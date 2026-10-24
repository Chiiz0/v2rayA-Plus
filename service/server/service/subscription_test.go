package service

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/touch"
	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/configure"
)

func subscriptionTestNode(t *testing.T, name string, id int) configure.ServerRaw {
	t.Helper()
	link := fmt.Sprintf("vless://11111111-1111-4111-8111-%012d@example.com:443?type=xhttp&mode=packet-up&extra=%%7B%%22future%%22%%3A9007199254740993%%7D#%s", id, name)
	node, err := serverObj.ParseVlessURL(link)
	if err != nil {
		t.Fatal(err)
	}
	return configure.ServerRaw{ServerObj: node, Latency: "123ms"}
}

func subscriptionTestHTTP(t *testing.T, nodes ...configure.ServerRaw) *httptest.Server {
	t.Helper()
	var links []string
	for _, node := range nodes {
		links = append(links, node.ServerObj.ExportToURL())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n"))))
	}))
	t.Cleanup(server.Close)
	return server
}

func subscriptionTestStore(t *testing.T, address string, nodes ...configure.ServerRaw) []byte {
	t.Helper()
	if err := configure.AppendSubscriptions([]*configure.SubscriptionRaw{{Address: address, Remarks: "original", Status: "old", Info: "old info", AutoSelect: true, Servers: nodes}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := configure.ReadSubscriptionSnapshot(0)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestSubscriptionRefreshRemapsDockerAndMainConnections(t *testing.T) {
	resetNodeTestDB(t)
	first, second := subscriptionTestNode(t, "first", 1), subscriptionTestNode(t, "second", 2)
	server := subscriptionTestHTTP(t, second, first)
	snapshot := subscriptionTestStore(t, server.URL, first, second)
	target := configure.Which{TYPE: configure.SubscriptionServerType, Sub: 0, ID: 1, Outbound: "proxy"}
	front := configure.Which{TYPE: configure.SubscriptionServerType, Sub: 0, ID: 2, Outbound: "proxy"}
	if err := configure.AddConnect(target); err != nil {
		t.Fatal(err)
	}
	if err := configure.SaveDockerProxy(configure.DockerProxy{Port: 23101, Which: target, FrontWhich: &front, ContainerName: "untouched-container", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := updateSubscriptionSnapshotWithClient(0, snapshot, false, server.Client()); err != nil {
		t.Fatal(err)
	}
	updated := configure.GetSubscription(0)
	if updated.Servers[0].ServerObj.GetName() != "second" || updated.Servers[1].ServerObj.GetName() != "first" {
		t.Fatal("subscription was not reordered")
	}
	if updated.Remarks != "original" || !updated.AutoSelect || updated.Address != server.URL || updated.Status == "old" {
		t.Fatalf("subscription metadata changed incorrectly: %+v", updated)
	}
	connections := configure.GetConnectedServersByOutbound("proxy")
	if connections.Len() != 1 || connections.Get()[0].ID != 2 {
		t.Fatalf("main connection was not remapped: %+v", connections)
	}
	proxies, err := configure.GetDockerProxies()
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || proxies[0].Which.ID != 2 || proxies[0].FrontWhich == nil || proxies[0].FrontWhich.ID != 1 || proxies[0].ContainerName != "untouched-container" || proxies[0].Status != "running" {
		t.Fatalf("Docker references or runtime state changed incorrectly: %+v", proxies)
	}
}

func TestSubscriptionDownloadDoesNotHoldNodeLockAndRejectsChangedSnapshot(t *testing.T) {
	resetNodeTestDB(t)
	oldNode, newNode := subscriptionTestNode(t, "old", 1), subscriptionTestNode(t, "new", 2)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte(newNode.ServerObj.ExportToURL())))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	snapshot := subscriptionTestStore(t, server.URL, oldNode)
	finished := make(chan error, 1)
	go func() { finished <- updateSubscriptionSnapshotWithClient(0, snapshot, false, server.Client()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("subscription request did not start")
	}
	edited := make(chan error, 1)
	go func() {
		edited <- ModifySubscriptionRemark(touch.Subscription{ID: 1, Address: server.URL, Remarks: "edited while downloading", AutoSelect: true})
	}()
	select {
	case err := <-edited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		unblock()
		<-finished
		<-edited
		t.Fatal("network download held the node operation lock")
	}
	unblock()
	select {
	case err := <-finished:
		if !errors.Is(err, configure.ErrSubscriptionChanged) {
			t.Fatalf("stale update error = %v, want ErrSubscriptionChanged", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription update did not finish")
	}
	updated := configure.GetSubscription(0)
	if updated.Remarks != "edited while downloading" || updated.Status != "old" || len(updated.Servers) != 1 || updated.Servers[0].ServerObj.GetName() != "old" {
		t.Fatalf("stale refresh overwrote the saved edit: %+v", updated)
	}
}

func TestSubscriptionRefreshPreservesOrRemovesMissingMainConnection(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprintf("disconnect=%t", disconnect), func(t *testing.T) {
			resetNodeTestDB(t)
			oldNode, newNode := subscriptionTestNode(t, "old", 1), subscriptionTestNode(t, "new", 2)
			server := subscriptionTestHTTP(t, newNode)
			snapshot := subscriptionTestStore(t, server.URL, oldNode)
			if err := configure.AddConnect(configure.Which{TYPE: configure.SubscriptionServerType, Sub: 0, ID: 1, Outbound: "proxy"}); err != nil {
				t.Fatal(err)
			}
			if err := updateSubscriptionSnapshotWithClient(0, snapshot, disconnect, server.Client()); err != nil {
				t.Fatal(err)
			}
			subscription := configure.GetSubscription(0)
			connections := configure.GetConnectedServersByOutbound("proxy")
			if disconnect {
				if len(subscription.Servers) != 1 || connections.Len() != 0 {
					t.Fatalf("missing node was not removed: %+v, %+v", subscription, connections)
				}
			} else if len(subscription.Servers) != 2 || subscription.Servers[1].ServerObj.GetName() != "old" || subscription.Servers[1].Latency != "123ms" || connections.Len() != 1 || connections.Get()[0].ID != 2 {
				t.Fatalf("missing connected node was not retained: %+v, %+v", subscription, connections)
			}
		})
	}
}

func TestSubscriptionRefreshRejectsChangedDockerConfiguration(t *testing.T) {
	resetNodeTestDB(t)
	oldNode, newNode := subscriptionTestNode(t, "node", 1), subscriptionTestNode(t, "node", 2)
	server := subscriptionTestHTTP(t, newNode)
	snapshot := subscriptionTestStore(t, server.URL, oldNode)
	if err := configure.SaveDockerProxy(configure.DockerProxy{Port: 23102, Which: configure.Which{TYPE: configure.SubscriptionServerType, Sub: 0, ID: 1}, Status: "paused"}); err != nil {
		t.Fatal(err)
	}
	if err := updateSubscriptionSnapshotWithClient(0, snapshot, true, server.Client()); err == nil {
		t.Fatal("same endpoint with different credentials was allowed to replace a Docker reference")
	}
	current, err := configure.ReadSubscriptionSnapshot(0)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(snapshot) {
		t.Fatalf("rejected update changed the subscription:\n%s", current)
	}
	proxies, err := configure.GetDockerProxies()
	if err != nil || len(proxies) != 1 || proxies[0].Status != "paused" || proxies[0].Which.ID != 1 {
		t.Fatalf("rejected update changed Docker state: %+v, %v", proxies, err)
	}
}

func TestScheduledSubscriptionSnapshotDoesNotFollowMovedIndex(t *testing.T) {
	resetNodeTestDB(t)
	first, second := subscriptionTestNode(t, "first", 1), subscriptionTestNode(t, "second", 2)
	server := subscriptionTestHTTP(t, first)
	subscriptionTestStore(t, server.URL+"/first", first)
	if err := configure.AppendSubscriptions([]*configure.SubscriptionRaw{{Address: server.URL + "/second", Servers: []configure.ServerRaw{second}}}); err != nil {
		t.Fatal(err)
	}
	snapshots, err := SnapshotSubscriptionsForUpdate()
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("snapshots = %+v, %v", snapshots, err)
	}
	if err := configure.RemoveSubscriptions([]int{0}); err != nil {
		t.Fatal(err)
	}
	err = updateSubscriptionSnapshotWithClient(snapshots[0].Index, snapshots[0].Raw, false, server.Client())
	if !errors.Is(err, configure.ErrSubscriptionChanged) {
		t.Fatalf("moved subscription error = %v, want ErrSubscriptionChanged", err)
	}
	if got := configure.GetSubscription(0); got.Address != server.URL+"/second" || got.Servers[0].ServerObj.GetName() != "second" {
		t.Fatalf("scheduled update retargeted the shifted subscription: %+v", got)
	}
}

func TestUpdateSubscriptionRejectsMissingIndex(t *testing.T) {
	resetNodeTestDB(t)
	for _, index := range []int{-1, 0, 99} {
		if err := UpdateSubscription(index, false); err == nil {
			t.Fatalf("missing subscription %d did not return an error", index)
		}
	}
}

func TestSubscriptionHTTPFailureDoesNotCommitEmptyResponse(t *testing.T) {
	resetNodeTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	snapshot := subscriptionTestStore(t, server.URL, subscriptionTestNode(t, "original", 1))
	if err := updateSubscriptionSnapshotWithClient(0, snapshot, true, server.Client()); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("HTTP failure was not returned: %v", err)
	}
	current, err := configure.ReadSubscriptionSnapshot(0)
	if err != nil || string(current) != string(snapshot) {
		t.Fatalf("HTTP failure changed subscription: %s, %v", current, err)
	}
}

func TestSubscriptionSnapshotRejectsCorruptListWithoutPanic(t *testing.T) {
	resetNodeTestDB(t)
	if err := db.Set("touch", "subscriptions", map[string]string{"not": "a list"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotSubscriptionsForUpdate(); err == nil {
		t.Fatal("corrupt subscription list did not return an error")
	}
	if err := AutoSelectServersFromSubscriptions(false); err == nil {
		t.Fatal("auto selection did not reject corrupt subscription list")
	}
}
