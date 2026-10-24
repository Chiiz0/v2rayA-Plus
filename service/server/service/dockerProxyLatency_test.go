package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/configure"
)

func TestSelectDockerProxiesByPorts(t *testing.T) {
	which := configure.Which{ID: 1, TYPE: configure.ServerType}
	proxies := []configure.DockerProxy{
		{Port: 1081, Which: which, Core: configure.DockerProxyCoreXray},
		{Port: 1082, Which: which, Core: configure.DockerProxyCoreV2Fly, FrontWhich: &configure.Which{ID: 2, TYPE: configure.ServerType}},
	}
	selected, err := selectDockerProxiesByPorts(proxies, []int{1082, 1081, 1082})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, []configure.DockerProxy{proxies[1], proxies[0]}) {
		t.Fatalf("selection lost port identity, settings, or input order: %+v", selected)
	}
	for _, tt := range []struct {
		name  string
		ports []int
	}{
		{name: "empty"},
		{name: "unknown port", ports: []int{1081, 1083}},
		{name: "zero port", ports: []int{0}},
		{name: "negative port", ports: []int{-1}},
		{name: "port above limit", ports: []int{65536}},
		{name: "too many requested ports", ports: make([]int, 33)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := selectDockerProxiesByPorts(proxies, tt.ports); err == nil {
				t.Fatal("expected invalid selection to fail")
			}
		})
	}
}

const dockerHTTPTestContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fakeDockerHTTPLatency(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("V2RAYA_HTTP_TEST_DIR", dir)
	t.Setenv("V2RAYA_HTTP_TEST_STATE", dockerHTTPTestContainerID+" true false")
	t.Setenv("V2RAYA_HTTP_TEST_OUTPUT", "204 0.123")
	t.Setenv("V2RAYA_HTTP_TEST_INSPECT_EXIT", "0")
	t.Setenv("V2RAYA_HTTP_TEST_RUN_EXIT", "0")
	t.Setenv("V2RAYA_HTTP_TEST_HANG", "")
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$V2RAYA_HTTP_TEST_DIR/calls"
printf '%s\000' "$@" > "$V2RAYA_HTTP_TEST_DIR/$1-args"
if [ "$V2RAYA_HTTP_TEST_HANG" = "$1" ]; then
  exec sleep 30
fi
case "$1" in
  inspect)
    printf '%s' "$V2RAYA_HTTP_TEST_STATE"
    exit "$V2RAYA_HTTP_TEST_INSPECT_EXIT"
    ;;
  run)
    printf '%s' "$V2RAYA_HTTP_TEST_OUTPUT"
    printf '%s' 'test diagnostic with secret credential' >&2
    exit "$V2RAYA_HTTP_TEST_RUN_EXIT"
    ;;
  rm) exit 0 ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func dockerHTTPTestArgs(t *testing.T, dir, command string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, command+"-args"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func assertDockerHTTPTestCalls(t *testing.T, dir, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("Docker calls = %q, want %q", b, want)
	}
}

func assertDockerHTTPHelperCleanup(t *testing.T, dir string) {
	t.Helper()
	assertDockerHTTPTestCalls(t, dir, "inspect\nrun\nrm\n")
	args := dockerHTTPTestArgs(t, dir, "run")
	if len(args) < 4 || args[2] != "--name" || !strings.HasPrefix(args[3], "v2raya-http-test-") {
		t.Fatalf("helper must have a unique test-container name: %q", args)
	}
	if got, want := dockerHTTPTestArgs(t, dir, "rm"), []string{"rm", "-f", args[3]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup = %q, want only the helper %q", got, want)
	}
}

func TestDockerProxyHttpLatencyUsesActualContainer(t *testing.T) {
	dir := fakeDockerHTTPLatency(t)
	// Stale database state and a stale Which must not affect the live test.
	proxy := configure.DockerProxy{
		Port: 1082, Status: "paused", ContainerName: "stale-name",
		Which: configure.Which{ID: 999, TYPE: configure.ServerType},
		Core:  configure.DockerProxyCoreAuto,
	}
	got := testDockerProxyHttpLatency(context.Background(), proxy, 8*time.Second, HttpTestURL)
	if got.Port != proxy.Port || got.Latency != "123ms" {
		t.Fatalf("latency = %+v, want port %d and 123ms", got, proxy.Port)
	}
	assertDockerHTTPHelperCleanup(t, dir)
	if got, want := dockerHTTPTestArgs(t, dir, "inspect"), []string{
		"inspect", "--type", "container", "--format", "{{.Id}} {{.State.Running}} {{.State.Paused}}", "v2raya-socks-1082",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inspect = %q, want %q", got, want)
	}
	args := dockerHTTPTestArgs(t, dir, "run")
	want := []string{
		"run", "--rm", "--name", args[3], "--network", "container:" + dockerHTTPTestContainerID,
		dockerProxyHTTPTestImage, "--silent", "--show-error", "--output", "/dev/null",
		"--write-out", "%{http_code} %{time_total}", "--max-time", "8.000", "--connect-timeout", "8.000",
		"--socks5-hostname", "127.0.0.1:1080", "--noproxy", "", "--proto", "=http,https", "--url", HttpTestURL,
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("curl must use the live container SOCKS port and never bypass it: got %q, want %q", args, want)
	}
}

