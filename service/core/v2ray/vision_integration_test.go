package v2ray

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

// Run only in a disposable Docker network namespace: this creates nft rules.
// See scripts/test-vision-transparent.sh. All traffic and certificates are local.
func TestVisionRedirectIntegration(t *testing.T) {
	if os.Getenv("V2RAYA_TEST_VISION_NETNS") != "1" {
		t.Skip("use scripts/test-vision-transparent.sh for isolated real-core testing")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("this test must run in an isolated Docker network namespace")
	}
	for _, binary := range []string{"xray", "v2ray"} {
		output, err := exec.Command(binary, "version").Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Log(strings.SplitN(string(output), "\n", 2)[0])
	}
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "vision redirect ok")
	}))
	defer target.Close()
	dir := t.TempDir()
	cert := target.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	certFile := write("cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	keyFile := write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	freePort := func() int {
		t.Helper()
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}
	start := func(t *testing.T, binary string, config []byte, port int) {
		t.Helper()
		path := write(fmt.Sprintf("%s-%d.json", binary, port), config)
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, binary, "run", "-c", path)
		// Trust only the disposable fixture certificate without using the
		// allowInsecure option removed by recent Xray versions.
		cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+certFile)
		logFile, err := os.CreateTemp(dir, "core-log-")
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			cancel()
			logFile.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cancel()
			_ = cmd.Wait()
			_ = logFile.Close()
			if t.Failed() {
				data, _ := os.ReadFile(logFile.Name())
				t.Log(string(data)) // disposable local credentials only
			}
		})
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("core listener did not start")
	}
	serverPort := freePort()
	const uuid = "11111111-1111-4111-8111-111111111111"
	server, err := json.Marshal(map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "info"},
		"dns": map[string]interface{}{"hosts": map[string]string{"example.com": "127.0.0.1"}},
		"inbounds": []interface{}{map[string]interface{}{
			"listen": "127.0.0.1", "port": serverPort, "protocol": "vless",
			"settings":       map[string]interface{}{"decryption": "none", "clients": []interface{}{map[string]string{"id": uuid, "flow": "xtls-rprx-vision"}}},
			"streamSettings": map[string]interface{}{"network": "tcp", "security": "tls", "tlsSettings": map[string]interface{}{"certificates": []coreObj.Certificate{{CertificateFile: certFile, KeyFile: keyFile}}}},
		}},
		// Recent Xray servers block private destinations by default. Permit
		// only the local test target in this disposable server configuration.
		"outbounds": []interface{}{map[string]interface{}{"protocol": "freedom", "settings": map[string]interface{}{"domainStrategy": "UseIP", "finalRules": []interface{}{map[string]interface{}{"action": "allow", "ip": []string{"127.0.0.1"}}}}, "streamSettings": map[string]interface{}{"sockopt": map[string]int{"mark": 128}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	start(t, "xray", server, serverPort)
	for _, tc := range []struct {
		name, binary                  string
		sniff, routeOnly, wantSuccess bool
	}{
		{"Xray generated safeguards", "xray", true, true, true},
		{"Xray sniffing disabled", "xray", false, false, true},
		// This control disproves the assumption that sniffing consumes ClientHello.
		{"Xray destination override control", "xray", true, false, true},
		{"V2Fly legacy config", "v2ray", true, false, false},
		{"V2Fly routeOnly alone cannot fix Vision", "v2ray", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := freePort()
			node, err := serverObj.ParseVlessURL(fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=tls&flow=xtls-rprx-vision&sni=example.com&fp=chrome&alpn=http%%2F1.1", uuid, serverPort))
			if err != nil {
				t.Fatal(err)
			}
			out, err := node.Configuration(serverObj.PriorInfo{Variant: where.Xray, Tag: "proxy"})
			if err != nil {
				t.Fatal(err)
			}
			setting := &configure.Setting{Transparent: configure.TransparentProxy, TransparentType: configure.TransparentRedirect, InboundSniffing: "http,tls,quic"}
			if !tc.sniff {
				setting.InboundSniffing = configure.InboundSniffingDisable
			}
			tmpl := &Template{Setting: setting, Outbounds: []coreObj.OutboundObject{out.CoreOutbound}}
			tmpl.AppendDokodemoTProxy("redirect", port, "transparent")
			tmpl.setInboundSniffing(setting, "")
			if tc.sniff && !tmpl.Inbounds[0].Sniffing.RouteOnly {
				t.Fatal("Vision safeguard missing")
			}
			if tc.sniff {
				// The request must actually be sniffed for this domain route to
				// succeed; merely forwarding the original IP cannot pass.
				tmpl.Outbounds = append(tmpl.Outbounds, coreObj.OutboundObject{Protocol: "blackhole", Tag: "block"})
				tmpl.Routing.Rules = []coreObj.RoutingRule{
					{Type: "field", Domain: []string{"full:example.com"}, OutboundTag: "proxy"},
					{Type: "field", Network: "tcp", OutboundTag: "block"},
				}
			}
			if err := tmpl.SetOutboundSockopt(); err != nil {
				t.Fatal(err)
			}
			tmpl.setLog("info")
			// Intentionally reconstruct the old broken config only for controls.
			if !tc.routeOnly {
				tmpl.Inbounds[0].Sniffing.RouteOnly = false
			}
			start(t, tc.binary, tmpl.ToConfigBytes(), port)
			targetPort := target.Listener.Addr().(*net.TCPAddr).Port
			rules := fmt.Sprintf("add table ip vision_test\nadd chain ip vision_test output { type nat hook output priority -100; policy accept; }\nadd rule ip vision_test output ip daddr 127.0.0.1 tcp dport %d meta mark != 128 counter redirect to :%d\n", targetPort, port)
			nft := exec.Command("nft", "-f", "-")
			nft.Stdin = strings.NewReader(rules)
			if output, err := nft.CombinedOutput(); err != nil {
				t.Fatalf("nft setup: %s: %v", output, err)
			}
			t.Cleanup(func() {
				if output, err := exec.Command("nft", "delete", "table", "ip", "vision_test").CombinedOutput(); err != nil {
					t.Errorf("nft cleanup: %s: %v", output, err)
				}
			})
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "example.com"}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			response, err := client.Get(target.URL)
			if tc.wantSuccess {
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != 200 || string(body) != "vision redirect ok" {
					t.Fatalf("TLS response: status=%d body=%q err=%v", response.StatusCode, body, err)
				}
			} else {
				if response != nil {
					response.Body.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "EOF") {
					t.Fatalf("expected legacy Vision TLS EOF, got %v", err)
				}
			}
			counter, err := exec.Command("nft", "list", "table", "ip", "vision_test").CombinedOutput()
			if err != nil || strings.Contains(string(counter), "counter packets 0") {
				t.Fatalf("request did not traverse redirect: %s (%v)", counter, err)
			}
		})
	}
}
