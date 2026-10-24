package sqlitestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/store"
)

var contractBucket = []byte{0, 255, 'b'}
var contractRaw = []byte{0, 255, 2, 0, 128}

func contractArchive(t *testing.T) *backup.Archive {
	t.Helper()
	archive, err := backup.NewSnapshot("bbolt", []backup.Bucket{
		{Name: contractBucket, Sequence: math.MaxUint64,
			Values: []backup.Value{{Key: []byte("empty"), Data: []byte{}}, {Key: []byte("raw"), Data: contractRaw},
				{Key: []byte("json"), Data: []byte(`{"unknown":18446744073709551615,"zero":0,"false":false,"negative":-1}`)}},
			Buckets: []backup.Bucket{{Name: []byte("nested"), Sequence: 1 << 63,
				Values:  []backup.Value{{Key: []byte{0, 254}, Data: []byte{255, 0}}},
				Buckets: []backup.Bucket{{Name: []byte("deep"), Sequence: 42, Values: []backup.Value{{Key: []byte("value"), Data: []byte{}}}}}}}},
		{Name: []byte("other")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func contractStore(t *testing.T) (*Store, string, *backup.Archive) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	archive := contractArchive(t)
	if err := Create(path, archive); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, path, archive
}

func getValue(t *testing.T, database *Store, bucket, key []byte) []byte {
	t.Helper()
	var value []byte
	if err := database.View(func(tx store.ReadTx) error {
		var err error
		value, err = tx.Get(bucket, key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestReadContractAndFullSnapshot(t *testing.T) {
	database, _, original := contractStore(t)
	var keys, values, names [][]byte
	if err := database.View(func(tx store.ReadTx) error {
		if _, ok := tx.(store.WriteTx); ok {
			return errors.New("View exposes WriteTx")
		}
		for _, pair := range [][2][]byte{{[]byte("absent"), []byte("value")}, {contractBucket, []byte("absent")}, {contractBucket, []byte("nested")}} {
			value, err := tx.Get(pair[0], pair[1])
			if err != nil || value != nil {
				return fmt.Errorf("missing/nested value semantics: %v", err)
			}
		}
		empty, err := tx.Get(contractBucket, []byte("empty"))
		if err != nil || empty == nil || len(empty) != 0 {
			return fmt.Errorf("empty value semantics: %v", err)
		}
		length, err := tx.BucketLen(contractBucket)
		if err != nil || length != 7 {
			return fmt.Errorf("recursive bucket length=%d: %v", length, err)
		}
		length, err = tx.BucketLen([]byte("absent"))
		if err != nil || length != 0 {
			return fmt.Errorf("absent bucket length=%d: %v", length, err)
		}
		if err := tx.ForEach([]byte("absent"), func(_, _ []byte) error { return errors.New("absent bucket visited") }); err != nil {
			return err
		}
		if err := tx.ForEach(contractBucket, func(key, value []byte) error { keys = append(keys, key); values = append(values, value); return nil }); err != nil {
			return err
		}
		return tx.ForEachBucket(func(name []byte) error { names = append(names, name); return nil })
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, [][]byte{[]byte("empty"), []byte("json"), []byte("nested"), []byte("raw")}) || !reflect.DeepEqual(names, [][]byte{contractBucket, []byte("other")}) {
		t.Fatal("byte iteration order differs from Bolt")
	}
	if values[0] == nil || len(values[0]) != 0 || values[2] != nil || !bytes.Equal(values[3], contractRaw) {
		t.Fatal("iteration lost value semantics")
	}
	snapshot, err := database.Snapshot()
	if err != nil || snapshot.Manifest != original.Manifest || !reflect.DeepEqual(snapshot.Buckets, original.Buckets) {
		t.Fatalf("complete snapshot differs: %v", err)
	}
	if snapshot.Source.Backend != "sqlite" || snapshot.Source.DigestKind != backup.LogicalDigestKind || snapshot.Source.SHA256 != snapshot.Manifest.LogicalSHA256 {
		t.Fatal("snapshot source does not describe current logical data")
	}
}

func TestOwnedBytesSurviveTransactionsAndClose(t *testing.T) {
	database, _, _ := contractStore(t)
	var owned, iterated, bucketName []byte
	if err := database.View(func(tx store.ReadTx) error {
		var err error
		owned, err = tx.Get(contractBucket, []byte("raw"))
		if err != nil {
			return err
		}
		owned[0] = 123
		if err := tx.ForEach(contractBucket, func(key, value []byte) error {
			if bytes.Equal(key, []byte("raw")) {
				iterated = value
				key[0] = 'x'
				value[0] = 124
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.ForEachBucket(func(name []byte) error {
			if bytes.Equal(name, contractBucket) {
				bucketName = name
				name[0] = 125
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	retained := getValue(t, database, contractBucket, []byte("raw"))
	if !bytes.Equal(retained, contractRaw) {
		t.Fatal("caller mutation altered storage")
	}
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("raw"), []byte("replacement")) }); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, contractRaw) || owned[0] != 123 || iterated[0] != 124 || bucketName[0] != 125 {
		t.Fatal("returned slices do not remain owned after close")
	}
}

func TestWritesPreserveTreesAndUnsignedSequences(t *testing.T) {
	database, _, original := contractStore(t)
	if err := database.Update(func(tx store.WriteTx) error {
		if err := tx.Put(contractBucket, []byte{0, 255}, []byte{128, 0}); err != nil {
			return err
		}
		if err := tx.Put([]byte("created"), []byte("nil"), nil); err != nil {
			return err
		}
		if err := tx.Delete([]byte("missing"), []byte("absent")); err != nil {
			return err
		}
		if err := tx.Delete(contractBucket, []byte("absent")); err != nil {
			return err
		}
		if err := tx.DeleteBucket([]byte("missing")); err != nil {
			return err
		}
		return tx.DeleteBucket([]byte("other"))
	}); err != nil {
		t.Fatal(err)
	}
	current, err := database.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	root := current.Buckets[0]
	if root.Sequence != math.MaxUint64 || !reflect.DeepEqual(root.Buckets, original.Buckets[0].Buckets) {
		t.Fatal("top-level writes changed nested data or sequences")
	}
	for _, value := range original.Buckets[0].Values {
		if !bytes.Equal(getValue(t, database, contractBucket, value.Key), value.Data) {
			t.Fatal("unrelated value changed")
		}
	}
	if empty := getValue(t, database, []byte("created"), []byte("nil")); empty == nil || len(empty) != 0 {
		t.Fatal("Put(nil) did not create empty value")
	}
	for _, fn := range []func(store.WriteTx) error{
		func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("nested"), []byte("overwrite")) },
		func(tx store.WriteTx) error { return tx.Delete(contractBucket, []byte("nested")) },
	} {
		if err := database.Update(fn); !errors.Is(err, ErrIncompatibleValue) {
			t.Fatalf("nested value guard: %v", err)
		}
	}
	if err := database.Update(func(tx store.WriteTx) error { return tx.DeleteBucket(contractBucket) }); err != nil {
		t.Fatal(err)
	}
	current, err = database.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Buckets) != 1 || string(current.Buckets[0].Name) != "created" {
		t.Fatal("DeleteBucket did not cascade through complete nested tree")
	}
}

func TestErrorsAndPanicsRollBackWholeTransaction(t *testing.T) {
	database, _, original := contractStore(t)
	sentinel := errors.New("callback failed")
	mutate := func(tx store.WriteTx) error {
		if err := tx.Put(contractBucket, []byte("raw"), []byte("changed")); err != nil {
			return err
		}
		if err := tx.DeleteBucket([]byte("other")); err != nil {
			return err
		}
		return tx.Put([]byte("new"), []byte("value"), []byte("changed"))
	}
	assertOriginal := func() {
		t.Helper()
		snapshot, err := database.Snapshot()
		if err != nil || snapshot.Manifest != original.Manifest {
			t.Fatalf("partial changes escaped rollback: %v", err)
		}
	}
	if err := database.Update(func(tx store.WriteTx) error {
		if err := mutate(tx); err != nil {
			return err
		}
		return fmt.Errorf("wrapped: %w", sentinel)
	}); !errors.Is(err, sentinel) {
		t.Fatalf("callback identity lost: %v", err)
	}
	assertOriginal()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = database.Update(func(tx store.WriteTx) error {
			if err := mutate(tx); err != nil {
				return err
			}
			panic(sentinel)
		})
	}()
	if recovered != sentinel {
		t.Fatal("original panic was not propagated")
	}
	assertOriginal()
	if err := database.Update(func(tx store.WriteTx) error {
		if err := mutate(tx); err != nil {
			return err
		}
		return tx.Put(contractBucket, nil, []byte("bad"))
	}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("invalid key not rejected: %v", err)
	}
	assertOriginal()
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("after"), []byte("usable")) }); err != nil {
		t.Fatal("writer resource retained after rollback", err)
	}
}

func TestIterationStopsAndReadPanicReleasesResources(t *testing.T) {
	database, _, _ := contractStore(t)
	sentinel := errors.New("stop")
	for _, iterate := range []func(store.ReadTx, func() error) error{
		func(tx store.ReadTx, visit func() error) error {
			return tx.ForEach(contractBucket, func(_, _ []byte) error { return visit() })
		},
		func(tx store.ReadTx, visit func() error) error {
			return tx.ForEachBucket(func(_ []byte) error { return visit() })
		},
	} {
		calls := 0
		err := database.View(func(tx store.ReadTx) error { return iterate(tx, func() error { calls++; return sentinel }) })
		if !errors.Is(err, sentinel) || calls != 1 {
			t.Fatalf("iteration error: calls=%d, err=%v", calls, err)
		}
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = database.View(func(store.ReadTx) error { panic(sentinel) })
	}()
	if recovered != sentinel {
		t.Fatal("View panic lost")
	}
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("after"), nil) }); err != nil {
		t.Fatal(err)
	}
}

func TestViewDoesNotBlockOnWriterAndKeepsOneSnapshot(t *testing.T) {
	database, _, _ := contractStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- database.Update(func(tx store.WriteTx) error {
			if err := tx.Put(contractBucket, []byte("raw"), []byte("committed")); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start")
	}
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWriter()
	readDone := make(chan error, 1)
	go func() {
		readDone <- database.View(func(tx store.ReadTx) error {
			before, err := tx.Get(contractBucket, []byte("raw"))
			if err != nil {
				return err
			}
			if !bytes.Equal(before, contractRaw) {
				return errors.New("read observed uncommitted data")
			}
			releaseWriter()
			select {
			case err := <-writerDone:
				if err != nil {
					return err
				}
			case <-time.After(5 * time.Second):
				return errors.New("read snapshot blocked writer commit")
			}
			after, err := tx.Get(contractBucket, []byte("raw"))
			if err != nil {
				return err
			}
			if !bytes.Equal(before, after) {
				return errors.New("View combined different committed snapshots")
			}
			return nil
		})
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		releaseWriter()
		<-readDone
		t.Fatal("View waited for open writer")
	}
	if string(getValue(t, database, contractBucket, []byte("raw"))) != "committed" {
		t.Fatal("new reader missed commit")
	}
}

func TestConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	database, _, _ := contractStore(t)
	var group sync.WaitGroup
	errorsOut := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < 15; n++ {
				if err := database.Update(func(tx store.WriteTx) error {
					value, err := tx.Get([]byte("counter"), []byte("count"))
					if err != nil {
						return err
					}
					if value == nil {
						value = []byte{0}
					}
					return tx.Put([]byte("counter"), []byte("count"), []byte{value[0] + 1})
				}); err != nil {
					errorsOut <- err
					return
				}
			}
		}()
	}
	group.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Fatal(err)
	}
	if value := getValue(t, database, []byte("counter"), []byte("count")); !bytes.Equal(value, []byte{120}) {
		t.Fatal("concurrent transaction lost update")
	}
}

