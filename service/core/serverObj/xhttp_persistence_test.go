package serverObj_test

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/db/configure"
	"go.etcd.io/bbolt"
)

// Exercise the production list codec and a real close/reopen of Bolt without
// touching db.DB(), which may point at the user's running configuration.
func persistXHTTPFixture(t *testing.T, value interface{}) []byte {
	t.Helper()
	list, err := sjson.SetBytes([]byte("[]"), "-1", value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "xhttp.db")
	database, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("configuration"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("nodes"), list)
	})
	closeErr := database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	database, err = bbolt.Open(path, 0600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var raw []byte
	err = database.View(func(tx *bbolt.Tx) error {
		list := tx.Bucket([]byte("configuration")).Get([]byte("nodes"))
		raw = []byte(gjson.GetBytes(list, "0").Raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestXHTTPBoltServerAndSubscriptionRoundTrip(t *testing.T) {
	const extra = `{"future":9007199254740993,"noGRPCHeader":false,"scMinPostsIntervalMs":0,"xmux":{"hKeepAlivePeriod":-1}}`
	const encryption = "mlkem768x25519plus.native.0rtt.test+public/key="
	link := "vless://11111111-1111-4111-8111-111111111111@example.com:443?type=xhttp&security=none&mode=packet-up&extra=" + url.QueryEscape(extra) + "&encryption=" + url.QueryEscape(encryption)
	parsed, err := serverObj.ParseVlessURL(link)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"canonical", "legacy URI", "explicit empty object"} {
		t.Run(kind, func(t *testing.T) {
			stored, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(stored, &fields); err != nil {
				t.Fatal(err)
			}
			if kind == "legacy URI" {
				delete(fields, "xhttpExtra")
				delete(fields, "encryption")
				fields["xhttpMode"] = json.RawMessage(`"auto"`)
			} else if kind == "explicit empty object" {
				fields["xhttpExtra"] = json.RawMessage(`{}`)
			}
			server := map[string]interface{}{"serverObj": fields, "latency": "123ms"}
			for _, subscription := range []bool{false, true} {
				name := "server"
				var value interface{} = server
				if subscription {
					name = "subscription"
					value = map[string]interface{}{"address": "https://example.com/subscription", "servers": []interface{}{server}}
				}
				t.Run(name, func(t *testing.T) {
					// The second write checks that clearing or recovering the
					// canonical object remains stable after normal persistence.
					for round := 0; round < 2; round++ {
						raw := persistXHTTPFixture(t, value)
						var loaded configure.ServerRaw
						if subscription {
							sub, err := configure.Bytes2SubscriptionRaw(raw)
							if err != nil {
								t.Fatal(err)
							}
							if len(sub.Servers) != 1 {
								t.Fatalf("loaded %d subscription nodes", len(sub.Servers))
							}
							loaded, value = sub.Servers[0], sub
						} else {
							node, err := configure.Bytes2ServerRaw(raw)
							if err != nil {
								t.Fatal(err)
							}
							loaded, value = *node, node
						}
						node := loaded.ServerObj.(*serverObj.V2Ray)
						if loaded.Latency != "123ms" || node.Encryption != encryption || node.XHTTPMode != "packet-up" {
							t.Fatalf("stored metadata or protocol fields changed: %+v", node)
						}
						shared, err := serverObj.ParseVlessURL(node.ExportToURL())
						if err != nil {
							t.Fatal(err)
						}
						for _, got := range []json.RawMessage{node.XHTTPExtra, shared.XHTTPExtra} {
							if kind == "explicit empty object" {
								if strings.TrimSpace(string(got)) != "{}" {
									t.Fatalf("cleared extra resurrected original URI: %s", got)
								}
							} else {
								for _, token := range []string{`"future":9007199254740993`, `"noGRPCHeader":false`, `"scMinPostsIntervalMs":0`, `"hKeepAlivePeriod":-1`} {
									if !strings.Contains(string(got), token) {
										t.Fatalf("stored extra lost %s: %s", token, got)
									}
								}
							}
						}
					}
				})
			}
		})
	}
}
