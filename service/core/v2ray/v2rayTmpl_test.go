package v2ray

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func TestInsertMappingOutboundVLESSEncryptionForHTTPLatency(t *testing.T) {
	const encryption = "mlkem768x25519plus.native.0rtt.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, tt := range []struct {
		name       string
		variant    where.Variant
		version    string
		encryption string
		legacy     bool
		wantErr    string
	}{
		{"xray preserves encryption", where.Xray, "26.7.28", encryption, false, ""},
		{"xray recovers legacy URI", where.Xray, "26.7.28", encryption, true, ""},
		{"v2fly rejects encryption", where.V2ray, "5.41.0", encryption, false, "requires Xray"},
		{"v2fly rejects legacy encryption", where.V2ray, "5.41.0", encryption, true, "requires Xray"},
		{"old xray rejects encryption", where.Xray, "25.8.3", encryption, false, "25.8.29 or newer"},
		{"first supported xray release", where.Xray, "25.8.29", encryption, false, ""},
		{"v2fly accepts unencrypted vless", where.V2ray, "5.41.0", "none", false, ""},
		{"old xray accepts default encryption", where.Xray, "1.8.24", "", false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			link := "vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=tcp&security=none&encryption=" + url.QueryEscape(tt.encryption)
			node, err := serverObj.ParseVlessURL(link)
			if err != nil {
				t.Fatal(err)
			}
			if tt.legacy {
				node.Encryption = ""
			}

			// TestHttpLatency inserts its temporary SOCKS mapping through this
			// method. Set the detected core directly to avoid starting a service
			// or consulting the user's installed core and database in this test.
			tmpl := &Template{
				Variant:     tt.variant,
				CoreVersion: tt.version,
				Setting:     &configure.Setting{Transparent: configure.TransparentClose},
			}
			err = tmpl.InsertMappingOutbound(node, "11080", false, 0, "socks")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("InsertMappingOutbound() error = %v, want %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "unsupported") {
					t.Fatalf("TestHttpLatency would hide this error as UNSUPPORTED PROTOCOL: %v", err)
				}
				if len(tmpl.Inbounds) != 0 || len(tmpl.Outbounds) != 0 {
					t.Fatal("unsupported core must fail before adding a temporary mapping")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			config := tmpl.ToConfigBytes()
			wantEncryption := tt.encryption
			if wantEncryption == "" {
				wantEncryption = "none"
			}
			if got := gjson.GetBytes(config, "outbounds.0.settings.vnext.0.users.0.encryption").String(); got != wantEncryption {
				t.Fatalf("HTTP test outbound encryption = %q, want %q", got, wantEncryption)
			}
			if got := gjson.GetBytes(config, "inbounds.0.port").Int(); got != 11080 {
				t.Fatalf("HTTP test SOCKS port = %d, want 11080", got)
			}
			if got := gjson.GetBytes(config, "routing.rules.0.outboundTag").String(); got != "outbound11080" {
				t.Fatalf("HTTP test route targets %q, want outbound11080", got)
			}
		})
	}
}
