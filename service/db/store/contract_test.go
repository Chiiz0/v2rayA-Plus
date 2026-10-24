package store_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

var contractBucket = []byte{0, 255, 'b'}
var contractRaw = []byte{0, 255, 2, 0, 128}

func newContractBolt(t *testing.T) (*bbolt.DB, store.Store) {
	t.Helper()
	handle, err := bbolt.Open(filepath.Join(t.TempDir(), "contract.db"), 0600, &bbolt.Options{InitialMmapSize: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := handle.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucket(contractBucket)
		if err != nil {
			return err
		}
		if err := bucket.SetSequence(math.MaxUint64); err != nil {
			return err
		}
		for key, value := range map[string][]byte{
			"empty": {}, "raw": contractRaw,
			"json": []byte(`{"unknown":18446744073709551615,"zero":0,"false":false,"negative":-1}`),
		} {
			if err := bucket.Put([]byte(key), value); err != nil {
				return err
			}
		}
		child, err := bucket.CreateBucket([]byte("nested"))
		if err != nil {
			return err
		}
		if err := child.SetSequence(1 << 63); err != nil {
			return err
		}
		if err := child.Put([]byte{0, 254}, []byte{255, 0}); err != nil {
			return err
		}
		deep, err := child.CreateBucket([]byte("deep"))
		if err != nil {
			return err
		}
		if err := deep.SetSequence(42); err != nil {
			return err
		}
		if err := deep.Put([]byte("value"), []byte{}); err != nil {
			return err
		}
		_, err = tx.CreateBucket([]byte("other"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return handle, store.NewBolt(handle)
}

func getContractValue(t *testing.T, database store.Store, bucket, key []byte) []byte {
	t.Helper()
	var result []byte
	if err := database.View(func(tx store.ReadTx) error {
		var err error
		result, err = tx.Get(bucket, key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBoltReadContract(t *testing.T) {
	handle, database := newContractBolt(t)
	before, err := os.ReadFile(handle.Path())
	if err != nil {
		t.Fatal(err)
	}
	var keys, buckets [][]byte
	var values [][]byte
	if err := database.View(func(tx store.ReadTx) error {
		if _, writable := tx.(store.WriteTx); writable {
			return errors.New("View exposes a writable transaction")
		}
		for _, test := range []struct{ bucket, key []byte }{
			{[]byte("absent"), []byte("value")}, {contractBucket, []byte("absent")}, {contractBucket, []byte("nested")},
		} {
			value, err := tx.Get(test.bucket, test.key)
			if err != nil || value != nil {
				return fmt.Errorf("missing value or nested bucket did not return nil: %v", err)
			}
		}
		empty, err := tx.Get(contractBucket, []byte("empty"))
		if err != nil || empty == nil || len(empty) != 0 {
			return fmt.Errorf("empty value was confused with missing: %v", err)
		}
		length, err := tx.BucketLen([]byte("absent"))
		if err != nil || length != 0 {
			return fmt.Errorf("missing bucket length: %d, %v", length, err)
		}
		if err := tx.ForEach([]byte("absent"), func(_, _ []byte) error { return errors.New("missing bucket was visited") }); err != nil {
			return err
		}
		// Four direct entries, two entries in nested, one in deep. A SQL
		// implementation counting only direct values would incorrectly return 3.
		length, err = tx.BucketLen(contractBucket)
		if err != nil || length != 7 {
			return fmt.Errorf("recursive bucket key count = %d, want 7: %v", length, err)
		}
		if err := tx.ForEach(contractBucket, func(key, value []byte) error {
			keys, values = append(keys, key), append(values, value)
			return nil
		}); err != nil {
			return err
		}
		return tx.ForEachBucket(func(name []byte) error { buckets = append(buckets, name); return nil })
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, [][]byte{[]byte("empty"), []byte("json"), []byte("nested"), []byte("raw")}) ||
		!reflect.DeepEqual(buckets, [][]byte{contractBucket, []byte("other")}) {
		t.Fatalf("iteration changed byte ordering: keys %q, buckets %q", keys, buckets)
	}
	if values[0] == nil || len(values[0]) != 0 || values[2] != nil || !bytes.Equal(values[3], contractRaw) {
		t.Fatal("iteration lost empty, nested bucket, or raw byte semantics")
	}
	after, err := os.ReadFile(handle.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read operations modified the database snapshot: %v", err)
	}
}

func TestBoltReturnedBytesAreOwned(t *testing.T) {
	handle, database := newContractBolt(t)
	var owned, iterated, bucketName []byte
	if err := database.View(func(tx store.ReadTx) error {
		var err error
		owned, err = tx.Get(contractBucket, []byte("raw"))
		if err != nil {
			return err
		}
		// Mutating a returned slice must not mutate mmap-backed source pages.
		owned[0] = 123
		if err := tx.ForEach(contractBucket, func(key, value []byte) error {
			if bytes.Equal(key, []byte("raw")) {
				iterated = value
				key[0], value[0] = 'x', 124
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
	if actual := getContractValue(t, database, contractBucket, []byte("raw")); !bytes.Equal(actual, contractRaw) {
		t.Fatal("caller mutation changed stored bytes")
	}
	retained := getContractValue(t, database, contractBucket, []byte("raw"))
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("raw"), []byte("replacement")) }); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, contractRaw) || owned[0] != 123 || iterated[0] != 124 || bucketName[0] != 125 {
		t.Fatal("returned bytes did not survive transaction end, later writes and handle close")
	}
}

func TestBoltWritesPreserveUnknownTreesAndSequence(t *testing.T) {
	handle, database := newContractBolt(t)
	originalJSON := getContractValue(t, database, contractBucket, []byte("json"))
	if err := database.Update(func(tx store.WriteTx) error {
		if err := tx.Put(contractBucket, []byte{0, 255}, []byte{128, 0}); err != nil {
			return err
		}
		if err := tx.Put([]byte("created"), []byte("nil"), nil); err != nil {
			return err
		}
		for _, bucket := range [][]byte{contractBucket, []byte("missing")} {
			if err := tx.Delete(bucket, []byte("absent")); err != nil {
				return err
			}
		}
		if err := tx.DeleteBucket([]byte("missing")); err != nil {
			return err
		}
		return tx.DeleteBucket([]byte("other"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := handle.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket(contractBucket)
		child := root.Bucket([]byte("nested"))
		deep := child.Bucket([]byte("deep"))
		if root.Sequence() != math.MaxUint64 || child.Sequence() != 1<<63 || deep.Sequence() != 42 ||
			!bytes.Equal(child.Get([]byte{0, 254}), []byte{255, 0}) || deep.Get([]byte("value")) == nil ||
			!bytes.Equal(root.Get([]byte("json")), originalJSON) || !bytes.Equal(root.Get([]byte("raw")), contractRaw) {
			return errors.New("top-level writes changed unknown values, nested tree or unsigned sequences")
		}
		if tx.Bucket([]byte("missing")) != nil || tx.Bucket([]byte("other")) != nil {
			return errors.New("missing deletes created a bucket or existing delete failed")
		}
		if value := tx.Bucket([]byte("created")).Get([]byte("nil")); value == nil || len(value) != 0 {
			return errors.New("nil Put did not store an existing empty value")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(store.WriteTx) error{
		func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("nested"), []byte("replace")) },
		func(tx store.WriteTx) error { return tx.Delete(contractBucket, []byte("nested")) },
	} {
		if err := database.Update(operation); !errors.Is(err, bbolt.ErrIncompatibleValue) {
			t.Fatalf("nested bucket was replaced/deleted through a value operation: %v", err)
		}
	}
}

func TestBoltUpdateRollbackErrorsAndPanic(t *testing.T) {
	_, database := newContractBolt(t)
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
	assertRolledBack := func() {
		t.Helper()
		if !bytes.Equal(getContractValue(t, database, contractBucket, []byte("raw")), contractRaw) || getContractValue(t, database, []byte("new"), []byte("value")) != nil {
			t.Fatal("failed transaction committed partial writes")
		}
		var names [][]byte
		if err := database.View(func(tx store.ReadTx) error {
			return tx.ForEachBucket(func(name []byte) error { names = append(names, name); return nil })
		}); err != nil || !reflect.DeepEqual(names, [][]byte{contractBucket, []byte("other")}) {
			t.Fatalf("failed transaction changed bucket structure: %q, %v", names, err)
		}
	}
	err := database.Update(func(tx store.WriteTx) error {
		if err := mutate(tx); err != nil {
			return err
		}
		return fmt.Errorf("wrapped: %w", sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error identity was lost: %v", err)
	}
	assertRolledBack()
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
		t.Fatal("Update did not propagate the original panic")
	}
	assertRolledBack()
	err = database.Update(func(tx store.WriteTx) error {
		if err := mutate(tx); err != nil {
			return err
		}
		return tx.Put(contractBucket, nil, []byte("invalid key"))
	})
	if !errors.Is(err, bbolt.ErrKeyRequired) {
		t.Fatalf("backend error identity was lost: %v", err)
	}
	assertRolledBack()
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("after"), []byte("usable")) }); err != nil {
		t.Fatalf("rollback retained the writer lock: %v", err)
	}
}

func TestBoltIterationErrorsAndViewPanic(t *testing.T) {
	_, database := newContractBolt(t)
	sentinel := errors.New("stop iteration")
	for _, iterate := range []func(store.ReadTx, func() error) error{
		func(tx store.ReadTx, visit func() error) error {
			return tx.ForEach(contractBucket, func(_, _ []byte) error { return visit() })
		},
		func(tx store.ReadTx, visit func() error) error {
			return tx.ForEachBucket(func(_ []byte) error { return visit() })
		},
	} {
		calls := 0
		err := database.View(func(tx store.ReadTx) error {
			return iterate(tx, func() error { calls++; return sentinel })
		})
		if !errors.Is(err, sentinel) || calls != 1 {
			t.Fatalf("iteration did not stop and preserve error: calls=%d, err=%v", calls, err)
		}
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = database.View(func(store.ReadTx) error { panic(sentinel) })
	}()
	if recovered != sentinel {
		t.Fatal("View did not propagate panic")
	}
	if err := database.Update(func(tx store.WriteTx) error { return tx.Put(contractBucket, []byte("after"), []byte("usable")) }); err != nil {
		t.Fatalf("View panic retained transaction resources: %v", err)
	}
}

func TestBoltViewDoesNotWaitForOpenWriter(t *testing.T) {
	handle, database := newContractBolt(t)
	writer, err := handle.Begin(true)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if err := writer.Bucket(contractBucket).Put([]byte("raw"), []byte("uncommitted")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CreateBucket([]byte("uncommitted")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- database.View(func(tx store.ReadTx) error {
			value, err := tx.Get(contractBucket, []byte("raw"))
			if err != nil || !bytes.Equal(value, contractRaw) {
				return fmt.Errorf("reader observed uncommitted writer data: %v", err)
			}
			length, err := tx.BucketLen([]byte("uncommitted"))
			if err != nil || length != 0 {
				return fmt.Errorf("reader observed uncommitted bucket: %v", err)
			}
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		_ = writer.Rollback()
		<-done
		t.Fatal("View blocked behind an open writer; it must use a read transaction")
	}
}

func TestBoltViewKeepsOneSnapshotAcrossConcurrentCommit(t *testing.T) {
	_, database := newContractBolt(t)
	if err := database.View(func(tx store.ReadTx) error {
		before, err := tx.Get(contractBucket, []byte("raw"))
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() {
			done <- database.Update(func(writer store.WriteTx) error {
				return writer.Put(contractBucket, []byte("raw"), []byte("committed"))
			})
		}()
		select {
		case err := <-done:
			if err != nil {
				return err
			}
		case <-time.After(3 * time.Second):
			return errors.New("reader blocked writer commit despite preallocated mmap")
		}
		after, err := tx.Get(contractBucket, []byte("raw"))
		if err != nil || !bytes.Equal(before, contractRaw) || !bytes.Equal(after, before) {
			return fmt.Errorf("View mixed snapshots across a concurrent commit: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if actual := getContractValue(t, database, contractBucket, []byte("raw")); string(actual) != "committed" {
		t.Fatal("subsequent View did not observe committed data")
	}
}

func TestBoltClosedAndNilHandlesReturnBackendError(t *testing.T) {
	handle, closed := newContractBolt(t)
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	for _, database := range []store.Store{closed, store.NewBolt(nil)} {
		if err := database.View(func(store.ReadTx) error { t.Fatal("closed View called callback"); return nil }); !errors.Is(err, bbolt.ErrDatabaseNotOpen) {
			t.Fatalf("View did not preserve closed handle error: %v", err)
		}
		if err := database.Update(func(store.WriteTx) error { t.Fatal("closed Update called callback"); return nil }); !errors.Is(err, bbolt.ErrDatabaseNotOpen) {
			t.Fatalf("Update did not preserve closed handle error: %v", err)
		}
	}
}
