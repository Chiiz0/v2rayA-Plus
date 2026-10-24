//go:build linux

package v2ray

import (
	"bytes"
	"os"
	"testing"
)

// Run by scripts/test-resolv-docker.sh against a temporary file bind mount.
// The second container exercises recovery using only the persistent snapshot.
func TestResolvDockerBindMount(t *testing.T) {
	stage := os.Getenv("V2RAYA_TEST_RESOLV_STAGE")
	if stage == "" {
		t.Skip("requires the isolated Docker harness")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("this test must run inside the isolated Docker harness")
	}
	oldPath, oldBackup := resolvPath, resolvBackupPath
	resolvPath = "/dns-bind-test-resolv.conf"
	resolvBackupPath = "/dns-bind-test-backup/snapshot.json"
	defer func() { resolvPath, resolvBackupPath = oldPath, oldBackup }()
	original, err := os.ReadFile("/dns-bind-test-original")
	if err != nil {
		t.Fatal(err)
	}
	switch stage {
	case "hijack":
		assertResolverBytes(t, resolvPath, original)
		h, err := NewResolvHijacker()
		if err != nil {
			t.Fatal(err)
		}
		h.Close() // Deliberately leave the snapshot for the next container.
		got, err := os.ReadFile(resolvPath)
		if err != nil || !bytes.HasPrefix(got, []byte(HijackFlag)) {
			t.Fatalf("DNS interception failed: %q %v", got, err)
		}
	case "recover":
		if err := RecoverResolvConf(); err != nil {
			t.Fatal(err)
		}
		assertResolverBytes(t, resolvPath, original)
		info, err := os.Stat(resolvPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0640 {
			t.Fatalf("original mode not restored: %v", info.Mode())
		}
		if _, err := os.Stat(resolvBackupPath); !os.IsNotExist(err) {
			t.Fatalf("backup not cleaned: %v", err)
		}
	default:
		t.Fatalf("unknown stage %q", stage)
	}
}
