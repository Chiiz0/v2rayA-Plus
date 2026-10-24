package v2ray

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/db/configure"
)

func TestXHTTPDownloadsBypassTransparentProxy(t *testing.T) {
	original := `{"future":{"large":9007199254740993},"downloadSettings":{"address":"example.com","port":443,"network":"xhttp","sockopt":{"mark":9,"tcpFastOpen":false,"domainStrategy":"UseIP"},"xhttpSettings":{"path":"/download","extra":{"downloadSettings":{"address":"nested.example.com","port":443,"network":"xhttp"}}}}}`
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			setting := &configure.Setting{Transparent: configure.TransparentClose}
			if enabled {
				setting.Transparent = configure.TransparentGfwlist
			}
			tmpl := &Template{Setting: setting}
			model := json.RawMessage(original)
			out := coreObj.OutboundObject{StreamSettings: &coreObj.StreamSettings{Network: "xhttp", XHTTPSettings: &coreObj.XHTTPSettings{Extra: model}}}
			if err := tmpl.checkAndSetMark(&out, 0x80); err != nil {
				t.Fatal(err)
			}
			if string(model) != original {
				t.Fatal("template marking changed stored node parameters")
			}
			got := out.StreamSettings.XHTTPSettings.Extra
			if !enabled {
				if string(got) != original {
					t.Fatal("transparent-disabled mode changed download settings")
				}
				return
			}
			if out.StreamSettings.Sockopt.Mark == nil || *out.StreamSettings.Sockopt.Mark != 0x80 {
				t.Fatal("uplink not marked")
			}
			for _, path := range []string{"downloadSettings.sockopt.mark", "downloadSettings.xhttpSettings.extra.downloadSettings.sockopt.mark"} {
				if gjson.GetBytes(got, path).Int() != 0x80 {
					t.Fatalf("%s not marked: %s", path, got)
				}
			}
			for _, path := range []string{"future.large", "downloadSettings.sockopt.tcpFastOpen", "downloadSettings.sockopt.domainStrategy", "downloadSettings.xhttpSettings.path"} {
				if field := gjson.GetBytes(got, path); !field.Exists() || field.Raw != gjson.Get(original, path).Raw {
					t.Fatalf("marking changed %s: %s", path, got)
				}
			}
		})
	}
}

func TestXHTTPDownloadMarkRejectsInvalidSettings(t *testing.T) {
	for _, raw := range []string{`[]`, `{"downloadSettings":false}`, `{"downloadSettings":{"sockopt":"invalid"}}`, `{"downloadSettings":{"xhttpSettings":[]}}`} {
		if _, err := markXHTTPDownloads(json.RawMessage(raw), 0x80, 0); err == nil {
			t.Fatalf("accepted malformed settings: %s", raw)
		}
	}
}
