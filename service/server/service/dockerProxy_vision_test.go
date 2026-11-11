package service

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func TestDockerProxyVisionCoreCompatibility(t *testing.T) {
	vision, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@example.com:443?type=tcp&security=tls&flow=xtls-rprx-vision-udp443")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := serverObj.ParseVlessURL("vless://22222222-2222-4222-8222-222222222222@example.net:443?type=tcp&security=tls")
	if err != nil {
		t.Fatal(err)
	}
	for _, core := range []configure.DockerProxyCore{configure.DockerProxyCoreXray, configure.DockerProxyCoreV2Fly} {
		for _, front := range []bool{false, true} {
			t.Run(string(core)+map[bool]string{false: "/target", true: "/front"}[front], func(t *testing.T) {
				selected, err := selectDockerProxyCore(core, where.V2ray, "5.41.0")
				if err != nil {
					t.Fatal(err)
				}
				var target serverObj.ServerObj = vision
				var pre serverObj.ServerObj
				if front {
					target, pre = plain, vision
				}
				got, err := buildDockerProxyRuntimeForServers(target, pre, selected)
				if core == configure.DockerProxyCoreV2Fly {
					if err == nil || !strings.Contains(err.Error(), "requires Xray") {
						t.Fatalf("expected Xray requirement, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !got.Vision {
					t.Fatal("Vision must validate the actual Docker image")
				}
				if value := gjson.GetBytes(got.ConfigBytes, "log.error"); !value.Exists() || value.String() == "none" {
					t.Fatal("Vision errors are disabled")
				}
			})
		}
	}
}
