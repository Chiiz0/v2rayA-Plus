//go:build linux

package sqlitebackup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/common/instancelock"
	"github.com/v2rayA/v2rayA/db/backup"
	"go.etcd.io/bbolt"
)

func TestFileCandidateFailureAndCrashRetry(t *testing.T) {
	for _, abrupt := range []bool{false, true} {
		for _, point := range []string{"committed", "synced", "published"} {
			name := point
			if abrupt {
				name += "-process-exit"
			}
			t.Run(name, func(t *testing.T) {
				archive := storeTestArchive(t, nil)
				directory := t.TempDir()
				path := filepath.Join(directory, "candidate.sqlite")
				if abrupt {
					input := filepath.Join(directory, "source.json")
					if err := backup.Write(input, archive); err != nil {
						t.Fatal(err)
					}
					command := exec.Command(os.Args[0], "-test.run=^TestFileCandidateCrashHelper$")
					command.Env = append(os.Environ(), "V2RAYA_SQLITE_TEST_POINT="+point, "V2RAYA_SQLITE_TEST_DIRECTORY="+directory)
					output, err := command.CombinedOutput()
					var exitError *exec.ExitError
					if !errors.As(err, &exitError) || exitError.ExitCode() != 71 {
						t.Fatalf("crash helper failed: %v %s", err, output)
					}
				} else {
					_, err := importCandidate(context.Background(), path, archive, func(stage string) error {
						if stage == point {
							return errors.New("injected publication failure")
						}
						return nil
					})
					if err == nil {
						t.Fatal("failure injection was ignored")
					}
				}
				_, statErr := os.Lstat(path)
				if point == "published" && statErr != nil || point != "published" && !os.IsNotExist(statErr) {
					t.Fatalf("unexpected published state after %s: %v", point, statErr)
				}
				result, err := Import(context.Background(), path, archive)
				if err != nil || result.AlreadyPresent != (point == "published") {
					t.Fatalf("retry did not recover correctly: %+v %v", result, err)
				}
				actual, _, err := Verify(context.Background(), path)
				if err != nil || !sameArchive(actual, archive) {
					t.Fatalf("retry produced an incomplete archive: %v", err)
				}
			})
		}
	}
}

func TestFileCandidateCrashHelper(t *testing.T) {
	point := os.Getenv("V2RAYA_SQLITE_TEST_POINT")
	if point == "" {
		return
	}
	directory := os.Getenv("V2RAYA_SQLITE_TEST_DIRECTORY")
	archive, err := backup.Read(filepath.Join(directory, "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = importCandidate(context.Background(), filepath.Join(directory, "candidate.sqlite"), archive, func(stage string) error {
		if stage == point {
			os.Exit(71) // Deliberately skip SQLite/file/lock cleanup.
		}
		return nil
	})
	t.Fatalf("crash point not reached: %v", err)
}

func TestFileCandidateRefusesPublishRaceAndCancellation(t *testing.T) {
	archive := storeTestArchive(t, nil)
	for _, collision := range []string{"destination", "sidecar", "cancel"} {
		t.Run(collision, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "candidate.sqlite")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err := importCandidate(ctx, path, archive, func(stage string) error {
				if stage != "synced" {
					return nil
				}
				switch collision {
				case "destination":
					return os.WriteFile(path, []byte("keep existing destination"), 0600)
				case "sidecar":
					return os.WriteFile(path+"-wal", []byte("keep existing sidecar"), 0600)
				default:
					cancel()
					return ctx.Err()
				}
			})
			if err == nil {
				t.Fatal("publication should have failed")
			}
			if collision == "destination" {
				data, _ := os.ReadFile(path)
				if string(data) != "keep existing destination" {
					t.Fatal("publication replaced the racing destination")
				}
			} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("failed operation published a destination")
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".v2raya-sqlite-") {
					t.Fatal("ordinary failure leaked a private staging directory")
				}
			}
		})
	}
}

func TestFileCandidateLocksAndLegacyWriters(t *testing.T) {
	archive := storeTestArchive(t, nil)
	directory := t.TempDir()
	lock, err := instancelock.Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "candidate.sqlite")
	if _, err := Import(context.Background(), path, archive); !errors.Is(err, instancelock.ErrAlreadyRunning) {
		t.Fatalf("directory lock was ignored: %v", err)
	}
	if _, _, err := Verify(context.Background(), path); !errors.Is(err, instancelock.ErrAlreadyRunning) {
		t.Fatalf("verification bypassed directory lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bolt.db", "boltv4.db"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			database, err := bbolt.Open(filepath.Join(directory, name), 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			start := time.Now()
			if _, err := Import(context.Background(), filepath.Join(directory, "candidate.sqlite"), archive); err == nil {
				t.Fatal("old service writer lock was ignored")
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("old writer lock wait was not bounded")
			}
		})
	}
}

func TestFileCandidatePathsPermissionsAndSourcePreservation(t *testing.T) {
	archive := storeTestArchive(t, nil)
	directory := t.TempDir()
	path := filepath.Join(directory, "候选 ?#%.sqlite")
	result, err := Import(context.Background(), path, archive)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	beforeBytes, _ := os.ReadFile(path)
	if before.Mode().Perm() != 0600 || result.Engine.SQLiteVersion != "3.54.1" || result.SchemaVersion != SchemaVersion || result.SHA256 == "" {
		t.Fatalf("candidate identity/permissions missing: %+v", result)
	}
	actual, verified, err := Verify(context.Background(), path)
	if err != nil || !sameArchive(actual, archive) || verified.SHA256 != result.SHA256 {
		t.Fatalf("special-character path or snapshot failed: %v", err)
	}
	after, _ := os.Stat(path)
	afterBytes, _ := os.ReadFile(path)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("verification modified the source")
	}
	for _, kind := range []string{"symlink", "fifo", "directory", "huge"} {
		t.Run(kind, func(t *testing.T) {
			invalid := filepath.Join(directory, kind)
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(path, invalid)
			case "fifo":
				err = syscall.Mkfifo(invalid, 0600)
			case "directory":
				err = os.Mkdir(invalid, 0700)
			case "huge":
				var file *os.File
				file, err = os.Create(invalid)
				if err == nil {
					err = file.Truncate(maxDatabaseBytes + 1)
					_ = file.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := Verify(context.Background(), invalid); err == nil {
				t.Fatal("unsafe source was accepted")
			}
			if _, err := Import(context.Background(), invalid, archive); err == nil {
				t.Fatal("unsafe destination was accepted")
			}
		})
	}
	for _, name := range []string{"bolt.db", "boltv4.db", instancelock.Filename} {
		if _, err := Import(context.Background(), filepath.Join(directory, name), archive); err == nil {
			t.Fatalf("service filename %s was accepted", name)
		}
	}
}
