package service

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func TestSelectDockerProxyCore(t *testing.T) {
	tests := []struct {
		name            string
		core            configure.DockerProxyCore
		detectedVariant where.Variant
		detectedVersion string
		wantRequested   configure.DockerProxyCore
		wantCore        configure.DockerProxyCore
		wantVariant     where.Variant
		wantVersion     string
		wantImage       string
		wantMount       string
		wantErr         bool
	}{
		{
			name:            "legacy empty follows v2fly",
			detectedVariant: where.V2ray,
			detectedVersion: "5.41.0",
			wantRequested:   configure.DockerProxyCoreAuto,
			wantCore:        configure.DockerProxyCoreV2Fly,
			wantVariant:     where.V2ray,
			wantVersion:     "5.41.0",
			wantImage:       "v2fly/v2fly-core:v5.41.0",
			wantMount:       "/etc/v2ray",
		},
		{
			name:            "auto follows xray",
			core:            configure.DockerProxyCoreAuto,
			detectedVariant: where.Xray,
			detectedVersion: "26.7.28",
			wantRequested:   configure.DockerProxyCoreAuto,
			wantCore:        configure.DockerProxyCoreXray,
			wantVariant:     where.Xray,
			wantImage:       "teddysun/xray:latest",
			wantMount:       "/etc/xray",
		},
		{
			name:            "explicit xray overrides v2fly",
			core:            configure.DockerProxyCoreXray,
			detectedVariant: where.V2ray,
			detectedVersion: "5.41.0",
			wantRequested:   configure.DockerProxyCoreXray,
			wantCore:        configure.DockerProxyCoreXray,
			wantVariant:     where.Xray,
			wantImage:       "teddysun/xray:latest",
			wantMount:       "/etc/xray",
		},
		{
			name:            "explicit v2fly overrides xray",
			core:            configure.DockerProxyCoreV2Fly,
			detectedVariant: where.Xray,
			detectedVersion: "26.7.28",
			wantRequested:   configure.DockerProxyCoreV2Fly,
			wantCore:        configure.DockerProxyCoreV2Fly,
			wantVariant:     where.V2ray,
			wantImage:       "v2fly/v2fly-core:latest",
			wantMount:       "/etc/v2ray",
		},
		{name: "invalid", core: "sing-box", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectDockerProxyCore(tt.core, tt.detectedVariant, tt.detectedVersion)
			if (err != nil) != tt.wantErr {
				t.Fatalf("selectDockerProxyCore() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.RequestedCore != tt.wantRequested || got.Core != tt.wantCore || got.Variant != tt.wantVariant ||
				got.CoreVersion != tt.wantVersion || got.ImageName != tt.wantImage || got.MountPath != tt.wantMount {
				t.Fatalf("selectDockerProxyCore() = %+v", got)
			}
		})
	}
}

func TestDockerProxyVLESSEncryption(t *testing.T) {
	const encryption = "mlkem768x25519plus.native.0rtt.test-key"
	encrypted, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=tcp&security=none&encryption=" + url.QueryEscape(encryption))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := serverObj.ParseVlessURL("vless://22222222-2222-4222-8222-222222222222@203.0.113.11:24443?type=tcp&security=none")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		core    configure.DockerProxyCore
		variant where.Variant
		version string
		front   bool
		plain   bool
		wantErr bool
	}{
		{name: "explicit Xray with V2Fly main", core: configure.DockerProxyCoreXray, variant: where.V2ray, version: "5.41.0"},
		{name: "explicit Xray with older Xray main", core: configure.DockerProxyCoreXray, variant: where.Xray, version: "1.8.0"},
		{name: "auto with Xray main", core: configure.DockerProxyCoreAuto, variant: where.Xray, version: "26.7.28"},
		{name: "encrypted front proxy", core: configure.DockerProxyCoreXray, front: true},
		{name: "V2Fly rejects encryption", core: configure.DockerProxyCoreV2Fly, wantErr: true},
		{name: "auto V2Fly rejects encryption", core: configure.DockerProxyCoreAuto, variant: where.V2ray, wantErr: true},
		{name: "V2Fly rejects encrypted front", core: configure.DockerProxyCoreV2Fly, front: true, wantErr: true},
		{name: "legacy unencrypted VLESS", core: configure.DockerProxyCoreV2Fly, plain: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectDockerProxyCore(tt.core, tt.variant, tt.version)
			if err != nil {
				t.Fatal(err)
			}
			var server serverObj.ServerObj = encrypted
			var front serverObj.ServerObj
			if tt.front {
				server, front = plain, encrypted
			} else if tt.plain {
				server = plain
			}
			got, err := buildDockerProxyRuntimeForServers(server, front, selected)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "Xray") {
					t.Fatalf("expected explicit Xray requirement, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.VLESSEncryption == tt.plain {
				t.Fatalf("VLESSEncryption = %v, plain = %v", got.VLESSEncryption, tt.plain)
			}
			var config SimpleConfig
			if err := json.Unmarshal(got.ConfigBytes, &config); err != nil {
				t.Fatal(err)
			}
			index, want := 0, encryption
			if tt.front {
				index = 1
				if config.Outbounds[0].ProxySettings.Tag != "pre-proxy" {
					t.Fatal("target is not chained to front proxy")
				}
			} else if tt.plain {
				want = "none"
			}
			if actual := gjson.GetBytes(got.ConfigBytes, "outbounds."+strconv.Itoa(index)+".settings.vnext.0.users.0.encryption").String(); actual != want {
				t.Fatalf("generated encryption = %q, want %q", actual, want)
			}
		})
	}
}

func TestDockerProxyEncryptionValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	for _, fail := range []bool{false, true} {
		name := "supported image"
		if fail {
			name = "unsupported image leaves existing proxy intact"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			calls, config := filepath.Join(dir, "calls"), filepath.Join(dir, "config.json")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DOCKER_TEST_CALLS", calls)
			t.Setenv("DOCKER_TEST_CONFIG", config)
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_TEST_CALLS\"\ncat > \"$DOCKER_TEST_CONFIG\"\n"
			if fail {
				script += "echo 'invalid encryption: secret-test-key' >&2\nexit 1\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			proxy := &dockerProxyRuntime{
				ConfigBytes: []byte(`{"outbounds":[]}`), Core: configure.DockerProxyCoreXray,
				ImageName: "teddysun/xray:latest", VLESSEncryption: true,
			}
			var err error
			if fail {
				err = setupDockerProxyRuntime(1085, proxy, true, false)
			} else {
				err = validateDockerProxyEncryption(proxy)
			}
			if (err != nil) != fail {
				t.Fatalf("validation error = %v, want failure = %v", err, fail)
			}
			if fail && (!strings.Contains(err.Error(), "VLESS Encryption") || strings.Contains(err.Error(), "secret-test-key")) {
				t.Fatalf("expected actionable error without credentials, got %v", err)
			}
			commands, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "run --rm --network none -i --entrypoint xray teddysun/xray:latest run -test -config stdin:\n"
			if string(commands) != want {
				t.Fatalf("unexpected Docker commands (existing proxy must be untouched): %s", commands)
			}
			input, err := os.ReadFile(config)
			if err != nil || string(input) != string(proxy.ConfigBytes) {
				t.Fatalf("configuration was not passed intact: %s, %v", input, err)
			}
		})
	}
	t.Run("plain VLESS needs no preflight", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if err := validateDockerProxyEncryption(&dockerProxyRuntime{Core: configure.DockerProxyCoreV2Fly}); err != nil {
			t.Fatal(err)
		}
	})
}
