package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func archiveFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("safe source file opening is currently supported on Linux")
	}
	path := filepath.Join(t.TempDir(), "source.db")
	database, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		unknown, err := tx.CreateBucket([]byte{0xff, 0, 0xfe})
		if err != nil {
			return err
		}
		if err = unknown.SetSequence(1<<63 + 7); err != nil {
			return err
		}
		if err = unknown.Put([]byte{0, 0x80, 'k'}, []byte{0xff, 0, 0x80, 1}); err != nil {
			return err
		}
		child, err := unknown.CreateBucket([]byte{0, 0xff})
		if err != nil {
			return err
		}
		if err = child.SetSequence(42); err != nil {
			return err
		}
		if err = child.Put([]byte("opaque-gob"), []byte{0, 10, 255, 17, 0}); err != nil {
			return err
		}
		if _, err = child.CreateBucket([]byte("nested-empty")); err != nil {
			return err
		}
		empty, err := tx.CreateBucket([]byte("empty"))
		if err != nil {
			return err
		}
		if err = empty.SetSequence(97); err != nil {
			return err
		}
		system, err := tx.CreateBucket([]byte("system"))
		if err != nil {
			return err
		}
		if err = system.Put([]byte("json"), []byte(`{"unknown":9007199254740993,"off":false,"sentinel":-1}`)); err != nil {
			return err
		}
		return system.Put([]byte("empty-value"), []byte{})
	})
	closeErr := database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return path
}

func fixtureArchive(t *testing.T) *Archive {
	t.Helper()
	archive, err := Export(archiveFixture(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func archiveBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoArchiveTemps(t *testing.T, directory string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, ".*.tmp-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary artifacts remain: %v, %v", files, err)
	}
}

func TestArchiveCompleteByteRoundTrip(t *testing.T) {
	source := archiveFixture(t)
	original := archiveBytes(t, source)
	archive, err := Export(source, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Source.SHA256 != digest(original) || archive.Manifest.Buckets != 5 || archive.Manifest.Values != 4 {
		t.Fatalf("incorrect source manifest: %+v", archive.Manifest)
	}
	if !bytes.Equal(original, archiveBytes(t, source)) {
		t.Fatal("read-only export changed source bytes")
	}
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "snapshot.json")
	if err = Write(archivePath, archive); err != nil {
		t.Fatal(err)
	}
	loaded, err := Read(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(archive, loaded) {
		t.Fatal("JSON encoding did not preserve the complete byte snapshot")
	}
	otherArchive := filepath.Join(directory, "identical.json")
	if err := Write(otherArchive, archive); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archiveBytes(t, archivePath), archiveBytes(t, otherArchive)) {
		t.Fatal("writing the same archive is not deterministic")
	}
	target := filepath.Join(directory, "restored.db")
	if err = Restore(target, loaded); err != nil {
		t.Fatal(err)
	}
	restored, err := Export(target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Manifest != archive.Manifest || !reflect.DeepEqual(restored.Buckets, archive.Buckets) {
		t.Fatal("restored database differs in bytes, hierarchy, sequence, or empty values")
	}
	for _, path := range []string{archivePath, target} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("backup permissions are not 0600: %s, %v", path, err)
		}
	}
	assertNoArchiveTemps(t, directory)
}