func TestConnectionSettingsAndBusyAreBounded(t *testing.T) {
	database, path, _ := contractStore(t)
	ctx := context.Background()
	// Hold all reader connections simultaneously to verify options on every
	// connection, including connections created lazily after Open returned.
	var connections []interface{ Close() error }
	defer func() {
		for _, c := range connections {
			_ = c.Close()
		}
	}()
	for n := 0; n < 4; n++ {
		connection, err := database.reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		for pragma, want := range map[string]int{"query_only": 1, "foreign_keys": 1, "trusted_schema": 0, "synchronous": 2, "busy_timeout": 2000} {
			var got int
			if err := connection.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("%s=%d: %v", pragma, got, err)
			}
		}
		if _, err := connection.ExecContext(ctx, "DELETE FROM kv_entries"); err == nil {
			t.Fatal("reader connection accepts writes")
		}
	}
	info, err := regularInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	other, err := openDatabase(path, info, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	started := time.Now()
	err = database.Update(func(tx store.WriteTx) error { return tx.Put([]byte("busy"), []byte("value"), nil) })
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("busy lock error lost: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("busy wait unbounded: %s", elapsed)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put([]byte("after-busy"), []byte("value"), nil) }); err != nil {
		t.Fatal(err)
	}
}

func TestClosedAndNilStoresReturnErrors(t *testing.T) {
	database, _, _ := contractStore(t)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Store{database, nil, &Store{}} {
		if err := s.View(func(store.ReadTx) error { t.Fatal("closed callback invoked"); return nil }); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed read: %v", err)
		}
		if err := s.Update(func(store.WriteTx) error { t.Fatal("closed callback invoked"); return nil }); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed write: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
