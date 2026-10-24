package serverObj

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	jsoniter "github.com/json-iterator/go"
	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
)

const testVLESSEncryption = "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestVLESSEncryptionRoundTrip(t *testing.T) {
	for _, encryption := range []string{
		testVLESSEncryption,
		"mlkem768x25519plus.xorpub.1rtt.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		// Treat encryption as an opaque value, preserving URL-escaped characters.
		"future-format.+/=?&",
	} {
		t.Run(encryption, func(t *testing.T) {
			link := "vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=tcp&security=none&flow=xtls-rprx-vision&encryption=" + url.QueryEscape(encryption) + "#encrypted-node"
			node, err := ParseVlessURL(link)
			if err != nil {
				t.Fatal(err)
			}
			if node.Encryption != encryption || node.Security != "" {
				t.Fatalf("parsed encryption = %q, scy = %q", node.Encryption, node.Security)
			}

			stored, err := jsoniter.Marshal(node)
			if err != nil {
				t.Fatal(err)
			}
			var restored V2Ray
			if err := jsoniter.Unmarshal(stored, &restored); err != nil {
				t.Fatal(err)
			}
			config, err := restored.Configuration(PriorInfo{Variant: where.Xray, CoreVersion: "26.7.28", Tag: "proxy"})
			if err != nil {
				t.Fatal(err)
			}
			user := config.CoreOutbound.Settings.Vnext.([]coreObj.Vnext)[0].Users[0]
			if user.Encryption != encryption || user.Security != "" || user.Flow != "xtls-rprx-vision" {
				t.Fatalf("generated user = %+v", user)
			}
			if config.CoreOutbound.StreamSettings.Security != "" {
				t.Fatal("VLESS encryption must not change transport security")
			}

			exported := restored.ExportToURL()
			u, err := url.Parse(exported)
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("encryption") != encryption || u.Query().Get("security") != "none" || u.Query().Get("flow") != "xtls-rprx-vision" {
				t.Fatalf("exported parameters = %v", u.Query())
			}
			roundTrip, err := ParseVlessURL(exported)
			if err != nil {
				t.Fatal(err)
			}
			if roundTrip.Encryption != encryption || roundTrip.ID != node.ID || roundTrip.Ps != node.Ps {
				t.Fatalf("round trip changed node: %+v", roundTrip)
			}
		})
	}
}

func TestVLESSEncryptionDefaults(t *testing.T) {
	for _, query := range []string{"", "&encryption=", "&encryption=none"} {
		t.Run(query, func(t *testing.T) {
			node, err := ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@example.com:443?type=tcp" + query)
			if err != nil {
				t.Fatal(err)
			}
			// A VMess cipher must never become VLESS encryption.
			node.Security = "aes-128-gcm"
			config, err := node.Configuration(PriorInfo{Variant: where.V2ray})
			if err != nil {
				t.Fatal(err)
			}
			user := config.CoreOutbound.Settings.Vnext.([]coreObj.Vnext)[0].Users[0]
			if node.Encryption != "none" || user.Encryption != "none" || user.Security != "" {
				t.Fatalf("default encryption: node = %q, user = %+v", node.Encryption, user)
			}
			u, err := url.Parse(node.ExportToURL())
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("encryption") != "none" {
				t.Fatalf("exported default encryption = %q", u.Query().Get("encryption"))
			}
		})
	}
}

func TestVLESSLegacyEncryption(t *testing.T) {
	for _, tt := range []struct {
		name       string
		encryption string
		original   string
		want       string
	}{
		{"recover original", "", "vless://id@example.com:443?encryption=" + testVLESSEncryption, testVLESSEncryption},
		{"explicit none overrides original", "none", "vless://id@example.com:443?encryption=" + testVLESSEncryption, "none"},
		{"edited value overrides original", testVLESSEncryption, "vless://id@example.com:443?encryption=old-value", testVLESSEncryption},
		{"missing original", "", "", "none"},
		{"malformed original", "", "vless://[invalid", "none"},
		{"non vless original", "", "vmess://id@example.com:443?encryption=wrong-protocol", "none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node := &V2Ray{Protocol: "vless", Add: "example.com", Port: "443", ID: "11111111-1111-4111-8111-111111111111", Net: "tcp", V: tt.original, Encryption: tt.encryption}
			config, err := node.Configuration(PriorInfo{Variant: where.Xray})
			if err != nil {
				t.Fatal(err)
			}
			got := config.CoreOutbound.Settings.Vnext.([]coreObj.Vnext)[0].Users[0].Encryption
			if got != tt.want {
				t.Fatalf("configuration encryption = %q, want %q", got, tt.want)
			}
			u, err := url.Parse(node.ExportToURL())
			if err != nil {
				t.Fatal(err)
			}
			if got := u.Query().Get("encryption"); got != tt.want {
				t.Fatalf("export encryption = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVLESSEncryptionCoreCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name    string
		variant where.Variant
		version string
		wantErr bool
	}{
		{"v2fly", where.V2ray, "5.41.0", true},
		{"unknown core", where.Unknown, "", true},
		{"unspecified core", "", "", true},
		{"old xray", where.Xray, "1.8.24", true},
		{"before introduction", where.Xray, "25.8.3", true},
		{"first supported release", where.Xray, "25.8.29", false},
		{"version prefix", where.Xray, "v25.8.29", false},
		{"current xray", where.Xray, "26.7.28", false},
		{"unknown xray version", where.Xray, "", false},
		{"custom xray version", where.Xray, "custom", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node := &V2Ray{Protocol: "vless", Net: "tcp", Encryption: testVLESSEncryption}
			_, err := node.Configuration(PriorInfo{Variant: tt.variant, CoreVersion: tt.version})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Configuration() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && (!strings.Contains(err.Error(), "VLESS Encryption") || !strings.Contains(err.Error(), "Xray") || strings.Contains(err.Error(), testVLESSEncryption)) {
				t.Fatalf("compatibility error must be actionable without including encryption credentials: %v", err)
			}
		})
	}
}

func TestVMessSecurityRoundTrip(t *testing.T) {
	raw := `{"v":"2","ps":"vmess-node","add":"example.com","port":"443","id":"11111111-1111-4111-8111-111111111111","aid":"0","scy":"aes-128-gcm","net":"tcp","tls":"tls"}`
	node, err := ParseVmessURL("vmess://" + base64.StdEncoding.EncodeToString([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if node.Security != "aes-128-gcm" || node.Encryption != "" {
		t.Fatalf("VMess security = %q, encryption = %q", node.Security, node.Encryption)
	}
	exported, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.TrimPrefix(node.ExportToURL(), "vmess://"), "="))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(exported, "scy").String() != "aes-128-gcm" || gjson.GetBytes(exported, "encryption").Exists() {
		t.Fatalf("VMess export changed security fields: %s", exported)
	}
}
