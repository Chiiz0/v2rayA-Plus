package service

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/db/store"
)

func TestSettingPatchPreservesOmittedAndUnknownFields(t *testing.T) {
	seedNodeOperationDB(t)
	const initial = `{"pacMode":"whitelist","ipforward":false,"portSharing":true,"mux":77,"unknown":{"integer":9007199254740993,"flag":false},"inboundSniffing":"http,tls"}`
	if err := db.Update(func(tx store.WriteTx) error { return tx.Put([]byte("system"), []byte("setting"), []byte(initial)) }); err != nil {
		t.Fatal(err)
	}
	if err := PatchSetting([]byte(`{"portSharing":false,"routeOnly":true,"unknown":{"integer":1},"dnsMode":"hijack"}`)); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := db.Get("system", "setting", &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"portSharing": "false", "routeOnly": "true", "ipforward": "false", "mux": "77", "unknown": `{"integer":9007199254740993,"flag":false}`, "inboundSniffing": `"http,tls"`} {
		if string(got[key]) != want {
			t.Errorf("%s = %s, want %s", key, got[key], want)
		}
	}
	if _, ok := got["dnsMode"]; ok {
		t.Fatal("unsupported field was added")
	}
}

func TestSettingPatchRejectsBeforeChangingState(t *testing.T) {
	seedNodeOperationDB(t)
	s := configure.NewSetting()
	s.IpForward = false
	if err := configure.SetSetting(s); err != nil {
		t.Fatal(err)
	}
	before := nodeOperationSnapshot(t)
	for _, input := range []string{`null`, `[]`, `{"portSharing":null}`, `{"muxOn":"yes","mux":0}`, `{"mux":"invalid"}`, `{"subscriptionAutoUpdateMode":"autoUpdateAtIntervals","subscriptionAutoUpdateIntervalHour":0}`} {
		// Use the backend enum verbatim for the timer validation case.
		if input == `{"subscriptionAutoUpdateMode":"autoUpdateAtIntervals","subscriptionAutoUpdateIntervalHour":0}` {
			b, _ := json.Marshal(map[string]any{"subscriptionAutoUpdateMode": configure.AutoUpdateAtIntervals, "subscriptionAutoUpdateIntervalHour": 0})
			input = string(b)
		}
		if err := PatchSetting([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
		if !reflect.DeepEqual(before, nodeOperationSnapshot(t)) {
			t.Fatalf("rejected patch modified state: %s", input)
		}
	}
}

func TestConcurrentSettingPatchesDoNotLoseFields(t *testing.T) {
	seedNodeOperationDB(t)
	s := configure.NewSetting()
	s.IpForward = false
	if err := configure.SetSetting(s); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, patch := range []string{`{"portSharing":true}`, `{"routeOnly":true}`, `{"mux":41}`} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			if err := PatchSetting([]byte(p)); err != nil {
				t.Error(err)
			}
		}(patch)
	}
	wg.Wait()
	got := GetSetting()
	if !got.PortSharing || !got.RouteOnly || got.Mux != 41 {
		t.Fatalf("lost concurrent field: %+v", got)
	}
}

func TestSettingPatchRestoresCoreAfterFailedReplacement(t *testing.T) {
	for _, recoveryFails := range []bool{false, true} {
		name := "recovery succeeds"
		if recoveryFails {
			name = "recovery also fails"
		}
		t.Run(name, func(t *testing.T) {
			seedNodeOperationDB(t)
			const initial = `{"pacMode":"whitelist","muxOn":"no","mux":0,"portSharing":false,"ipforward":false,"unknown":{"integer":9007199254740993,"flag":false}}`
			if err := db.Update(func(tx store.WriteTx) error {
				return tx.Put([]byte("system"), []byte("setting"), []byte(initial))
			}); err != nil {
				t.Fatal(err)
			}
			if err := configure.SetRunning(true); err != nil {
				t.Fatal(err)
			}
			before := nodeOperationSnapshot(t)
			launchErr := errors.New("replacement core failed to launch")
			recoveryErr := errors.New("original core failed to recover")
			calls := 0
			reload := func() error {
				calls++
				if calls == 1 {
					if !GetSetting().PortSharing {
						t.Fatal("replacement core did not read the new settings")
					}
					// CoreProcessManager.Start stops the previous process and
					// records it as stopped before attempting its replacement.
					if err := configure.SetRunning(false); err != nil {
						t.Fatal(err)
					}
					return launchErr
				}
				if calls != 2 || configure.GetRunning() {
					t.Fatalf("recovery must run exactly once while stopped; calls=%d", calls)
				}
				if got := nodeOperationSnapshot(t)["system/setting"]; got != initial {
					t.Fatalf("recovery did not read the exact original settings: %s", got)
				}
				if recoveryFails {
					return recoveryErr
				}
				return configure.SetRunning(true)
			}
			err := func() error {
				nodeOperationMu.Lock()
				defer nodeOperationMu.Unlock()
				return patchSettingLocked([]byte(`{"portSharing":true}`), reload)
			}()
			if !errors.Is(err, launchErr) || calls != 2 {
				t.Fatalf("launch failure or recovery lost: calls=%d error=%v", calls, err)
			}
			if recoveryFails {
				if !errors.Is(err, recoveryErr) || !strings.Contains(err.Error(), "core recovery failed") {
					t.Fatalf("both launch and recovery errors must be reported: %v", err)
				}
				if configure.GetRunning() {
					t.Fatal("failed recovery was reported as running")
				}
			} else if !reflect.DeepEqual(before, nodeOperationSnapshot(t)) {
				t.Fatal("successful recovery did not restore the original state")
			}
		})
	}
}
