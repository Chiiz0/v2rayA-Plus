package resolv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckResolvConfCreatesOnlyAbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if err := checkResolvConf(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "nameserver 223.6.6.6\n" {
		t.Fatalf("created resolver = %q, %v", got, err)
	}
	for _, content := range []string{"", "nameserver 192.0.2.53\nsearch lan\n"} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := checkResolvConf(path); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("existing resolver changed to %q, want %q (%v)", got, content, err)
		}
	}
}

func TestCheckResolvConfPreservesSymlinks(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		name := "existing target"
		if dangling {
			name = "missing target"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path, target := filepath.Join(dir, "resolv.conf"), filepath.Join(dir, "stub-resolv.conf")
			const original = "nameserver 127.0.0.53\n"
			if !dangling {
				if err := os.WriteFile(target, []byte(original), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := checkResolvConf(path); err != nil {
				t.Fatal(err)
			}
			if got, err := os.Readlink(path); err != nil || got != target {
				t.Fatalf("resolver symlink changed: %q, %v", got, err)
			}
			got, err := os.ReadFile(target)
			if dangling {
				if !os.IsNotExist(err) {
					t.Fatalf("missing symlink target was created: %q, %v", got, err)
				}
			} else if err != nil || string(got) != original {
				t.Fatalf("symlink target changed to %q, %v", got, err)
			}
		})
	}
}

func TestCreateResolvConfRejectsConcurrentReplacement(t *testing.T) {
	dir := t.TempDir()
	path, target := filepath.Join(dir, "resolv.conf"), filepath.Join(dir, "stub-resolv.conf")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("expected missing resolver before replacement")
	}
	// Simulate another resolver manager installing a link after the check.
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := createResolvConf(path, []string{"192.0.2.53"}); !os.IsExist(err) {
		t.Fatalf("creating over a symlink must fail with existence error, got %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("concurrently installed symlink target was created: %v", err)
	}
}