func TestDockerProxyHttpLatencyContainerState(t *testing.T) {
	for _, tt := range []struct {
		name  string
		state string
		exit  int
		want  string
	}{
		{name: "paused", state: dockerHTTPTestContainerID + " true true", want: "PAUSED"},
		{name: "stopped", state: dockerHTTPTestContainerID + " false false", want: "STOPPED"},
		{name: "missing", exit: 1, want: "CONTAINER UNAVAILABLE"},
		{name: "incomplete inspect", state: "true false", want: "INVALID CONTAINER STATE"},
		{name: "invalid running state", state: dockerHTTPTestContainerID + " maybe false", want: "INVALID CONTAINER STATE"},
		{name: "invalid paused state", state: dockerHTTPTestContainerID + " true maybe", want: "INVALID CONTAINER STATE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeDockerHTTPLatency(t)
			t.Setenv("V2RAYA_HTTP_TEST_STATE", tt.state)
			t.Setenv("V2RAYA_HTTP_TEST_INSPECT_EXIT", strconv.Itoa(tt.exit))
			got := testDockerProxyHttpLatency(context.Background(), configure.DockerProxy{Port: 1081, Status: "running"}, time.Second, HttpTestURL)
			if got.Port != 1081 || got.Latency != tt.want {
				t.Fatalf("latency = %+v, want %s", got, tt.want)
			}
			assertDockerHTTPTestCalls(t, dir, "inspect\n")
		})
	}
}

func TestDockerProxyHttpLatencyResults(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
		exit   int
		want   string
	}{
		{name: "no content", output: "204 0.123", want: "123ms"},
		{name: "redirect is reachable", output: "302 0.0125", want: "12ms"},
		{name: "HTTP client error", output: "404 0.123", want: "BAD RESPONSE"},
		{name: "HTTP server error", output: "500 0.123", want: "BAD RESPONSE"},
		{name: "no HTTP response", output: "000 0.123", want: "BAD RESPONSE"},
		{name: "missing timing", output: "204", want: "INVALID TEST RESULT"},
		{name: "invalid status", output: "bad 0.123", want: "INVALID TEST RESULT"},
		{name: "invalid timing", output: "204 NaN", want: "INVALID TEST RESULT"},
		{name: "negative timing", output: "204 -0.123", want: "INVALID TEST RESULT"},
		{name: "curl timeout", output: "000 1.000", exit: 28, want: "TIMEOUT"},
		{name: "curl TLS failure", output: "000 0.123", exit: 35, want: "HTTP TEST FAILED"},
		{name: "nonzero exit overrides HTTP status", output: "204 0.123", exit: 7, want: "HTTP TEST FAILED"},
		{name: "Docker failure", exit: 125, want: "TEST ERROR: check Docker and the curl test image"},
		{name: "curl cannot execute", exit: 126, want: "TEST ERROR: check Docker and the curl test image"},
		{name: "curl missing", exit: 127, want: "TEST ERROR: check Docker and the curl test image"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeDockerHTTPLatency(t)
			t.Setenv("V2RAYA_HTTP_TEST_OUTPUT", tt.output)
			t.Setenv("V2RAYA_HTTP_TEST_RUN_EXIT", strconv.Itoa(tt.exit))
			got := testDockerProxyHttpLatency(context.Background(), configure.DockerProxy{Port: 1081}, time.Second, HttpTestURL)
			if got.Port != 1081 || got.Latency != tt.want {
				t.Fatalf("latency = %+v, want %s", got, tt.want)
			}
			assertDockerHTTPHelperCleanup(t, dir)
		})
	}
}

func TestDockerProxyHttpLatencyCancellation(t *testing.T) {
	for _, command := range []string{"inspect", "run"} {
		t.Run(command, func(t *testing.T) {
			dir := fakeDockerHTTPLatency(t)
			t.Setenv("V2RAYA_HTTP_TEST_HANG", command)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			started := time.Now()
			got := testDockerProxyHttpLatency(ctx, configure.DockerProxy{Port: 1081}, time.Second, HttpTestURL)
			if got.Port != 1081 || got.Latency != "TIMEOUT" {
				t.Fatalf("latency = %+v, want TIMEOUT", got)
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("canceled test did not return promptly")
			}
			if command == "run" {
				assertDockerHTTPHelperCleanup(t, dir)
			} else {
				assertDockerHTTPTestCalls(t, dir, "inspect\n")
			}
		})
	}
}
