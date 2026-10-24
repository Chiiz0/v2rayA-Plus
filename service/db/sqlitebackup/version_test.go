package sqlitebackup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v2rayA/v2rayA/db/backup"
)

func TestFrozenCandidateRejectsRuntimeSnapshotBeforeWriting(t *testing.T) {
	archive, err := backup.NewSnapshot("sqlite", storeTestArchive(t, nil).Buckets)
	if err != nil {
		t.Fatal(err)
	}
	database := storeTestDatabase(t)
	if err := Write(context.Background(), database, archive); err == nil {
		t.Fatal("runtime snapshot accepted by frozen v1 candidate writer")
	}
	var count int
	if err := database.QueryRow("SELECT count(*) FROM sqlite_schema").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected snapshot left candidate schema: %d, %v", count, err)
	}
	path := filepath.Join(t.TempDir(), "candidate.sqlite")
	if _, err := Import(context.Background(), path, archive); err == nil {
		t.Fatal("runtime snapshot accepted by frozen candidate import")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("rejected snapshot created a candidate file")
	}
}
