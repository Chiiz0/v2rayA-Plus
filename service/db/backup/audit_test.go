package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

type auditFixture map[string]map[string][]byte

func auditGob(t *testing.T, value interface{}) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := gob.NewEncoder(&buffer).Encode(value); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func auditNames(t *testing.T, names ...string) []byte {
	t.Helper()
	set := map[[32]byte]interface{}{}
	for _, name := range names {
		set[sha256.Sum256(auditGob(t, name))] = name
	}
	return auditGob(t, set)
}

func auditArchive(t *testing.T, fixture auditFixture) *Archive {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	database, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		for name, values := range fixture {
			bucket, err := tx.CreateBucket([]byte(name))
			if err != nil {
				return err
			}
			for key, value := range values {
				if err := bucket.Put([]byte(key), value); err != nil {
					return err
				}
			}
		}
		return nil
	})
	closeErr := database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	archive, err := Export(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func auditValidFixture(t *testing.T) auditFixture {
	t.Helper()
	node := `{"serverObj":{"protocol":"vless","id":"DO_NOT_PRINT_NODE_SECRET","encryption":"DO_NOT_PRINT_ENCRYPTION_SECRET","net":"xhttp","xhttpExtra":{"future":9007199254740993,"enabled":false,"xmux":{"hKeepAlivePeriod":-1}}},"latency":"12ms"}`
	return auditFixture{
		"system": {
			"setting": []byte(`{"ipforward":false,"mux":0}`), "ports": []byte(`{"socks5":20170}`),
			"internalDnsList": []byte("null"), "externalDnsList": []byte(`"dns.example"`), "running": []byte("false"),
			"future": []byte("DO_NOT_PRINT_UNKNOWN_SECRET"),
		},
		"touch": {
			"servers":       []byte("[" + node + "," + node + "]"),
			"subscriptions": []byte(`[{"address":"https://example.com/DO_NOT_PRINT_SUBSCRIPTION_SECRET","servers":[` + node + "," + node + `]},{"address":"https://example.net/sub","servers":[` + node + "]}]"),
		},
		"accounts":        {"用户": []byte(`"DO_NOT_PRINT_ACCOUNT_HASH"`)},
		"outbounds":       {"names": auditNames(t, "a", "中")},
		"outbound.proxy":  {"connectedServers": []byte(`{"touches":[{"_type":"server","id":1,"outbound":"proxy"}]}`)},
		"outbound.a":      {"connectedServers": []byte(`{"touches":[{"_type":"subscriptionServer","sub":0,"id":2,"outbound":"a"}]}`)},
		"outbound.中":      {"connectedServers": []byte(`{"touches":[{"_type":"subscriptionServer","sub":1,"id":1,"outbound":"中"}]}`)},
		"outbound.orphan": {"connectedServers": []byte(`{"touches":[{"_type":"server","id":2,"outbound":"orphan"}]}`)},
		"dockerProxies": {
			"23101": []byte(`{"port":23101,"which":{"_type":"subscriptionServer","sub":0,"id":2},"frontWhich":{"_type":"server","id":1},"containerName":"v2raya-socks-23101","core":"xray","status":"running"}`),
			"23102": []byte(`{"port":23102,"which":{"_type":"subscriptionServer","sub":1,"id":1},"containerName":"v2raya-socks-23102","core":"v2fly","status":"paused"}`),
		},
		string([]byte{0xff, 0x00}): {string([]byte{0xfe}): []byte("opaque extension")},
	}
}

func auditHasIssue(report Report, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestAuditCompleteDatabasePreservesRawDataAndSecrets(t *testing.T) {
	archive := auditArchive(t, auditValidFixture(t))
	before, _ := json.Marshal(archive)
	report := Audit(archive)
	if report.HasErrors() {
		t.Fatalf("valid fixture failed: %+v", report.Issues)
	}
	counts := report.Counts
	if counts.Accounts != 1 || counts.Servers != 2 || counts.Subscriptions != 2 || counts.SubscriptionServers != 3 || counts.Outbounds != 3 || counts.MainConnections != 4 || counts.DockerProxies != 2 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
	if counts.Buckets != archive.Manifest.Buckets || counts.Values != archive.Manifest.Values {
		t.Fatalf("archive counts disagree: %+v", counts)
	}
	if !auditHasIssue(report, "outbound_orphan") || !auditHasIssue(report, "unknown_preserved") || len(report.Preserved) != 2 {
		t.Fatalf("unknown data or orphan outbound was not reported: %+v", report)
	}
	if len(report.Docker) != 2 || report.Docker[0].Port != 23101 || report.Docker[0].VolumeName != "v2raya-socks-23101" || report.Docker[1].Status != "paused" {
		t.Fatalf("Docker recovery metadata is incorrect: %+v", report.Docker)
	}
	output, _ := json.Marshal(report)
	if bytes.Contains(output, []byte("DO_NOT_PRINT")) || !bytes.Contains(output, []byte("base64:")) {
		t.Fatalf("report exposed a secret or failed to encode a binary location: %s", output)
	}
	after, _ := json.Marshal(archive)
	if !bytes.Equal(before, after) {
		t.Fatal("audit changed the archive")
	}
	if !strings.Contains(report.Scope, "not verified") {
		t.Fatal("report must describe the limits of structural auditing")
	}
}

func TestAuditRejectsDamagedStructuresAndReferences(t *testing.T) {
	tests := []struct {
		name, bucket, key, raw, code string
	}{
		{"invalid system JSON", "system", "setting", `{`, "system_json_invalid"},
		{"wrong system type", "system", "running", `"false"`, "system_json_invalid"},
		{"invalid account", "accounts", "用户", `DO_NOT_PRINT_BROKEN_HASH`, "account_value_invalid"},
		{"wrong server list", "touch", "servers", `{}`, "touch_list_invalid"},
		{"empty server value", "touch", "servers", ``, "touch_list_invalid"},
		{"empty subscription value", "touch", "subscriptions", ``, "touch_list_invalid"},
		{"missing server object", "touch", "servers", `[{}]`, "server_object_invalid"},
		{"wrong server object", "touch", "servers", `[{"serverObj":[]}]`, "server_object_invalid"},
		{"invalid subscription", "touch", "subscriptions", `[null]`, "subscription_invalid"},
		{"wrong subscription nodes", "touch", "subscriptions", `[{"servers":{}}]`, "subscription_servers_invalid"},
		{"wrong connection set", "outbound.proxy", "connectedServers", `[]`, "connections_invalid"},
		{"missing server reference", "outbound.proxy", "connectedServers", `{"touches":[{"_type":"server","id":3}]}`, "reference_node_missing"},
		{"zero node index", "outbound.proxy", "connectedServers", `{"touches":[{"_type":"server","id":0}]}`, "reference_node_missing"},
		{"fractional node index", "outbound.proxy", "connectedServers", `{"touches":[{"_type":"server","id":1.5}]}`, "reference_invalid"},
		{"missing subscription reference", "outbound.proxy", "connectedServers", `{"touches":[{"_type":"subscriptionServer","sub":2,"id":1}]}`, "reference_subscription_missing"},
		{"negative subscription reference", "outbound.proxy", "connectedServers", `{"touches":[{"_type":"subscriptionServer","sub":-1,"id":1}]}`, "reference_subscription_missing"},
		{"subscription is not a node", "outbound.proxy", "connectedServers", `{"touches":[{"_type":"subscription","id":1}]}`, "reference_type_invalid"},
		{"mismatched outbound", "outbound.a", "connectedServers", `{"touches":[{"_type":"server","id":1,"outbound":"proxy"}]}`, "reference_outbound_mismatch"},
		{"invalid orphan reference", "outbound.orphan", "connectedServers", `{"touches":[{"_type":"server","id":99,"outbound":"orphan"}]}`, "reference_node_missing"},
		{"Docker key mismatch", "dockerProxies", "23101", `{"port":23103,"which":{"_type":"server","id":1}}`, "docker_port_mismatch"},
		{"Docker port outside range", "dockerProxies", "23101", `{"port":65536,"which":{"_type":"server","id":1}}`, "docker_port_mismatch"},
		{"Docker missing target", "dockerProxies", "23101", `{"port":23101}`, "reference_invalid"},
		{"Docker negative unused sub", "dockerProxies", "23101", `{"port":23101,"which":{"_type":"server","id":1,"sub":-1}}`, "docker_reference_invalid"},
		{"Docker missing front", "dockerProxies", "23101", `{"port":23101,"which":{"_type":"server","id":1},"frontWhich":{"_type":"server","id":9}}`, "reference_node_missing"},
		{"Docker same front", "dockerProxies", "23101", `{"port":23101,"which":{"_type":"server","id":1},"frontWhich":{"_type":"server","id":1}}`, "docker_self_front"},
		{"Docker container mismatch", "dockerProxies", "23101", `{"port":23101,"which":{"_type":"server","id":1},"containerName":"v2raya-socks-23102"}`, "docker_container_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := auditValidFixture(t)
			fixture[tt.bucket][tt.key] = []byte(tt.raw)
			report := Audit(auditArchive(t, fixture))
			if !report.HasErrors() || !auditHasIssue(report, tt.code) {
				t.Fatalf("missing %s: %+v", tt.code, report.Issues)
			}
			output, _ := json.Marshal(report)
			if bytes.Contains(output, []byte("DO_NOT_PRINT")) {
				t.Fatalf("diagnostic exposed a stored value: %s", output)
			}
		})
	}
}

func TestAuditUsesActualGobSetIncludingUnicodeAndEmpty(t *testing.T) {
	for _, names := range [][]string{nil, {"a"}, {"中", "é", "x"}} {
		fixture := auditFixture{"outbounds": {"names": auditNames(t, names...)}}
		report := Audit(auditArchive(t, fixture))
		if report.HasErrors() || report.Counts.Outbounds != len(names)+1 {
			t.Fatalf("valid gob set was rejected: %+v", report)
		}
	}
	for name, raw := range map[string][]byte{
		"invalid bytes":    []byte("DO_NOT_PRINT_INVALID_GOB"),
		"wrong set type":   auditGob(t, []string{"a"}),
		"wrong value type": auditGob(t, map[[32]byte]interface{}{{}: 1}),
		"wrong name hash":  auditGob(t, map[[32]byte]interface{}{{}: "a"}),
		"trailing data":    append(auditNames(t, "a"), byte(1)),
	} {
		t.Run(name, func(t *testing.T) {
			report := Audit(auditArchive(t, auditFixture{"outbounds": {"names": raw}}))
			if !report.HasErrors() {
				t.Fatalf("malformed gob set was accepted: %+v", report)
			}
		})
	}
}

func TestAuditAllowsLegacyNullListsAndEmptyConnections(t *testing.T) {
	fixture := auditFixture{
		"touch":          {"servers": []byte("null"), "subscriptions": []byte(`[{"servers":null}]`)},
		"outbounds":      {"names": auditNames(t)},
		"outbound.proxy": {"connectedServers": []byte("null")},
	}
	if report := Audit(auditArchive(t, fixture)); report.HasErrors() {
		t.Fatalf("legacy empty data was rejected: %+v", report.Issues)
	}
}

func TestAuditRejectsInvalidArchiveWithoutEchoingData(t *testing.T) {
	if report := Audit(nil); !report.HasErrors() || !auditHasIssue(report, "archive_invalid") {
		t.Fatal("nil archive was accepted")
	}
	archive := auditArchive(t, auditValidFixture(t))
	archive.Buckets[0].Values[0].Data = []byte("DO_NOT_PRINT_TAMPERED_SECRET")
	report := Audit(archive)
	output, _ := json.Marshal(report)
	if !report.HasErrors() || !auditHasIssue(report, "archive_invalid") || bytes.Contains(output, []byte("DO_NOT_PRINT")) {
		t.Fatalf("invalid archive report is unsafe: %s", output)
	}
}

func TestAuditEncodesBinaryKeyLocations(t *testing.T) {
	fixture := auditFixture{"accounts": {string([]byte{0xff}): []byte("invalid hash")}}
	report := Audit(auditArchive(t, fixture))
	if len(report.Issues) != 1 || report.Issues[0].Location != "/accounts/base64:_w" {
		t.Fatalf("binary key location was not safely encoded: %+v", report.Issues)
	}
}

func TestAuditKnownValueKeysCannotBeNestedBuckets(t *testing.T) {
	for _, tt := range []struct {
		bucket, key string
		wantError   bool
	}{
		{"system", "setting", true},
		{"system", "running", true},
		{"touch", "servers", true},
		{"touch", "subscriptions", true},
		{"outbounds", "names", true},
		{"outbound.proxy", "connectedServers", true},
		{"outbound.proxy", "setting", true},
		{"accounts", "user", true},
		{"dockerProxies", "23101", true},
		{"touch", "future", false},
		{"system", "future", false},
		{"outbound.proxy", "future", false},
		{"unknown", "servers", false},
	} {
		t.Run(tt.bucket+"/"+tt.key, func(t *testing.T) {
			archive := auditArchive(t, auditFixture{tt.bucket: {}})
			archive.Buckets[0].Buckets = []Bucket{{Name: []byte(tt.key), Values: []Value{{Key: []byte("private"), Data: []byte("DO_NOT_PRINT_NESTED_SECRET"), SHA256: digest([]byte("DO_NOT_PRINT_NESTED_SECRET"))}}}}
			var err error
			archive.Manifest, err = manifestFor(archive.Buckets)
			if err != nil {
				t.Fatal(err)
			}
			report := Audit(archive)
			if report.HasErrors() != tt.wantError || auditHasIssue(report, "known_value_is_bucket") != tt.wantError {
				t.Fatalf("nested bucket audit = %+v, want error=%t", report.Issues, tt.wantError)
			}
			output, _ := json.Marshal(report)
			if bytes.Contains(output, []byte("DO_NOT_PRINT")) {
				t.Fatal("nested raw value was exposed")
			}
		})
	}
}