func TestExportSourceLockAndReadOnly(t *testing.T) {
	path := archiveFixture(t)
	original := archiveBytes(t, path)
	writer, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = Export(path, 50*time.Millisecond)
	if !errors.Is(err, bbolt.ErrTimeout) || time.Since(started) > 2*time.Second {
		t.Fatalf("export did not respect the active writer lock and finite timeout: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	reader, err := bbolt.Open(path, 0400, &bbolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err = Export(path, time.Second); err != nil {
		t.Fatalf("shared read-only source should be exportable: %v", err)
	}
	if !bytes.Equal(original, archiveBytes(t, path)) {
		t.Fatal("locking or exporting changed source bytes")
	}
}

func TestExportRejectsMissingSymlinkAndDamagedSource(t *testing.T) {
	source := archiveFixture(t)
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.db")
	if _, err := Export(missing, time.Second); err == nil {
		t.Fatal("export accepted a missing source")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only export created the missing source")
	}
	link := filepath.Join(directory, "source-link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	damaged := filepath.Join(directory, "damaged.db")
	if err := os.WriteFile(damaged, []byte("not a Bolt database"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, damaged, directory} {
		if _, err := Export(path, time.Second); err == nil {
			t.Fatalf("export accepted invalid source %s", path)
		}
	}
	if _, err := Export(source, 0); err == nil {
		t.Fatal("export permitted an unlimited lock wait")
	}
}

func TestArchiveNeverOverwritesDestination(t *testing.T) {
	archive := fixtureArchive(t)
	for _, operation := range []struct {
		name string
		fn   func(string, *Archive) error
	}{{"write", Write}, {"restore", Restore}} {
		for _, kind := range []string{"file", "symlink", "dangling symlink", "directory"} {
			t.Run(operation.name+"/"+kind, func(t *testing.T) {
				directory := t.TempDir()
				target := filepath.Join(directory, "destination")
				original := []byte("existing user bytes")
				var err error
				switch kind {
				case "file":
					err = os.WriteFile(target, original, 0644)
				case "symlink":
					backing := filepath.Join(directory, "backing")
					if err = os.WriteFile(backing, original, 0600); err == nil {
						err = os.Symlink(backing, target)
					}
				case "dangling symlink":
					err = os.Symlink(filepath.Join(directory, "absent"), target)
				case "directory":
					err = os.Mkdir(target, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = operation.fn(target, archive); !errors.Is(err, os.ErrExist) {
					t.Fatalf("expected no-overwrite failure, got %v", err)
				}
				if kind == "file" || kind == "symlink" {
					if !bytes.Equal(archiveBytes(t, target), original) {
						t.Fatal("existing destination bytes changed")
					}
				}
				if strings.Contains(kind, "symlink") {
					if _, err := os.Readlink(target); err != nil {
						t.Fatal("existing destination symlink was replaced")
					}
				}
				assertNoArchiveTemps(t, directory)
			})
		}
	}
}

func TestArchiveWriteFailuresAndAtomicPublish(t *testing.T) {
	archive := fixtureArchive(t)
	directory := t.TempDir()
	for _, operation := range []func(string, *Archive) error{Write, Restore} {
		if err := operation(filepath.Join(directory, "missing-parent", "output"), archive); err == nil {
			t.Fatal("operation succeeded without a destination directory")
		}
		bad := *archive
		bad.Manifest.Values++
		path := filepath.Join(directory, "invalid-output")
		if err := operation(path, &bad); err == nil {
			t.Fatal("operation published an invalid archive")
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("validation failure created a destination")
		}
	}
	file, err := createArchiveTemp(filepath.Join(directory, "raced-target"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	if _, err = file.Write([]byte("new complete content")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "raced-target")
	if err = os.WriteFile(target, []byte("racing writer"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = publishNoReplace(file.Name(), target); !errors.Is(err, os.ErrExist) {
		t.Fatalf("racing destination was not protected: %v", err)
	}
	if string(archiveBytes(t, target)) != "racing writer" {
		t.Fatal("atomic publication overwrote a racing writer")
	}
}

func TestReadArchiveStrictJSONAndSize(t *testing.T) {
	archive := fixtureArchive(t)
	encoded, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, data string }{
		{"trailing object", string(encoded) + `{}`},
		{"trailing garbage", string(encoded) + `oops`},
		{"unknown field", `{"unknown":true,` + string(encoded[1:])},
		{"duplicate field", `{"version":1,` + string(encoded[1:])},
		{"case alias field", `{"Version":1,` + string(encoded[1:])},
		{"damaged JSON", string(encoded[:len(encoded)-1])},
		{"null", `null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(path); err == nil {
				t.Fatal("accepted noncanonical or invalid archive JSON")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "oversize.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(MaxArchiveBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversize archive was not rejected: %v", err)
	}
}
