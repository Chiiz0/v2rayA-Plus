package serverObj

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	jsoniter "github.com/json-iterator/go"
	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
)

const xhttpTestLink = "vless://11111111-1111-4111-8111-111111111111@example.com:443?type=xhttp&security=none"

func parseXHTTPTest(t *testing.T, query string) *V2Ray {
	t.Helper()
	v, err := ParseVlessURL(xhttpTestLink + query)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func assertXHTTPJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	// UseNumber canonicalizes nested key order without rounding large integers.
	var gotObject, wantObject interface{}
	gotDecoder := json.NewDecoder(bytes.NewReader(got))
	gotDecoder.UseNumber()
	if err := gotDecoder.Decode(&gotObject); err != nil {
		t.Fatal(err)
	}
	wantDecoder := json.NewDecoder(strings.NewReader(want))
	wantDecoder.UseNumber()
	if err := wantDecoder.Decode(&wantObject); err != nil {
		t.Fatal(err)
	}
	gotCanonical, _ := json.Marshal(gotObject)
	wantCanonical, _ := json.Marshal(wantObject)
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("extra = %s, want %s", gotCanonical, wantCanonical)
	}
}

func TestXHTTPExtraRoundTrip(t *testing.T) {
	extra := `{"noGRPCHeader":false,"scMinPostsIntervalMs":0,"xPaddingBytes":"100-200","xmux":{"hKeepAlivePeriod":-1,"maxConnections":1,"cMaxReuseTimes":0},"future":{"integer":9007199254740993,"off":false,"empty":{}},"downloadSettings":{"address":"down.example","port":443,"network":"xhttp","security":"tls","tlsSettings":{"serverName":"down.example"},"xhttpSettings":{"path":"/down","extra":{"headers":{"X-Test":"a&b=+%"},"xmux":{"hKeepAlivePeriod":-1}}}}}`
	v := parseXHTTPTest(t, "&mode=packet-up&path=/up&encryption="+url.QueryEscape(testVLESSEncryption)+"&extra="+url.QueryEscape(extra))
	for _, protocol := range []string{"vless", "vmess"} {
		t.Run(protocol, func(t *testing.T) {
			node := *v
			node.Protocol = protocol
			if protocol == "vmess" {
				node.Encryption = ""
				node.Security = "aes-128-gcm"
			}
			stored, err := json.Marshal(&node)
			if err != nil {
				t.Fatal(err)
			}
			var restored V2Ray
			if err := jsoniter.Unmarshal(stored, &restored); err != nil {
				t.Fatal(err)
			}
			assertXHTTPJSON(t, restored.XHTTPExtra, extra)
			roundTrip, err := NewV2Ray(restored.ExportToURL())
			if err != nil {
				t.Fatal(err)
			}
			got := roundTrip.(*V2Ray)
			assertXHTTPJSON(t, got.XHTTPExtra, extra)
			if got.XHTTPMode != "packet-up" || got.Encryption != node.Encryption || got.Security != node.Security {
				t.Fatalf("protocol/security fields changed: %+v", got)
			}
			cfg, err := got.Configuration(PriorInfo{Variant: where.Xray, CoreVersion: "26.7.28"})
			if err != nil {
				t.Fatal(err)
			}
			assertXHTTPJSON(t, cfg.CoreOutbound.StreamSettings.XHTTPSettings.Extra, extra)
			if protocol == "vless" && cfg.CoreOutbound.Settings.Vnext.([]coreObj.Vnext)[0].Users[0].Encryption != testVLESSEncryption {
				t.Fatal("VLESS Encryption was lost")
			}
			if protocol == "vless" {
				u, _ := url.Parse(got.ExportToURL())
				if u.Query().Get("mode") != "packet-up" || u.Query().Has("xhttpMode") || u.Query().Has("xhttpRawJson") {
					t.Fatal("export must use canonical mode/extra")
				}
			}
		})
	}
}

