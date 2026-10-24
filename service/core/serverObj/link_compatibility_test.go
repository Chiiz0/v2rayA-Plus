package serverObj

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
)

func TestParseVmessNumericFields(t *testing.T) {
	for _, fields := range []string{
		`"port":443,"aid":0,"v":2`,
		`"port":"443","aid":0,"v":"2"`,
		`"port":443,"aid":"0","v":2`,
		`"port":4.43e2,"aid":0.0,"v":2.0`,
	} {
		t.Run(fields, func(t *testing.T) {
			raw := fmt.Sprintf(`{%s,"ps":"numeric-node","add":"example.com","id":"11111111-1111-4111-8111-111111111111","scy":"aes-128-gcm","net":"raw"}`, fields)
			node, err := ParseVmessURL("vmess://" + base64.RawStdEncoding.EncodeToString([]byte(raw)))
			if err != nil {
				t.Fatal(err)
			}
			if node.Port != "443" || node.Aid != "0" || node.V != "2" || node.GetPort() != 443 {
				t.Fatalf("numeric fields were not normalized: port=%q aid=%q v=%q", node.Port, node.Aid, node.V)
			}
			if node.Net != "raw" || node.Security != "aes-128-gcm" || node.Encryption != "" {
				t.Fatalf("unrelated VMess fields changed: %+v", node)
			}
			roundTrip, err := ParseVmessURL(node.ExportToURL())
			if err != nil {
				t.Fatal(err)
			}
			if roundTrip.Port != node.Port || roundTrip.Aid != node.Aid || roundTrip.V != node.V {
				t.Fatal("VMess share round trip changed numeric fields")
			}
		})
	}
}

func TestParseVmessDoesNotTruncateFractionalFields(t *testing.T) {
	for _, key := range []string{"port", "aid", "v"} {
		t.Run(key, func(t *testing.T) {
			raw := fmt.Sprintf(`{"%s":1.5}`, key)
			_, err := ParseVmessURL("vmess://" + base64.RawStdEncoding.EncodeToString([]byte(raw)))
			if err == nil || !strings.Contains(err.Error(), "expected an integer") {
				t.Fatalf("fractional %s must not be silently truncated: %v", key, err)
			}
		})
	}
}

func TestVLESSRawTransportRoundTrip(t *testing.T) {
	for _, network := range []string{"raw", "RAW"} {
		for _, variant := range []where.Variant{where.Xray, where.V2ray} {
			t.Run(network+"/"+string(variant), func(t *testing.T) {
				encryption := "none"
				if variant == where.Xray {
					encryption = testVLESSEncryption
				}
				link := "vless://11111111-1111-4111-8111-111111111111@example.com:443?type=" + network +
					"&security=none&headerType=http&host=front.example.com&path=%2Frequest&encryption=" + url.QueryEscape(encryption)
				node, err := ParseVlessURL(link)
				if err != nil {
					t.Fatal(err)
				}
				// Sharing an imported node must work even before configuration.
				exportedBefore := node.ExportToURL()
				config, err := node.Configuration(PriorInfo{Variant: variant, CoreVersion: "26.7.28"})
				if err != nil {
					t.Fatal(err)
				}
				stream := config.CoreOutbound.StreamSettings
				if stream.Network != "tcp" || stream.TCPSettings == nil || stream.TCPSettings.Header.Type != "http" {
					t.Fatalf("RAW must configure TCP including its HTTP header: %+v", stream)
				}
				if stream.TCPSettings.Header.Request.Headers.Host[0] != node.Host || stream.TCPSettings.Header.Request.Path[0] != node.Path {
					t.Fatal("RAW lost its TCP HTTP host/path settings")
				}
				if got := config.CoreOutbound.Settings.Vnext.([]coreObj.Vnext)[0].Users[0].Encryption; got != encryption {
					t.Fatalf("RAW encryption = %q, want %q", got, encryption)
				}
				if node.Net != network || node.ExportToURL() != exportedBefore {
					t.Fatal("generating a compatible core config must not mutate the imported link")
				}
				roundTrip, err := ParseVlessURL(exportedBefore)
				if err != nil {
					t.Fatal(err)
				}
				if roundTrip.Net != network || roundTrip.Type != "http" || roundTrip.Host != node.Host || roundTrip.Path != node.Path || roundTrip.Encryption != encryption {
					t.Fatalf("RAW share round trip lost fields: %+v", roundTrip)
				}
			})
		}
	}
}

func TestSSPluginWithoutOptions(t *testing.T) {
	for _, tt := range []struct {
		plugin string
		name   string
	}{
		{"v2ray-plugin", "v2ray-plugin"},
		{"obfs-local", "simple-obfs"},
		{"simpleobfs", "simple-obfs"},
		{"v2ray-plugin;", "v2ray-plugin"},
	} {
		t.Run(tt.plugin, func(t *testing.T) {
			link := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:password")) +
				"@example.com:443?plugin=" + url.QueryEscape(tt.plugin)
			node, err := ParseSSURL(link)
			if err != nil {
				t.Fatal(err)
			}
			if node.Plugin.Name != tt.name || node.Plugin.Opts != (Sip003Opts{}) {
				t.Fatalf("plugin name without options = %+v", node.Plugin)
			}
		})
	}
	plugin := ParseSip003("v2ray-plugin;mode=websocket;tls;host=front.example.com")
	if plugin.Name != "v2ray-plugin" || plugin.Opts.Obfs != "websocket" || plugin.Opts.Tls != "tls" || plugin.Opts.Host != "front.example.com" {
		t.Fatalf("existing plugin options changed: %+v", plugin)
	}
}

func TestParseSSTruncatedLinks(t *testing.T) {
	for _, link := range []string{"", "ss:", "ss:x", "ss:/", "ss://", "ss://@", "not-a-share-link"} {
		t.Run(link, func(t *testing.T) {
			if node, err := ParseSSURL(link); err == nil || node != nil {
				t.Fatalf("invalid SS link %q: node=%+v err=%v", link, node, err)
			}
		})
	}
}
