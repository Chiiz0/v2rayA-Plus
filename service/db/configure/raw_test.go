package configure

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/v2rayA/v2rayA/core/serverObj"
)

const persistedVlessEncryption = "mlkem768x25519plus.native.0rtt.test+public/key="

func persistedVlessLink(encryption string) string {
	u := url.URL{
		Scheme:   "vless",
		User:     url.User("11111111-1111-4111-8111-111111111111"),
		Host:     "203.0.113.10:24443",
		Fragment: "stored VLESS",
	}
	query := url.Values{"security": {"none"}, "type": {"tcp"}}
	if encryption != "" {
		query.Set("encryption", encryption)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func marshalRaw(t *testing.T, value interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertVlessRawEncryption(t *testing.T, raw ServerRaw, want string) {
	t.Helper()
	v, ok := raw.ServerObj.(*serverObj.V2Ray)
	if !ok {
		t.Fatalf("ServerObj has type %T, want *serverObj.V2Ray", raw.ServerObj)
	}
	if v.Encryption != want {
		t.Errorf("stored encryption = %q, want %q", v.Encryption, want)
	}
	shared, err := url.Parse(v.ExportToURL())
	if err != nil {
		t.Fatal(err)
	}
	if got := shared.Query().Get("encryption"); got != want {
		t.Errorf("shared encryption = %q, want %q", got, want)
	}
}

func TestVlessServerRawRoundTrip(t *testing.T) {
	v, err := serverObj.ParseVlessURL(persistedVlessLink(persistedVlessEncryption))
	if err != nil {
		t.Fatal(err)
	}
	raw := &ServerRaw{ServerObj: v, Latency: "188ms"}
	loaded, err := Bytes2ServerRaw(marshalRaw(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	assertVlessRawEncryption(t, *loaded, persistedVlessEncryption)
	if loaded.Latency != raw.Latency || loaded.ServerObj.GetName() != v.Ps {
		t.Fatal("server metadata changed after loading")
	}
}

func TestVlessSubscriptionRawRoundTrip(t *testing.T) {
	raw := &SubscriptionRaw{Address: "https://example.com/subscription", Remarks: "VLESS", AutoSelect: true}
	want := []string{persistedVlessEncryption, "none"}
	for _, encryption := range want {
		v, err := serverObj.ParseVlessURL(persistedVlessLink(encryption))
		if err != nil {
			t.Fatal(err)
		}
		raw.Servers = append(raw.Servers, ServerRaw{ServerObj: v})
	}
	loaded, err := Bytes2SubscriptionRaw(marshalRaw(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Servers) != len(want) {
		t.Fatalf("loaded %d servers, want %d", len(loaded.Servers), len(want))
	}
	for i, encryption := range want {
		assertVlessRawEncryption(t, loaded.Servers[i], encryption)
	}
	if loaded.Address != raw.Address || loaded.Remarks != raw.Remarks || loaded.AutoSelect != raw.AutoSelect {
		t.Fatal("subscription metadata changed after loading")
	}
}

func TestVlessRawLegacyEncryption(t *testing.T) {
	tests := []struct {
		name       string
		original   string
		encryption *string
		want       string
	}{
		{name: "recover original URI", original: persistedVlessEncryption, want: persistedVlessEncryption},
		{name: "missing original encryption", want: "none"},
		{name: "explicit none overrides original URI", original: persistedVlessEncryption, encryption: stringPointer("none"), want: "none"},
		{name: "explicit value overrides original URI", original: persistedVlessEncryption, encryption: stringPointer("updated-encryption"), want: "updated-encryption"},
		{name: "explicit empty clears original URI", original: persistedVlessEncryption, encryption: stringPointer(""), want: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := map[string]interface{}{
				"protocol": "vless", "v": persistedVlessLink(tt.original),
				"add": "203.0.113.10", "port": "24443",
				"id":  "11111111-1111-4111-8111-111111111111",
				"net": "tcp", "tls": "none", "ps": "stored VLESS",
			}
			if tt.encryption != nil {
				obj["encryption"] = *tt.encryption
			}
			server := map[string]interface{}{"serverObj": obj}
			t.Run("server", func(t *testing.T) {
				loaded, err := Bytes2ServerRaw(marshalRaw(t, server))
				if err != nil {
					t.Fatal(err)
				}
				assertVlessRawEncryption(t, *loaded, tt.want)
				loadedAgain, err := Bytes2ServerRaw(marshalRaw(t, loaded))
				if err != nil {
					t.Fatal(err)
				}
				assertVlessRawEncryption(t, *loadedAgain, tt.want)
			})
			t.Run("subscription", func(t *testing.T) {
				subscription := map[string]interface{}{"servers": []interface{}{server}}
				loaded, err := Bytes2SubscriptionRaw(marshalRaw(t, subscription))
				if err != nil {
					t.Fatal(err)
				}
				assertVlessRawEncryption(t, loaded.Servers[0], tt.want)
				loadedAgain, err := Bytes2SubscriptionRaw(marshalRaw(t, loaded))
				if err != nil {
					t.Fatal(err)
				}
				assertVlessRawEncryption(t, loadedAgain.Servers[0], tt.want)
			})
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