func TestXHTTPLegacyPrecedence(t *testing.T) {
	raw := `{"host":"legacy.example","path":"/legacy","mode":"stream-up","noSSEHeader":true,"headers":{"X-Outer":"1"},"extra":{"headers":{"X-Inner":"2"},"future":9007199254740993}}`
	v := parseXHTTPTest(t, "&xhttpRawJson="+url.QueryEscape(raw))
	if v.Host != "legacy.example" || v.Path != "/legacy" || v.XHTTPMode != "stream-up" {
		t.Fatalf("legacy basic fields were lost: %+v", v)
	}
	assertXHTTPJSON(t, v.XHTTPExtra, `{"noSSEHeader":true,"headers":{"X-Inner":"2"},"future":9007199254740993}`)
	v = parseXHTTPTest(t, "&host=&path=/own&mode=auto&xhttpMode=stream-one&xhttpRawJson="+url.QueryEscape(raw)+"&extra={}&noGRPCHeader=false&scMinPostsIntervalFrom=0&scMinPostsIntervalTo=0&xmuxHKeepAlive=-1")
	if v.Host != "" || v.Path != "/own" || v.XHTTPMode != "auto" {
		t.Fatalf("explicit basic parameters lost precedence: %+v", v)
	}
	assertXHTTPJSON(t, v.XHTTPExtra, `{"noGRPCHeader":false,"scMinPostsIntervalMs":0,"xmux":{"hKeepAlivePeriod":-1}}`)
}

func TestXHTTPFlattenedRangeCompatibility(t *testing.T) {
	v := parseXHTTPTest(t, "&mode=packet-up&x_padding_bytes=100-200&scMaxEachPostBytesFrom=8192&scMinPostsIntervalTo=0&xmuxMaxConcurFrom=4&xmuxMaxConcurTo=2&xmuxHMaxReusableFrom=0&xmuxHMaxReusableTo=0&xhttpHeaders="+url.QueryEscape(`{"X-Test":"ok"}`))
	assertXHTTPJSON(t, v.XHTTPExtra, `{"xPaddingBytes":"100-200","scMaxEachPostBytes":8192,"scMinPostsIntervalMs":0,"headers":{"X-Test":"ok"},"xmux":{"maxConcurrency":"4-2","hMaxReusableSecs":0}}`)
}

func TestXHTTPOldStoredURIRecoveryAndExplicitClear(t *testing.T) {
	link := xhttpTestLink + "&mode=packet-up&xhttpRawJson=" + url.QueryEscape(`{"host":"legacy.example","path":"/old","extra":{"xmux":{"hKeepAlivePeriod":-1},"future":false}}`)
	fields := map[string]interface{}{"protocol": "vless", "net": "xhttp", "xhttpMode": "auto", "host": "", "path": "", "v": link}
	for _, clear := range []bool{false, true} {
		if clear {
			fields["xhttpExtra"] = map[string]interface{}{}
		}
		stored, _ := json.Marshal(fields)
		var v V2Ray
		if err := jsoniter.Unmarshal(stored, &v); err != nil {
			t.Fatal(err)
		}
		if clear {
			assertXHTTPJSON(t, v.XHTTPExtra, `{}`)
			if v.XHTTPMode != "auto" || v.Host != "" {
				t.Fatal("explicit empty extra must prevent legacy recovery")
			}
		} else {
			assertXHTTPJSON(t, v.XHTTPExtra, `{"xmux":{"hKeepAlivePeriod":-1},"future":false}`)
			if v.XHTTPMode != "packet-up" || v.Host != "legacy.example" || v.Path != "/old" {
				t.Fatalf("legacy URI not restored: %+v", v)
			}
		}
	}
}

