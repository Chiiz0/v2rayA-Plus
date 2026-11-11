package service

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

// This opt-in check uses disposable containers with no network or mounts.
// It validates the real core's configuration parser, not a remote handshake.
func TestRealityDockerIntegration(t *testing.T) {
	if os.Getenv("V2RAYA_TEST_REALITY_DOCKER") != "1" {
		t.Skip("set V2RAYA_TEST_REALITY_DOCKER=1 to validate with the local Xray image")
	}
	const image = "teddysun/xray:latest"
	command := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		base := []string{"run", "--rm", "--network", "none", "--entrypoint", "xray", image}
		out, err := exec.CommandContext(ctx, "docker", append(base, args...)...).Output()
		if err != nil {
			t.Fatalf("Xray %s failed: %v", args[0], err)
		}
		return string(out)
	}
	t.Log(strings.SplitN(command("version"), "\n", 2)[0])
	keyOutput := command("mldsa65")
	var verify string
	for _, line := range strings.Split(keyOutput, "\n") {
		if strings.HasPrefix(line, "Verify:") {
			verify = strings.TrimSpace(strings.TrimPrefix(line, "Verify:"))
		}
	}
	if verify == "" {
		t.Fatal("Xray did not return an ML-DSA-65 verification key")
	}
	selected, err := selectDockerProxyCore(configure.DockerProxyCoreXray, where.V2ray, "5.41.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, front := range []bool{false, true} {
		name := "target"
		if front {
			name = "front"
		}
		t.Run(name, func(t *testing.T) {
			node, err := serverObj.ParseVlessURL("vless://11111111-1111-4111-8111-111111111111@example.com:443?type=tcp&security=reality&sni=example.com&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=ab&pqv=" + url.QueryEscape(verify))
			if err != nil {
				t.Fatal(err)
			}
			plain, err := serverObj.ParseVlessURL("vless://22222222-2222-4222-8222-222222222222@example.net:443?type=tcp&security=tls&sni=example.net")
			if err != nil {
				t.Fatal(err)
			}
			build := func() *dockerProxyRuntime {
				t.Helper()
				var target serverObj.ServerObj = node
				var pre serverObj.ServerObj
				if front {
					target, pre = plain, node
				}
				proxy, err := buildDockerProxyRuntimeForServers(target, pre, selected)
				if err != nil {
					t.Fatal(err)
				}
				return proxy
			}
			if err := validateDockerProxyConfig(build()); err != nil {
				t.Fatal(err)
			}
			// An invalid key must fail: this also proves the core reads the field.
			node.Mldsa65Verify = "invalid-verification-key"
			if err := validateDockerProxyConfig(build()); err == nil {
				t.Fatal("Xray accepted an invalid ML-DSA-65 verification key")
			}
		})
	}
}
