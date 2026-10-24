package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeDockerInspection(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker inspection uses a POSIX shell")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("V2RAYA_INSPECTION_TEST_DIR", dir)
	t.Setenv("V2RAYA_INSPECTION_CONTAINER_MODE", "success")
	t.Setenv("V2RAYA_INSPECTION_VOLUME_MODE", "success")
	script := `#!/bin/sh
printf '%s\000' "$@" >> "$V2RAYA_INSPECTION_TEST_DIR/calls"
if [ "$2" != inspect ] || [ "$3" != -- ] || [ "$4" != v2raya-socks-19001 ]; then exit 99; fi
case "$1" in
  container) mode="$V2RAYA_INSPECTION_CONTAINER_MODE" ;;
  volume) mode="$V2RAYA_INSPECTION_VOLUME_MODE" ;;
  *) exit 99 ;;
esac
case "$mode" in
  missing) printf 'Error response from daemon: No such %s: %s\n' "$1" "$4" >&2; exit 1 ;;
  missing-volume) printf 'Error response from daemon: get %s: no such volume\n' "$4" >&2; exit 1 ;;
  unavailable) printf 'Cannot connect to Docker daemon: private-inspect-secret\n' >&2; exit 1 ;;
  empty) printf '[]'; exit 0 ;;
  invalid) printf 'private-inspect-secret'; exit 0 ;;
  large-out) head -c 1048577 /dev/zero; exit 0 ;;
  large-err) head -c 65537 /dev/zero >&2; exit 1 ;;
  hang) exec sleep 60 ;;
  *) cat "$V2RAYA_INSPECTION_TEST_DIR/$1.json" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	writeInspectionJSON(t, dir, "container", containerInspectionFixture())
	writeInspectionJSON(t, dir, "volume", map[string]interface{}{"Name": "v2raya-socks-19001"})
	return dir
}

func writeInspectionJSON(t *testing.T, dir, kind string, object map[string]interface{}) {
	t.Helper()
	data, err := json.Marshal([]interface{}{object})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, kind+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func containerInspectionFixture() map[string]interface{} {
	return map[string]interface{}{
		"Name":       "/v2raya-socks-19001",
		"Config":     map[string]interface{}{"Image": "teddysun/xray:latest", "Env": []string{"TOKEN=private-inspect-secret"}},
		"Mounts":     []interface{}{map[string]interface{}{"Type": "volume", "Name": "v2raya-socks-19001", "Destination": "/etc/xray"}},
		"HostConfig": map[string]interface{}{"PortBindings": map[string]interface{}{"1080/tcp": []interface{}{map[string]interface{}{"HostPort": "19001"}}}},
	}
}

func inspectionReference() DockerReference {
	return DockerReference{Port: 19001, ContainerName: "v2raya-socks-19001", VolumeName: "v2raya-socks-19001", Core: "xray"}
}

func assertInspectionContainsNoSecrets(t *testing.T, report ExternalReport) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil || strings.Contains(string(data), "private-inspect-secret") || strings.Contains(string(data), "TOKEN=") {
		t.Fatalf("report exposed raw Docker metadata: %v", err)
	}
}

func TestInspectDockerOnlyReadsExpectedMetadata(t *testing.T) {
	dir := fakeDockerInspection(t)
	report := InspectDocker(context.Background(), []DockerReference{inspectionReference()})
	if report.HasErrors() || len(report.Checks) != 1 || report.Checks[0].Container != "present" || report.Checks[0].Volume != "present" || !strings.Contains(report.Scope, "volume contents not checked") {
		t.Fatalf("unexpected inspection: %+v", report)
	}
	assertInspectionContainsNoSecrets(t, report)
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	want := []string{"container", "inspect", "--", "v2raya-socks-19001", "volume", "inspect", "--", "v2raya-socks-19001"}
	got := strings.Split(strings.TrimSuffix(string(calls), "\x00"), "\x00")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("inspection performed unexpected commands: %q, %v", got, err)
	}
}

func TestInspectDockerDistinguishesMissingAndUnavailable(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "unavailable", "invalid", "large-out", "large-err", "hang"} {
		t.Run(mode, func(t *testing.T) {
			fakeDockerInspection(t)
			t.Setenv("V2RAYA_INSPECTION_CONTAINER_MODE", mode)
			t.Setenv("V2RAYA_INSPECTION_VOLUME_MODE", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			started := time.Now()
			report := InspectDocker(ctx, []DockerReference{inspectionReference()})
			want := "unavailable"
			if mode == "missing" || mode == "empty" {
				want = "missing"
			}
			if !report.HasErrors() || len(report.Checks) != 1 || report.Checks[0].Container != want || report.Checks[0].Volume != want {
				t.Fatalf("%s state = %+v, want %s", mode, report, want)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("inspection exceeded its parent deadline: %v", elapsed)
			}
			if strings.HasPrefix(mode, "large") && !strings.Contains(report.Checks[0].ContainerDetail, "limit") {
				t.Fatal("large inspection output was not bounded")
			}
			assertInspectionContainsNoSecrets(t, report)
		})
	}
}

func TestInspectDockerRecognizesDaemonMissingVolume(t *testing.T) {
	fakeDockerInspection(t)
	t.Setenv("V2RAYA_INSPECTION_VOLUME_MODE", "missing-volume")
	report := InspectDocker(context.Background(), []DockerReference{inspectionReference()})
	if !report.HasErrors() || report.Checks[0].Container != "present" || report.Checks[0].Volume != "missing" {
		t.Fatalf("daemon missing-volume response misclassified: %+v", report)
	}
}

func TestInspectDockerReportsMetadataDrift(t *testing.T) {
	for _, kind := range []string{"image", "mount", "port"} {
		t.Run(kind, func(t *testing.T) {
			dir := fakeDockerInspection(t)
			metadata := containerInspectionFixture()
			switch kind {
			case "image":
				metadata["Config"] = map[string]interface{}{"Image": "v2fly/v2fly-core:v5.16.1"}
			case "mount":
				metadata["Mounts"] = []interface{}{map[string]interface{}{"Type": "bind", "Name": "v2raya-socks-19001", "Destination": "/etc/xray"}}
			case "port":
				metadata["HostConfig"] = map[string]interface{}{"PortBindings": map[string]interface{}{"1080/tcp": []interface{}{map[string]interface{}{"HostPort": "19002"}}}}
			}
			writeInspectionJSON(t, dir, "container", metadata)
			report := InspectDocker(context.Background(), []DockerReference{inspectionReference()})
			if !report.HasErrors() || report.Checks[0].Container != "present" || len(report.Checks[0].Differences) != 1 {
				t.Fatalf("metadata drift was not distinguished from absence: %+v", report)
			}
		})
	}
}

func TestInspectDockerAutoCoreAndTaggedRepositories(t *testing.T) {
	for _, image := range []string{"docker.io/teddysun/xray:26.7.28", "index.docker.io/teddysun/xray@sha256:1234", "v2fly/v2fly-core:v5.16.1"} {
		t.Run(image, func(t *testing.T) {
			dir := fakeDockerInspection(t)
			metadata := containerInspectionFixture()
			metadata["Config"] = map[string]interface{}{"Image": image}
			if strings.HasPrefix(image, "v2fly/") {
				metadata["Mounts"] = []interface{}{map[string]interface{}{"Type": "volume", "Name": "v2raya-socks-19001", "Destination": "/etc/v2ray"}}
			}
			writeInspectionJSON(t, dir, "container", metadata)
			ref := inspectionReference()
			ref.Core = "auto"
			if report := InspectDocker(context.Background(), []DockerReference{ref}); report.HasErrors() {
				t.Fatalf("known repository tag/digest was misclassified: %+v", report)
			}
		})
	}
}

func TestInspectDockerInvalidIdentityDoesNotExecute(t *testing.T) {
	dir := fakeDockerInspection(t)
	for _, ref := range []DockerReference{
		{Port: 0}, {Port: 65536}, {Port: 19001, ContainerName: "other-container"}, {Port: 19001, VolumeName: "other-volume"},
	} {
		if report := InspectDocker(context.Background(), []DockerReference{ref}); !report.HasErrors() {
			t.Fatalf("invalid identity was accepted: %+v", report)
		}
	}
	if report := InspectDocker(context.Background(), nil); report.HasErrors() || len(report.Checks) != 0 {
		t.Fatalf("empty inventory should need no external checks: %+v", report)
	}
	if _, err := os.Lstat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatalf("invalid/empty inventory executed Docker: %v", err)
	}
}
