package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func requireSafeDNSCopy(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("safe no-follow file access is currently Linux-only")
	}
}

func writeDNSRecoveryFixture(t *testing.T, content []byte) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, DNSRecoveryFilename)
	if err := os.WriteFile(path, content, 0640); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestInspectDNSRecoveryMetadataAndMissing(t *testing.T) {
	dir := t.TempDir()
	report, err := InspectDNSRecovery(dir)
	if err != nil || report != (DNSReport{}) {
		t.Fatalf("missing recovery file: %+v, %v", report, err)
	}
	// Similar names and the live resolver file are never inspected.
	if err := os.WriteFile(filepath.Join(dir, "resolv.conf"), []byte("not the recovery file"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = InspectDNSRecovery(dir)
	if err != nil || report.Present {
		t.Fatalf("inspected a different file: %+v, %v", report, err)
	}
	dir, _ = writeDNSRecoveryFixture(t, []byte("opaque recovery bytes"))
	report, err = InspectDNSRecovery(dir)
	if err != nil || !report.Present || report.Bytes != 21 || report.SHA256 != "" {
		t.Fatalf("metadata inspection should not hash/read content: %+v, %v", report, err)
	}
	if _, err := InspectDNSRecovery(filepath.Join(t.TempDir(), "missing-directory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source directory must be an error: %v", err)
	}
}

func TestCopyDNSRecoveryPreservesOpaqueBytesAndPermissions(t *testing.T) {
	requireSafeDNSCopy(t)
	for _, content := range [][]byte{nil, []byte("not JSON\x00\xff\nsecret recovery bytes\r\n")} {
		dir, source := writeDNSRecoveryFixture(t, content)
		before, err := os.Stat(source)
		if err != nil {
			t.Fatal(err)
		}
		outDir := t.TempDir()
		destination := filepath.Join(outDir, "recovery-copy")
		report, err := CopyDNSRecovery(dir, destination)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		if !report.Present || report.Bytes != int64(len(content)) || report.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("incorrect recovery checksum or size: %+v", report)
		}
		copied, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(copied, content) {
			t.Fatal("recovery bytes were parsed, rewritten, or not copied")
		}
		info, err := os.Stat(destination)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("copy permissions must be 0600: %v, %v", info, err)
		}
		after, err := os.Stat(source)
		if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
			t.Fatalf("source metadata was changed: %v", err)
		}
		original, err := os.ReadFile(source)
		if err != nil || !bytes.Equal(original, content) {
			t.Fatal("source recovery data was changed")
		}
		entries, err := os.ReadDir(outDir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "recovery-copy" {
			t.Fatalf("temporary export files were not cleaned up: %v, %v", entries, err)
		}
	}
}

func TestCopyDNSRecoveryMissingCreatesNothing(t *testing.T) {
	requireSafeDNSCopy(t)
	outDir := t.TempDir()
	report, err := CopyDNSRecovery(t.TempDir(), filepath.Join(outDir, "copy"))
	if err != nil || report != (DNSReport{}) {
		t.Fatalf("missing recovery file: %+v, %v", report, err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing source created a destination: %v, %v", entries, err)
	}
}

func TestDNSRecoveryRejectsSourceSymlinksAndDirectories(t *testing.T) {
	requireSafeDNSCopy(t)
	for _, kind := range []string{"symlink", "dangling symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, DNSRecoveryFilename)
			if kind == "directory" {
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "target")
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte("do not follow"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, source); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := InspectDNSRecovery(dir); err == nil {
				t.Fatal("inspection accepted a non-regular recovery file")
			}
			destination := filepath.Join(t.TempDir(), "copy")
			if _, err := CopyDNSRecovery(dir, destination); err == nil {
				t.Fatal("copy accepted a non-regular recovery file")
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid source produced a destination: %v", err)
			}
		})
	}
	dir, _ := writeDNSRecoveryFixture(t, []byte("private"))
	linkedDir := filepath.Join(t.TempDir(), "linked-source")
	if err := os.Symlink(dir, linkedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDNSRecovery(linkedDir); err == nil {
		t.Fatal("inspection followed a source-directory symlink")
	}
	if _, err := CopyDNSRecovery(linkedDir, filepath.Join(t.TempDir(), "copy")); err == nil {
		t.Fatal("copy followed a source-directory symlink")
	}
}

func TestCopyDNSRecoveryNeverReplacesExistingDestination(t *testing.T) {
	requireSafeDNSCopy(t)
	dir, _ := writeDNSRecoveryFixture(t, []byte("new recovery bytes"))
	for _, kind := range []string{"file", "directory", "symlink", "dangling symlink"} {
		t.Run(kind, func(t *testing.T) {
			outDir := t.TempDir()
			destination := filepath.Join(outDir, "copy")
			switch kind {
			case "file":
				if err := os.WriteFile(destination, []byte("keep existing"), 0644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				target := filepath.Join(t.TempDir(), "target")
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte("keep existing"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, destination); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CopyDNSRecovery(dir, destination); !errors.Is(err, os.ErrExist) {
				t.Fatalf("existing destination must be rejected: %v", err)
			}
			after, err := os.Lstat(destination)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("existing path was replaced: %v", err)
			}
			if kind == "file" || kind == "symlink" {
				data, err := os.ReadFile(destination)
				if err != nil || string(data) != "keep existing" {
					t.Fatal("existing file or symlink target changed")
				}
			}
			entries, err := os.ReadDir(outDir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("rejected publication left temporary data: %v, %v", entries, err)
			}
		})
	}
}

func TestCopyDNSRecoveryConcurrentPublicationHasOneWinner(t *testing.T) {
	requireSafeDNSCopy(t)
	dir, _ := writeDNSRecoveryFixture(t, []byte("identical opaque recovery bytes"))
	destination := filepath.Join(t.TempDir(), "copy")
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := CopyDNSRecovery(dir, destination)
			results <- err
		}()
	}
	wg.Wait()
	first, second := <-results, <-results
	if (first == nil) == (second == nil) || first != nil && !errors.Is(first, os.ErrExist) || second != nil && !errors.Is(second, os.ErrExist) {
		t.Fatalf("expected one success and one existing-path error: %v, %v", first, second)
	}
}
