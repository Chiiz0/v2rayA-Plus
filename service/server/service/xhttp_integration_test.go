package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

// Opt-in integration test: use only new containers on an internal network,
// with no published ports, host networking, host resolver or Docker data mounts.
func TestXHTTPDockerIntegration(t *testing.T) {
	if os.Getenv("V2RAYA_TEST_XHTTP_DOCKER") != "1" {
		t.Skip("run scripts/test-xhttp-docker.sh for the isolated integration test")
	}
	dir := t.TempDir()
	prefix := "v2raya-xhttp-test-" + strings.ToLower(filepath.Base(dir)) + fmt.Sprint(time.Now().UnixNano())
	command := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}
	run := func(args ...string) []byte {
		t.Helper()
		out, err := command(args...)
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return out
	}
	write := func(name string, value interface{}) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	network := prefix + "-net"
	run("network", "create", "--internal", network)
	t.Cleanup(func() { _, _ = command("network", "rm", network) })
	start := func(name, image, network string, args ...string) {
		t.Helper()
		t.Cleanup(func() { _, _ = command("rm", "-f", name) })
		base := []string{"run", "--rm", "-d", "--name", name, "--network", network}
		base = append(base, args...)
		base = append(base, image)
		run(base...)
	}
	const body = "xhttp-advanced-settings-ok\n"
	originFile := filepath.Join(dir, "index.html")
	if err := os.WriteFile(originFile, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	start(prefix+"-origin", "nginx:alpine", network, "--network-alias", "origin", "--mount", "type=bind,src="+originFile+",dst=/usr/share/nginx/html/index.html,readonly")
	originIP := strings.TrimSpace(string(run("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", prefix+"-origin")))
	if output := run("run", "--rm", "--network", network, "curlimages/curl:latest", "--silent", "--show-error", "--fail", "--max-time", "5", "--retry", "3", "--retry-connrefused", "http://"+originIP+"/"); string(output) != body {
		t.Fatalf("isolated HTTP origin returned %q", output)
	}
	const id = "11111111-1111-4111-8111-111111111111"
	serverConfig := map[string]interface{}{
		"log": map[string]string{"loglevel": "debug"},
		"inbounds": []interface{}{map[string]interface{}{
			"listen": "0.0.0.0", "port": 24443, "protocol": "vless",
			"settings": map[string]interface{}{"decryption": "none", "clients": []interface{}{map[string]string{"id": id}}},
			"streamSettings": map[string]interface{}{"network": "xhttp", "security": "none", "xhttpSettings": map[string]interface{}{
				"path": "/tunnel", "mode": "auto", "extra": map[string]interface{}{"xPaddingBytes": "100-200"},
			}},
		}},
		// Recent Xray releases block private destinations from VLESS by default.
		// Permit only this isolated test origin; do not change application policy.
		"outbounds": []interface{}{map[string]interface{}{"protocol": "freedom", "settings": map[string]interface{}{
			"finalRules": []interface{}{map[string]interface{}{"action": "allow", "ip": []string{originIP + "/32"}}},
		}}},
	}
	serverPath := write("server.json", serverConfig)
	start(prefix+"-server", "teddysun/xray:latest", network, "--network-alias", "xhttp-server", "--mount", "type=bind,src="+serverPath+",dst=/etc/xray/config.json,readonly")
	serverIP := strings.TrimSpace(string(run("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", prefix+"-server")))
	frontNetwork := prefix + "-front-net"
	run("network", "create", "--internal", frontNetwork)
	t.Cleanup(func() { _, _ = command("network", "rm", frontNetwork) })
	frontConfig := map[string]interface{}{
		"log": map[string]string{"loglevel": "debug"},
		"inbounds": []interface{}{map[string]interface{}{
			"listen": "0.0.0.0", "port": 24444, "protocol": "vless",
			"settings": map[string]interface{}{"decryption": "none", "clients": []interface{}{map[string]string{"id": id}}},
		}},
		"outbounds": []interface{}{map[string]interface{}{"protocol": "freedom", "settings": map[string]interface{}{
			"finalRules": []interface{}{map[string]interface{}{"action": "allow", "ip": []string{serverIP + "/32"}}},
		}}},
	}
	frontPath := write("front.json", frontConfig)
	start(prefix+"-front", "teddysun/xray:latest", frontNetwork, "--network-alias", "front-server", "--mount", "type=bind,src="+frontPath+",dst=/etc/xray/config.json,readonly")
	run("network", "connect", network, prefix+"-front")
	for _, tt := range []struct {
		name          string
		splitDownload bool
		front         bool
	}{
		{name: "advanced packet-up"},
		{name: "separate download settings", splitDownload: true},
		{name: "front proxy with separate download", splitDownload: true, front: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			extra := map[string]interface{}{
				"headers": map[string]string{"X-Test-Header": "advanced"}, "noGRPCHeader": false, "noSSEHeader": false,
				"scMaxEachPostBytes": 8192, "scMinPostsIntervalMs": 0, "xPaddingBytes": "100-200",
				"xmux": map[string]interface{}{"maxConnections": 1, "hKeepAlivePeriod": -1},
			}
			if tt.splitDownload {
				extra["downloadSettings"] = map[string]interface{}{
					"address": serverIP, "port": 24443, "network": "xhttp", "security": "none",
					"xhttpSettings": map[string]interface{}{"path": "/tunnel", "mode": "packet-up", "extra": map[string]interface{}{"xPaddingBytes": "100-200"}},
				}
			}
			extraBytes, err := json.Marshal(extra)
			if err != nil {
				t.Fatal(err)
			}
			link := "vless://" + id + "@" + serverIP + ":24443?encryption=none&security=none&type=xhttp&mode=packet-up&path=%2Ftunnel&extra=" + url.QueryEscape(string(extraBytes))
			node, err := serverObj.ParseVlessURL(link)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := selectDockerProxyCore(configure.DockerProxyCoreXray, where.V2ray, "5.41.0")
			if err != nil {
				t.Fatal(err)
			}
			var front serverObj.ServerObj
			clientNetwork := network
			if tt.front {
				clientNetwork = frontNetwork
				// Prove the topology cannot hide a missing download dialerProxy.
				// An HTTP response (even 404) means a direct path exists.
				if output, err := command("run", "--rm", "--network", frontNetwork, "curlimages/curl:latest", "--silent", "--show-error", "--max-time", "2", "http://"+serverIP+":24443/tunnel"); err == nil {
					t.Fatalf("front-only network unexpectedly reached the XHTTP server directly: %s", output)
				}
				front, err = serverObj.ParseVlessURL("vless://" + id + "@front-server:24444?encryption=none&security=none&type=tcp")
				if err != nil {
					t.Fatal(err)
				}
			}
			proxy, err := buildDockerProxyRuntimeForServers(node, front, selected)
			if err != nil {
				t.Fatal(err)
			}
			if !proxy.XHTTP {
				t.Fatal("generated configuration is missing the XHTTP preflight flag")
			}
			if err := validateDockerProxyConfig(proxy); err != nil {
				t.Fatal(err)
			}
			settings := gjson.GetBytes(proxy.ConfigBytes, "outbounds.0.streamSettings.xhttpSettings")
			if !settings.Get("extra.noSSEHeader").Exists() || settings.Get("extra.xmux.hKeepAlivePeriod").Int() != -1 {
				t.Fatal("advanced settings were dropped before the real-core test")
			}
			clientSuffix := fmt.Sprintf("%v-%v", tt.splitDownload, tt.front)
			clientPath := filepath.Join(dir, "client-"+clientSuffix+".json")
			proxy.ConfigBytes, err = sjson.SetBytes(proxy.ConfigBytes, "log.loglevel", "debug")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(clientPath, proxy.ConfigBytes, 0644); err != nil {
				t.Fatal(err)
			}
			clientName := prefix + "-client-" + clientSuffix
			start(clientName, proxy.ImageName, clientNetwork, "--mount", "type=bind,src="+clientPath+",dst=/etc/xray/config.json,readonly")
			var output []byte
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				output, err = command("run", "--rm", "--network", "container:"+clientName, "curlimages/curl:latest", "--silent", "--show-error", "--fail", "--max-time", "5", "--proxy", "socks5h://127.0.0.1:1080", "http://"+originIP+"/")
				if err == nil && string(output) == body {
					t.Log("actual Xray SOCKS request returned the isolated origin body")
					return
				}
				time.Sleep(250 * time.Millisecond)
			}
			clientLogs, _ := command("logs", clientName)
			serverLogs, _ := command("logs", prefix+"-server")
			originLogs, _ := command("logs", prefix+"-origin")
			frontLogs, _ := command("logs", prefix+"-front")
			t.Fatalf("XHTTP request failed: %v %s\nclient: %s\nserver: %s\norigin: %s\nfront: %s", err, output, clientLogs, serverLogs, originLogs, frontLogs)
		})
	}
}
