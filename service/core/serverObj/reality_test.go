package serverObj

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
)

func TestRealityMLDSA65RoundTrip(t *testing.T) {
	// Keys are opaque here: the selected core validates their encoding/length.
	key := strings.Repeat("verify_-", 450) + "+/="
	link := "vless://11111111-1111-4111-8111-111111111111@example.com:443?type=raw&security=reality&sni=example.com&fp=chrome&pbk=public&sid=ab&pqv=" + url.QueryEscape(key)
	node, err := ParseVlessURL(link)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		stored, err := json.Marshal(node)
		if err != nil {
			t.Fatal(err)
		}
		var loaded V2Ray
		if err := json.Unmarshal(stored, &loaded); err != nil {
			t.Fatal(err)
		}
		config, err := loaded.Configuration(PriorInfo{Variant: where.Xray, CoreVersion: "26.10.31"})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(config.CoreOutbound)
		if err != nil {
			t.Fatal(err)
		}
		if got := gjson.GetBytes(encoded, "streamSettings.realitySettings.mldsa65Verify").String(); got != key {
			t.Fatal("generated configuration lost the verification key")
		}
		node, err = ParseVlessURL(loaded.ExportToURL())
		if err != nil || node.Mldsa65Verify != key {
			t.Fatalf("share link lost the verification key: %v", err)
		}
	}
}

func TestRealityMLDSA65LegacyAndClear(t *testing.T) {
	for _, tc := range []struct {
		name, field, original, want string
	}{
		{"legacy URI", "", "vless://id@example.com:443?pqv=original", "original"},
		{"edited key", `,"pqv":"edited"`, "vless://id@example.com:443?pqv=original", "edited"},
		{"cleared key", `,"pqv":""`, "vless://id@example.com:443?pqv=original", ""},
		{"old node without key", "", "vless://id@example.com:443", ""},
		{"other protocol", "", "vmess://id@example.com:443?pqv=wrong", ""},
		{"malformed URI", "", "vless://[invalid", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, _ := json.Marshal(tc.original)
			raw := []byte(`{"protocol":"vless","tls":"reality","net":"tcp","v":` + string(original) + tc.field + `}`)
			for round := 0; round < 2; round++ {
				var node V2Ray
				if err := json.Unmarshal(raw, &node); err != nil {
					t.Fatal(err)
				}
				if node.Mldsa65Verify != tc.want {
					t.Fatalf("round %d key = %q, want %q", round, node.Mldsa65Verify, tc.want)
				}
				var err error
				raw, err = json.Marshal(node)
				if err != nil {
					t.Fatal(err)
				}
				if !gjson.GetBytes(raw, "pqv").Exists() {
					t.Fatal("canonical key must remain present, including when cleared")
				}
			}
		})
	}
}

func TestRealityMLDSA65CoreCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		variant       where.Variant
		wantErr       bool
	}{
		{"V2Fly", "5.41.0", where.V2ray, true},
		{"old Xray", "25.6.8", where.Xray, true},
		{"first supported Xray", "v25.7.26", where.Xray, false},
		{"current Xray", "26.10.31", where.Xray, false},
		{"Docker image checked later", "", where.Xray, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, err := ParseVlessURL("vless://id@example.com:443?security=reality&pqv=VERIFY")
			if err != nil {
				t.Fatal(err)
			}
			_, err = node.Configuration(PriorInfo{Variant: tc.variant, CoreVersion: tc.version})
			if (err != nil) != tc.wantErr {
				t.Fatalf("configuration error = %v, want error %t", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "REALITY ML-DSA-65") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
