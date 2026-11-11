package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func TestDockerProxyRealityImageVersionValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	for _, tt := range []struct {
		name, version, wantErr string
		front                  bool
	}{
		{name: "old image rejected", version: "25.6.8", wantErr: "25.7.26"},
		{name: "first supported image", version: "25.7.26"},
		{name: "current image", version: "26.10.31"},
		{name: "front uses image version", version: "25.6.8", front: true, wantErr: "25.7.26"},
		{name: "supported front", version: "26.10.31", front: true},
		{name: "unknown image rejected", version: "unknown-secret-version", wantErr: "cannot determine Xray version"},
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
			reality, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=tcp&security=reality&pqv=VERIFY")
			if err != nil {
				t.Fatal(err)
			}
			plain, _ := serverObj.ParseVlessURL("vless://22222222-2222-4222-8222-222222222222@203.0.113.11:24443?type=tcp&security=none")
			selected, _ := selectDockerProxyCore(configure.DockerProxyCoreXray, where.Xray, "99.0.0")
			var target serverObj.ServerObj = reality
			var front serverObj.ServerObj
			if tt.front {
				target, front = plain, reality
			}
			proxy, err := buildDockerProxyRuntimeForServers(target, front, selected)
			if err != nil {
				t.Fatal(err)
			}
			if !proxy.RealityMLDSA65 {
				t.Fatal("missing REALITY validation flag")
			}
			index := "0"
			if tt.front {
				index = "1"
			}
			if gjson.GetBytes(proxy.ConfigBytes, "outbounds."+index+".streamSettings.realitySettings.mldsa65Verify").String() != "VERIFY" {
				t.Fatal("missing verification key in generated configuration")
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
