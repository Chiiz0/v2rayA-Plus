package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/v2rayA/v2rayA/db/configure"
)

func TestReplaceOutboundConnectionsRestoresAfterCoreFailure(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, recoveryFails := range []bool{false, true} {
			seedNodeOperationDB(t)
			if err := configure.AddOutbound("other"); err != nil {
				t.Fatal(err)
			}
			if err := configure.AddConnect(configure.Which{TYPE: configure.ServerType, ID: 3, Outbound: "other"}); err != nil {
				t.Fatal(err)
			}
			if !empty {
				if err := configure.AddConnect(configure.Which{TYPE: configure.ServerType, ID: 1, Outbound: "proxy"}); err != nil {
					t.Fatal(err)
				}
			}
			previous := configure.GetConnectedServersByOutbound("proxy")
			other := configure.GetConnectedServersByOutbound("other")
			failed := errors.New("reload failed")
			recovery := errors.New("recovery failed")
			calls := 0
			err := replaceOutboundConnectionsLocked("proxy", []*configure.Which{{TYPE: configure.ServerType, ID: 2}}, 1, func() error {
				calls++
				if calls == 1 {
					current := configure.GetConnectedServersByOutbound("proxy")
					if current.Len() != 1 || current.Get()[0].ID != 2 {
						t.Fatal("reload did not see replacement")
					}
					return failed
				}
				if recoveryFails {
					return recovery
				}
				return nil
			})
			if calls != 2 || !errors.Is(err, failed) || recoveryFails && !errors.Is(err, recovery) {
				t.Fatalf("missing reload/recovery failure: calls=%d error=%v", calls, err)
			}
			got := configure.GetConnectedServersByOutbound("proxy")
			if got.Len() != previous.Len() || !empty && !reflect.DeepEqual(got.Get(), previous.Get()) {
				t.Fatalf("did not restore prior selection (empty=%v): %+v", empty, got)
			}
			if !reflect.DeepEqual(other, configure.GetConnectedServersByOutbound("other")) {
				t.Fatal("rollback changed another group")
			}
		}
	}
}

func TestReplaceOutboundConnectionsDoesNotReloadRejectedRequest(t *testing.T) {
	seedNodeOperationDB(t)
	err := replaceOutboundConnectionsLocked("proxy", []*configure.Which{{TYPE: configure.ServerType, ID: 1}, {TYPE: configure.ServerType, ID: 2}}, 1, func() error {
		t.Fatal("rejected request reloaded the core")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "at most 1") || configure.GetConnectedServersByOutbound("proxy").Len() != 0 {
		t.Fatalf("core restriction changed selection: %v", err)
	}
	if err := replaceOutboundConnectionsLocked("proxy", []*configure.Which{{TYPE: configure.ServerType, ID: 1}, {TYPE: configure.ServerType, ID: 1}}, 1, nil); err != nil {
		t.Fatalf("duplicate member should fit a single-member core: %v", err)
	}
}
