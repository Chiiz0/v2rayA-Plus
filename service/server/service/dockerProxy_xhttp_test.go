package service

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func TestDockerProxyXHTTP(t *testing.T) {
	const extra = `{"headers":{"X-Token":"test-header"},"noGRPCHeader":false,"scMinPostsIntervalMs":0,"xPaddingBytes":"100-200","xmux":{"hKeepAlivePeriod":-1},"futureKey":{"enabled":false}}`
	xhttp, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=xhttp&security=none&mode=packet-up&path=%2Ftunnel&extra=" + url.QueryEscape(extra))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := serverObj.ParseVlessURL("vless://22222222-2222-4222-8222-222222222222@203.0.113.11:24443?type=tcp&security=none")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		core    configure.DockerProxyCore
		main    where.Variant
		front   bool
		wantErr bool
	}{
		{name: "Xray proxy with V2Fly main", core: configure.DockerProxyCoreXray, main: where.V2ray},
		{name: "auto Xray", core: configure.DockerProxyCoreAuto, main: where.Xray},
		{name: "XHTTP front proxy", core: configure.DockerProxyCoreXray, front: true},
		{name: "V2Fly target rejected", core: configure.DockerProxyCoreV2Fly, wantErr: true},
		{name: "V2Fly front rejected", core: configure.DockerProxyCoreV2Fly, front: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectDockerProxyCore(tt.core, tt.main, "1.8.0")
			if err != nil {
				t.Fatal(err)
			}
			var server serverObj.ServerObj = xhttp
			var front serverObj.ServerObj
			index := "0"
			if tt.front {
				server, front, index = plain, xhttp, "1"
			}
			got, err := buildDockerProxyRuntimeForServers(server, front, selected)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "Xray") {
					t.Fatalf("expected Xray requirement, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.XHTTP {
				t.Fatal("XHTTP must trigger validation with the actual Docker image")
			}
			settings := gjson.GetBytes(got.ConfigBytes, "outbounds."+index+".streamSettings.xhttpSettings")
			if settings.Get("mode").String() != "packet-up" || settings.Get("path").String() != "/tunnel" {
				t.Fatalf("lost base settings: %s", settings.Raw)
			}
			for _, field := range []string{"noGRPCHeader", "scMinPostsIntervalMs", "xmux.hKeepAlivePeriod", "futureKey.enabled", "headers.X-Token", "xPaddingBytes"} {
				if gotField, want := settings.Get("extra."+field), gjson.Get(extra, field); !gotField.Exists() || gotField.Raw != want.Raw {
					t.Fatalf("extra %s = %s, want %s", field, gotField.Raw, want.Raw)
				}
			}
		})
	}
}

func TestDockerProxyXHTTPFrontChain(t *testing.T) {
	const extra = `{"future":9007199254740993,"downloadSettings":{"address":"download.example","port":443,"network":"xhttp","sockopt":{"tcpFastOpen":true,"dialerProxy":"old"},"xhttpSettings":{"path":"/download","extra":{"downloadSettings":{"address":"nested.example","port":443,"network":"splithttp","sockopt":{"mark":42},"splithttpSettings":{"path":"/nested","future":9007199254740995}}}}}}`
	target, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=xhttp&security=none&mode=packet-up&extra=" + url.QueryEscape(extra))
	if err != nil {
		t.Fatal(err)
	}
	front, err := serverObj.ParseVlessURL("vless://22222222-2222-4222-8222-222222222222@203.0.113.11:24443?type=tcp&security=none")
	if err != nil {
		t.Fatal(err)
	}
	selected, _ := selectDockerProxyCore(configure.DockerProxyCoreXray, where.V2ray, "5.41.0")
	got, err := buildDockerProxyRuntimeForServers(target, front, selected)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(got.ConfigBytes, "outbounds.0.proxySettings").Exists() {
		t.Fatal("legacy proxySettings would bypass the XHTTP transport")
	}
	for _, path := range []string{
		"outbounds.0.streamSettings.sockopt.dialerProxy",
		"outbounds.0.streamSettings.xhttpSettings.extra.downloadSettings.sockopt.dialerProxy",
		"outbounds.0.streamSettings.xhttpSettings.extra.downloadSettings.xhttpSettings.extra.downloadSettings.sockopt.dialerProxy",
	} {
		if value := gjson.GetBytes(got.ConfigBytes, path).String(); value != "pre-proxy" {
			t.Fatalf("%s = %q, want pre-proxy", path, value)
		}
	}
	stored := gjson.GetBytes(got.ConfigBytes, "outbounds.0.streamSettings.xhttpSettings.extra")
	for _, path := range []string{"future", "downloadSettings.sockopt.tcpFastOpen", "downloadSettings.xhttpSettings.path", "downloadSettings.xhttpSettings.extra.downloadSettings.sockopt.mark", "downloadSettings.xhttpSettings.extra.downloadSettings.splithttpSettings.future"} {
		if got, want := stored.Get(path).Raw, gjson.Get(extra, path).Raw; got != want {
			t.Fatalf("unrelated field %s changed: %s != %s", path, got, want)
		}
	}
	// Reusing the same saved node without a front must not inherit the prior chain.
	unlinked, err := buildDockerProxyRuntimeForServers(target, nil, selected)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(unlinked.ConfigBytes, "outbounds.0.streamSettings.sockopt.dialerProxy").Exists() ||
		gjson.GetBytes(unlinked.ConfigBytes, "outbounds.0.streamSettings.xhttpSettings.extra.downloadSettings.sockopt.dialerProxy").String() != "old" {
		t.Fatal("building the chain changed the saved node")
	}
}

func TestDockerProxyXHTTPImageVersionValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	for _, tt := range []struct {
		name, version, extra, wantErr string
		front                         bool
	}{
		{name: "old image rejects known fields", version: "24.12.31", extra: `{"xPaddingKey":"padding"}`, wantErr: "26.1.31"},
		{name: "new image validates config", version: "26.7.28", extra: `{"xPaddingKey":"padding"}`},
		{name: "front uses image version", version: "24.12.31", extra: `{"xPaddingKey":"padding"}`, front: true, wantErr: "26.1.31"},
		{name: "nested feature uses image version", version: "26.1.31", extra: `{"downloadSettings":{"address":"example.com","port":443,"network":"xhttp","xhttpSettings":{"extra":{"sessionIDKey":"session"}}}}`, wantErr: "26.6.22"},
		{name: "unrecognized version is not accepted", version: "unknown-secret-version", extra: `{}`, wantErr: "cannot determine Xray version"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DOCKER_TEST_CALLS", calls)
			t.Setenv("DOCKER_TEST_VERSION", tt.version)
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_TEST_CALLS\"\nif [ \"$1\" = rm ]; then exit 0; fi\ncase \"$*\" in *' version') printf 'Xray %s\\n' \"$DOCKER_TEST_VERSION\";; *) cat >/dev/null;; esac\n"
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			xhttp, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=xhttp&security=none&extra=" + url.QueryEscape(tt.extra))
			if err != nil {
				t.Fatal(err)
			}
			plain, _ := serverObj.ParseVlessURL("vless://22222222-2222-4222-8222-222222222222@203.0.113.11:24443?type=tcp&security=none")
			selected, _ := selectDockerProxyCore(configure.DockerProxyCoreXray, where.Xray, "99.0.0")
			var target serverObj.ServerObj = xhttp
			var front serverObj.ServerObj
			if tt.front {
				target, front = plain, xhttp
			}
			proxy, err := buildDockerProxyRuntimeForServers(target, front, selected)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" {
				err = setupDockerProxyRuntime(1085, proxy, true, false)
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || strings.Contains(err.Error(), "secret-version") {
					t.Fatalf("expected %q without output disclosure, got %v", tt.wantErr, err)
				}
			} else if err = validateDockerProxyConfig(proxy); err != nil {
				t.Fatal(err)
			}
			commands, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "run --rm --network none --entrypoint xray teddysun/xray:latest version"
			if tt.wantErr == "" {
				want += "\nrun --rm --network none -i --entrypoint xray teddysun/xray:latest run -test -config stdin:"
			}
			if got := dockerProxyCheckCommands(t, commands); got != want {
				t.Fatalf("unexpected operation before successful validation: %s", commands)
			}
		})
	}
}

func TestDockerProxyPreflightTimeoutCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	for _, phase := range []string{"version", "config"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DOCKER_TEST_CALLS", calls)
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_TEST_CALLS\"\nif [ \"$1\" = rm ]; then exit 0; fi\n"
			if phase == "config" {
				script += "case \"$*\" in *' version') echo 'Xray 26.7.28'; exit 0;; esac\n"
			}
			script += "exec sleep 60\n"
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := validateDockerProxyConfigContext(ctx, &dockerProxyRuntime{
				ConfigBytes: []byte(`{"outbounds":[]}`), Core: configure.DockerProxyCoreXray,
				ImageName: "teddysun/xray:latest", XHTTP: true,
			})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected bounded preflight, got %v", err)
			}
			commands, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			checks := strings.Split(dockerProxyCheckCommands(t, commands), "\n")
			want := 1
			if phase == "config" {
				want = 2
			}
			if len(checks) != want {
				t.Fatalf("expected %d checks with independent cleanup: %s", want, commands)
			}
		})
	}
}
