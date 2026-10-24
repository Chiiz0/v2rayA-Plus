//go:build linux

package v2ray

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func isolateResolver(t *testing.T) string {
	t.Helper()
	oldPath, oldBackup := resolvPath, resolvBackupPath
	dir := t.TempDir()
	resolvPath = filepath.Join(dir, "resolv.conf")
	resolvBackupPath = filepath.Join(dir, "snapshot.json")
	t.Cleanup(func() {
		removeResolvHijacker()
		resolvPath, resolvBackupPath = oldPath, oldBackup
	})
	return dir
}

func assertResolverBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %s = %q, %v; want %q", path, got, err, want)
	}
}

func TestResolvRoundTrip(t *testing.T) {
	for _, kind := range []string{"file", "empty", "missing", "absolute symlink", "relative symlink", "dangling symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := isolateResolver(t)
			original := []byte("# local configuration\nsearch office.example\noptions timeout:2 ndots:3\nnameserver 192.0.2.1\n")
			target := filepath.Join(dir, "manager-resolv.conf")
			var link string
			switch kind {
			case "empty":
				original = nil
				fallthrough
			case "file":
				if err := os.WriteFile(resolvPath, original, 0600); err != nil {
					t.Fatal(err)
				}
			case "absolute symlink", "relative symlink", "dangling symlink":
				link = target
				if kind == "relative symlink" {
					link = filepath.Base(target)
				}
				if kind != "dangling symlink" {
					if err := os.WriteFile(target, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(link, resolvPath); err != nil {
					t.Fatal(err)
				}
			}
			h := &ResolvHijacker{}
			if err := h.HijackResolv(); err != nil {
				t.Fatal(err)
			}
			backup, err := os.ReadFile(resolvBackupPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.HijackResolv(); err != nil {
				t.Fatal(err)
			}
			assertResolverBytes(t, resolvBackupPath, backup)
			if link != "" {
				if kind == "dangling symlink" {
					if _, err := os.Lstat(target); !os.IsNotExist(err) {
						t.Fatalf("dangling target was created: %v", err)
					}
				} else {
					assertResolverBytes(t, target, original)
				}
			}
			h.Close()
			if err := RecoverResolvConf(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				if _, err := os.Lstat(resolvPath); !os.IsNotExist(err) {
					t.Fatalf("missing state not restored: %v", err)
				}
			case "file", "empty":
				assertResolverBytes(t, resolvPath, original)
				info, _ := os.Stat(resolvPath)
				if info.Mode().Perm() != 0600 {
					t.Fatalf("mode = %v", info.Mode())
				}
			default:
				got, err := os.Readlink(resolvPath)
				if err != nil || got != link {
					t.Fatalf("symlink = %q, %v; want %q", got, err, link)
				}
			}
			if _, err := os.Lstat(resolvBackupPath); !os.IsNotExist(err) {
				t.Fatalf("backup not removed: %v", err)
			}
			if err := RecoverResolvConf(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResolvFailClosed(t *testing.T) {
	for _, failure := range []string{"backup directory absent", "corrupt backup", "backup symlink", "already intercepted"} {
		t.Run(failure, func(t *testing.T) {
			dir := isolateResolver(t)
			original := []byte("nameserver 192.0.2.10\n")
			if failure == "already intercepted" {
				original = []byte(HijackFlag + "\nnameserver 127.2.0.17\n")
			}
			if err := os.WriteFile(resolvPath, original, 0640); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "backup directory absent":
				resolvBackupPath = filepath.Join(dir, "absent", "snapshot")
			case "corrupt backup":
				if err := os.WriteFile(resolvBackupPath, []byte(`{"version":1,"kind":"invalid"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "backup symlink":
				if err := os.Symlink(resolvPath, resolvBackupPath); err != nil {
					t.Fatal(err)
				}
			}
			if h, err := NewResolvHijacker(); err == nil {
				h.Close()
				t.Fatal("interception unexpectedly succeeded")
			}
			assertResolverBytes(t, resolvPath, original)
		})
	}
}

func TestResolvBindMountCompatibleWrite(t *testing.T) {
	dir := isolateResolver(t)
	original := []byte("nameserver 192.0.2.11\noptions rotate\n")
	if err := os.WriteFile(resolvPath, original, 0640); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "same-inode")
	if err := os.Link(resolvPath, alias); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(resolvPath)
	if err := resetResolvHijacker(); err != nil {
		t.Fatal(err)
	}
	intercepted, err := os.ReadFile(alias)
	if err != nil || !bytes.HasPrefix(intercepted, []byte(HijackFlag)) {
		t.Fatalf("inode was replaced: %q %v", intercepted, err)
	}
	removeResolvHijacker()
	assertResolverBytes(t, alias, original)
	after, _ := os.Stat(resolvPath)
	if !os.SameFile(before, after) {
		t.Fatal("resolv.conf inode was replaced")
	}
}

func TestResolvRestoreRetryAndSymlinkSafety(t *testing.T) {
	dir := isolateResolver(t)
	original := []byte("nameserver 192.0.2.12\n")
	if err := os.WriteFile(resolvPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	h := &ResolvHijacker{}
	if err := h.HijackResolv(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(resolvPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(resolvPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := restoreResolv(); err == nil {
		t.Fatal("restore over directory succeeded")
	}
	if _, err := os.Stat(resolvBackupPath); err != nil {
		t.Fatalf("backup lost: %v", err)
	}
	if err := os.Remove(resolvPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "new-manager-target")
	unchanged := []byte("nameserver 192.0.2.99\n")
	if err := os.WriteFile(target, unchanged, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, resolvPath); err != nil {
		t.Fatal(err)
	}
	if err := writeResolvFileNoFollow(resolvPath, original, 0644); err == nil {
		t.Fatal("writer followed symlink")
	}
	if err := restoreResolv(); err != nil {
		t.Fatal(err)
	}
	assertResolverBytes(t, target, unchanged)
	assertResolverBytes(t, resolvPath, original)
}

func TestResolvCloseAndConcurrentLifecycle(t *testing.T) {
	isolateResolver(t)
	original := []byte("nameserver 192.0.2.13\n")
	if err := os.WriteFile(resolvPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	h, err := newResolvHijacker(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = h.HijackResolv()
				_ = h.Close()
			}
		}()
	}
	wg.Wait()
	if err := restoreResolv(); err != nil {
		t.Fatal(err)
	}
	if err := h.HijackResolv(); err != nil {
		t.Fatal(err)
	}
	assertResolverBytes(t, resolvPath, original)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := resetResolvHijacker(); err != nil {
					t.Error(err)
				}
				removeResolvHijacker()
			}
		}()
	}
	wg.Wait()
	removeResolvHijacker()
	assertResolverBytes(t, resolvPath, original)
}

func TestResolvRejectsLegacyInterceptedSymlinkTarget(t *testing.T) {
	dir := isolateResolver(t)
	target := filepath.Join(dir, "manager.conf")
	lostOriginal := []byte(HijackFlag + "\nnameserver 127.2.0.17\n")
	if err := os.WriteFile(target, lostOriginal, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, resolvPath); err != nil {
		t.Fatal(err)
	}
	if h, err := NewResolvHijacker(); err == nil {
		h.Close()
		t.Fatal("already overwritten target accepted as original")
	}
	assertResolverBytes(t, target, lostOriginal)
	if link, err := os.Readlink(resolvPath); err != nil || link != target {
		t.Fatalf("original link changed: %q %v", link, err)
	}
	if _, err := os.Lstat(resolvBackupPath); !os.IsNotExist(err) {
		t.Fatalf("invalid baseline was saved: %v", err)
	}
}