func TestVMessLegacyXHTTPJSON(t *testing.T) {
	raw := `{"add":"example.com","port":"443","id":"11111111-1111-4111-8111-111111111111","net":"xhttp","xhttpMode":"packet-up","scMinPostsIntervalFrom":0,"scMinPostsIntervalTo":0,"xmuxHKeepAlive":-1,"xhttpRawJson":"{\"future\":9007199254740993}"}`
	v, err := ParseVmessURL("vmess://" + base64.StdEncoding.EncodeToString([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	assertXHTTPJSON(t, v.XHTTPExtra, `{"scMinPostsIntervalMs":0,"xmux":{"hKeepAlivePeriod":-1},"future":9007199254740993}`)
}

func TestVMessLegacyXHTTPObjectRecoversEmptyBasicFields(t *testing.T) {
	for _, savedMode := range []string{"", "auto"} {
		fields := map[string]interface{}{
			"add": "example.com", "port": "443", "id": "11111111-1111-4111-8111-111111111111", "net": "xhttp",
			"host": "", "path": "", "xhttpMode": savedMode,
			"xhttpRawJson": `{"host":"raw.example","path":"/raw","mode":"stream-up","future":9007199254740993}`,
		}
		raw, _ := json.Marshal(fields)
		v, err := ParseVmessURL("vmess://" + base64.StdEncoding.EncodeToString(raw))
		if err != nil {
			t.Fatal(err)
		}
		if v.Host != "raw.example" || v.Path != "/raw" || v.XHTTPMode != "stream-up" {
			t.Fatalf("legacy full object was hidden by old defaults: %+v", v)
		}
		assertXHTTPJSON(t, v.XHTTPExtra, `{"future":9007199254740993}`)
		fields["mode"], fields["xhttpMode"], fields["host"], fields["path"] = "packet-up", "stream-one", "saved.example", "/saved"
		raw, _ = json.Marshal(fields)
		v, err = ParseVmessURL("vmess://" + base64.StdEncoding.EncodeToString(raw))
		if err != nil {
			t.Fatal(err)
		}
		if v.Host != "saved.example" || v.Path != "/saved" || v.XHTTPMode != "packet-up" {
			t.Fatalf("explicit saved values lost precedence: %+v", v)
		}
	}
}

func TestXHTTPOldStoredRawObjectModeRecovery(t *testing.T) {
	link := xhttpTestLink + "&xhttpRawJson=" + url.QueryEscape(`{"mode":"stream-up","host":"legacy.example","path":"/legacy","future":false}`)
	stored, _ := json.Marshal(map[string]interface{}{"protocol": "vless", "net": "xhttp", "xhttpMode": "auto", "host": "", "path": "", "v": link})
	var v V2Ray
	if err := jsoniter.Unmarshal(stored, &v); err != nil {
		t.Fatal(err)
	}
	if v.XHTTPMode != "stream-up" || v.Host != "legacy.example" || v.Path != "/legacy" {
		t.Fatalf("old parser defaults hid the full legacy object: %+v", v)
	}
}

func TestXHTTPRejectsInvalidOrDangerousParameters(t *testing.T) {
	for _, extra := range []string{
		`[]`, `null`, `"text"`, `{`,
		`{"scMaxEachPostBytes":"0-100"}`, `{"scMaxEachPostBytes":-1}`, `{"scMaxEachPostBytes":"2147483648"}`,
		`{"xPaddingBytes":"-1-100"}`, `{"xPaddingBytes":{"from":1,"to":2}}`, `{"scMaxBufferedPosts":-1}`,
		`{"noGRPCHeader":null}`, `{"noSSEHeader":"false"}`, `{"headers":{"Host":"example.com"}}`, `{"headers":{"X-Test":1}}`,
		`{"xmux":{"maxConnections":1,"maxConcurrency":2}}`, `{"xmux":{"hKeepAlivePeriod":1.5}}`,
		`{"downloadSettings":{}}`, `{"downloadSettings":{"network":"tcp"}}`,
		`{"downloadSettings":{"network":"xhttp"}}`, `{"downloadSettings":{"address":"example.com","port":0,"network":"xhttp"}}`,
		`{"sessionIDTable":"HEX"}`, `{"sessionIDTable":"HEX","sessionIDLength":0}`, `{"sessionIDTable":"HEX","sessionIDLength":"1-2147483647"}`,
		`{"downloadSettings":{"network":"xhttp","xhttpSettings":{"extra":{"xPaddingBytes":-1}}}}`,
	} {
		t.Run(extra, func(t *testing.T) {
			if _, err := ParseVlessURL(xhttpTestLink + "&extra=" + url.QueryEscape(extra)); err == nil {
				t.Fatal("unsafe extra accepted")
			}
		})
	}
	for _, query := range []string{"&mode=unknown", "&scMaxEachPostBytesFrom=not-int", "&noGRPCHeader=no", "&xhttpRawJson=[]", "&mode=stream-one&extra=" + url.QueryEscape(`{"downloadSettings":{"network":"xhttp"}}`)} {
		if _, err := ParseVlessURL(xhttpTestLink + query); err == nil {
			t.Fatalf("invalid query accepted: %s", query)
		}
	}
	for _, extra := range []string{`{"xPaddingBytes":0,"scMaxEachPostBytes":0,"scMaxBufferedPosts":0}`, `{"xPaddingBytes":"0-100","scMinPostsIntervalMs":"-2--1","xmux":{"hKeepAlivePeriod":-1}}`} {
		parseXHTTPTest(t, "&extra="+url.QueryEscape(extra))
	}
}

func TestXHTTPCoreCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name, query, version string
		variant              where.Variant
		wantError            string
	}{
		{"v2fly", "", "5.7.0", where.V2ray, "requires Xray"},
		{"old Xray", "", "1.8.24", where.Xray, "require Xray"},
		{"extra introduction", "&extra=" + url.QueryEscape(`{"future":false}`), "24.10.31", where.Xray, "24.11.11"},
		{"stream-one introduction", "&mode=stream-one", "24.11.21", where.Xray, "24.11.30"},
		{"stream-up introduction", "&mode=stream-up", "24.10.31", where.Xray, "24.11.11"},
		{"original basic XHTTP", "", "24.10.31", where.Xray, ""},
		{"new padding", "&extra=" + url.QueryEscape(`{"xPaddingObfsMode":false}`), "25.8.29", where.Xray, "26.1.31"},
		{"session ID introduction", "&extra=" + url.QueryEscape(`{"sessionIDKey":"sid"}`), "26.1.31", where.Xray, "26.6.22"},
		{"new zero padding bound", "&extra=" + url.QueryEscape(`{"xPaddingBytes":"0-100"}`), "26.7.28", where.Xray, "zero endpoint"},
		{"older zero padding bound", "&extra=" + url.QueryEscape(`{"xPaddingBytes":"0-100"}`), "25.8.29", where.Xray, ""},
		{"custom Xray", "&extra=" + url.QueryEscape(`{"xPaddingObfsMode":false,"future":9007199254740993}`), "custom", where.Xray, ""},
		{"nested feature version", "&extra=" + url.QueryEscape(`{"downloadSettings":{"address":"example.com","port":443,"network":"xhttp","xhttpSettings":{"extra":{"xPaddingObfsMode":true}}}}`), "25.8.29", where.Xray, "26.1.31"},
		{"nested zero padding bound", "&extra=" + url.QueryEscape(`{"downloadSettings":{"address":"example.com","port":443,"network":"xhttp","xhttpSettings":{"xPaddingBytes":"0-100"}}}`), "26.7.28", where.Xray, "zero endpoint"},
		{"download method alias", "&extra=" + url.QueryEscape(`{"downloadSettings":{"address":"example.com","port":443,"method":"xhttp"}}`), "26.6.22", where.Xray, "26.7.11"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := parseXHTTPTest(t, tt.query)
			_, err := v.Configuration(PriorInfo{Variant: tt.variant, CoreVersion: tt.version})
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
		})
	}
}
