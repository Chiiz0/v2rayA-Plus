package v2ray

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func visionOutbound(flow string) coreObj.OutboundObject {
	return coreObj.OutboundObject{Protocol: "vless", Settings: coreObj.Settings{
		Vnext: []coreObj.Vnext{{Users: []coreObj.User{{Flow: flow}}}},
	}}
}

func TestVisionTransparentSniffing(t *testing.T) {
	for _, flow := range []string{"", "xtls-rprx-vision", "xtls-rprx-vision-udp443"} {
		for _, mode := range []configure.InboundSniffing{configure.InboundSniffingDisable, "", configure.InboundSniffingHttpTLS, "http,tls,quic"} {
			for _, routeOnly := range []bool{false, true} {
				t.Run(flow+"/"+string(mode)+"/"+map[bool]string{false: "override", true: "route-only"}[routeOnly], func(t *testing.T) {
					setting := &configure.Setting{InboundSniffing: mode, RouteOnly: routeOnly}
					tmpl := &Template{Setting: setting, Outbounds: []coreObj.OutboundObject{
						{Protocol: "freedom"}, visionOutbound(flow), // Vision need not be the default outbound.
					}}
					tmpl.AppendDokodemoTProxy("redirect", 52345, "transparent")
					tmpl.AppendDokodemoTProxy("tproxy", 52346, "custom-transparent")
					tmpl.Inbounds = append(tmpl.Inbounds,
						coreObj.Inbound{Tag: "transparent_ipv4", Protocol: "socks"},
						coreObj.Inbound{Tag: "transparent_ipv6", Protocol: "socks"},
						coreObj.Inbound{Tag: "rule-socks", Protocol: "socks"},
						coreObj.Inbound{Tag: "dns-in", Protocol: "dokodemo-door"},
						coreObj.Inbound{Tag: "api-in", Protocol: "dokodemo-door"},
					)
					tmpl.setInboundSniffing(setting, "\n example.org\r\n\n")
					enabled := mode != "" && mode != configure.InboundSniffingDisable
					for i, in := range tmpl.Inbounds {
						wantEnabled := enabled && i < 5
						wantRouteOnly := wantEnabled && (routeOnly || (flow != "" && i < 4))
						if in.Sniffing.Enabled != wantEnabled || in.Sniffing.RouteOnly != wantRouteOnly {
							t.Fatalf("%s sniffing = %+v, enabled=%v routeOnly=%v", in.Tag, in.Sniffing, wantEnabled, wantRouteOnly)
						}
						if wantEnabled {
							want := []string{"http", "tls"}
							if mode != configure.InboundSniffingHttpTLS {
								want = append(want, "quic")
							}
							if !reflect.DeepEqual(in.Sniffing.DestOverride, want) || !reflect.DeepEqual(in.Sniffing.DomainsExcluded, []string{"example.org"}) {
								t.Fatalf("sniffing preference lost: %+v", in.Sniffing)
							}
						}
					}
					if setting.RouteOnly != routeOnly {
						t.Fatal("effective safeguard changed the saved preference")
					}
				})
			}
		}
	}
}

func TestVisionSniffingPreservesFakeDNS(t *testing.T) {
	tmpl := &Template{Outbounds: []coreObj.OutboundObject{visionOutbound("xtls-rprx-vision")}, Inbounds: []coreObj.Inbound{{
		Tag: "transparent", Sniffing: coreObj.Sniffing{Enabled: true, DestOverride: []string{"fakedns"}},
	}}}
	tmpl.protectVisionSniffing()
	if !tmpl.Inbounds[0].Sniffing.RouteOnly || !reflect.DeepEqual(tmpl.Inbounds[0].Sniffing.DestOverride, []string{"fakedns"}) {
		t.Fatalf("FakeDNS override lost: %+v", tmpl.Inbounds[0])
	}
}

func TestVisionLatencyMappingCoreCompatibility(t *testing.T) {
	for _, flow := range []string{"xtls-rprx-vision", "xtls-rprx-vision-udp443"} {
		for _, variant := range []where.Variant{where.Xray, where.V2ray, where.Unknown} {
			t.Run(flow+"/"+string(variant), func(t *testing.T) {
				node, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@example.com:443?type=tcp&security=tls&flow=" + flow)
				if err != nil {
					t.Fatal(err)
				}
				tmpl := &Template{Variant: variant, CoreVersion: "26.10.21", Setting: &configure.Setting{Transparent: configure.TransparentClose}}
				err = tmpl.InsertMappingOutbound(node, "11080", true, 0, "socks")
				if variant != where.Xray {
					if err == nil || !strings.Contains(err.Error(), "requires Xray") || strings.Contains(err.Error(), "unsupported") {
						t.Fatalf("expected actionable error that latency UI will retain, got %v", err)
					}
					if len(tmpl.Inbounds)+len(tmpl.Outbounds) != 0 {
						t.Fatal("failed configuration added a mapping")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := gjson.GetBytes(tmpl.ToConfigBytes(), "outbounds.0.settings.vnext.0.users.0.flow").String(); got != flow {
					t.Fatalf("flow = %q, want %q", got, flow)
				}
			})
		}
	}
}

func TestCoreDiagnosticLogging(t *testing.T) {
	for _, level := range []string{"trace", "debug", "info", "warn", "error"} {
		tmpl := &Template{}
		tmpl.setLog(level)
		config := tmpl.ToConfigBytes()
		if errLog := gjson.GetBytes(config, "log.error"); !errLog.Exists() || errLog.String() == "none" {
			t.Fatalf("%s disabled core errors: %s", level, config)
		}
		want := "info"
		if level == "warn" {
			want = "warning"
		} else if level == "error" {
			want = "error"
		}
		if tmpl.Log.Loglevel != want {
			t.Fatalf("%s core level = %s, want %s", level, tmpl.Log.Loglevel, want)
		}
	}
	for _, tc := range []struct{ line, level, message string }{
		{"2026/11/11 12:00:00.123456 [Error] connection failed", "error", "[Error] connection failed"},
		{"2026/11/11 12:00:00 [Warning] failed to decode response", "warning", "[Warning] failed to decode response"},
		{"[Debug] details", "debug", "[Debug] details"},
		{"2026/11/11 12:00:00 from tcp:127.0.0.1 accepted", "info", "from tcp:127.0.0.1 accepted"},
		{"plugin output [Error] quoted text", "info", "plugin output [Error] quoted text"},
	} {
		if level, message := coreLogLine(tc.line); level != tc.level || message != tc.message {
			t.Fatalf("coreLogLine(%q) = %q, %q", tc.line, level, message)
		}
	}
	if n, err := (logInfoWriter{}).Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write = %d, %v", n, err)
	}
}
