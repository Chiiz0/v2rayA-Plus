package db

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

func helperTransactionID(t *testing.T) int {
	t.Helper()
	var id int
	if err := database.handle.View(func(tx *bbolt.Tx) error {
		id = tx.ID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func helperPutRaw(t *testing.T, bucket, key string, value []byte) {
	t.Helper()
	if err := Update(func(tx store.WriteTx) error {
		return tx.Put([]byte(bucket), []byte(key), value)
	}); err != nil {
		t.Fatal(err)
	}
}

func helperRequireRaw(t *testing.T, bucket, key string, want []byte) {
	t.Helper()
	got, err := GetRaw(bucket, key)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("stored bytes changed: %v; got %q, want %q", err, got, want)
	}
}

func TestHelperMissingReadsDoNotCreateBucketsOrWrite(t *testing.T) {
	bucket := t.Name()
	before := helperTransactionID(t)
	var value any
	if err := Get(bucket, "missing", &value); err == nil {
		t.Fatal("missing JSON value was accepted")
	}
	if value, err := GetRaw(bucket, "missing"); err == nil || value != nil {
		t.Fatal("missing raw value was accepted")
	}
	if Exists(bucket, "missing") {
		t.Fatal("missing value exists")
	}
	if length, err := GetBucketLen(bucket); err != nil || length != 0 {
		t.Fatalf("missing bucket length: %d, %v", length, err)
	}
	if keys, err := GetBucketKeys(bucket); err != nil || len(keys) != 0 {
		t.Fatalf("missing bucket keys: %v, %v", keys, err)
	}
	if value, err := ListGet(bucket, "missing", 0); err == nil || value != nil {
		t.Fatal("missing list element was accepted")
	}
	if values, err := ListGetAll(bucket, "missing"); err != nil || len(values) != 0 {
		t.Fatalf("missing list contents: %v, %v", values, err)
	}
	if length, err := ListLen(bucket, "missing"); err != nil || length != 0 {
		t.Fatalf("missing list length: %d, %v", length, err)
	}
	if values, err := StringSetGetAll(bucket, "missing"); err != nil || len(values) != 0 {
		t.Fatalf("missing set contents: %v, %v", values, err)
	}
	if after := helperTransactionID(t); after != before {
		t.Fatal("a read helper committed a write transaction")
	}
	if err := database.handle.View(func(tx *bbolt.Tx) error {
		if tx.Bucket([]byte(bucket)) != nil {
			t.Error("a read helper created the missing bucket")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHelperPlainReadsOwnBytesAndPreserveEmptyValues(t *testing.T) {
	bucket := t.Name()
	value := json.RawMessage(`{"large":9007199254740993,"fraction":1.2300,"false":false,"zero":0,"negative":-1}`)
	if err := Set(bucket, "z-json", value); err != nil {
		t.Fatal(err)
	}
	helperPutRaw(t, bucket, "a-empty", []byte{})
	before := helperTransactionID(t)
	var decoded json.RawMessage
	if err := Get(bucket, "z-json", &decoded); err != nil || !bytes.Equal(decoded, value) {
		t.Fatalf("JSON payload changed: %v, %q", err, decoded)
	}
	raw, err := GetRaw(bucket, "z-json")
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = '!'
	helperRequireRaw(t, bucket, "z-json", value)
	empty, err := GetRaw(bucket, "a-empty")
	if err != nil || empty == nil || len(empty) != 0 || !Exists(bucket, "a-empty") {
		t.Fatalf("empty value confused with missing value: %v, %v", empty, err)
	}
	if err := Get(bucket, "a-empty", &decoded); err == nil {
		t.Fatal("empty raw value unexpectedly decoded as JSON")
	}
	if keys, err := GetBucketKeys(bucket); err != nil || !reflect.DeepEqual(keys, []string{"a-empty", "z-json"}) {
		t.Fatalf("key byte order changed: %v, %v", keys, err)
	}
	if length, err := GetBucketLen(bucket); err != nil || length != 2 {
		t.Fatalf("bucket length: %d, %v", length, err)
	}
	if helperTransactionID(t) != before {
		t.Fatal("existing-data reads committed a write")
	}
	if err := BucketClear(bucket); err != nil {
		t.Fatal(err)
	}
	if err := BucketClear(bucket); err != nil || Exists(bucket, "z-json") {
		t.Fatalf("clearing a missing bucket failed: %v", err)
	}
}

func TestHelperListMutationPreservesRawNeighborsAndRollsBack(t *testing.T) {
	bucket := t.Name()
	first := json.RawMessage(`{"large":9007199254740993,"fraction":1.2300,"disabled":false}`)
	second := json.RawMessage(`{"nested":{"negative":-1,"empty":{}}}`)
	if err := ListAppend(bucket, "list", []json.RawMessage{first, second}); err != nil {
		t.Fatal(err)
	}
	if err := ListAppend(bucket, "list", nil); err != nil {
		t.Fatal(err)
	}
	if err := ListSet(bucket, "list", 1, json.RawMessage(`{"changed":true}`)); err != nil {
		t.Fatal(err)
	}
	before := helperTransactionID(t)
	values, err := ListGetAll(bucket, "list")
	if err != nil || len(values) != 3 || !bytes.Equal(values[0], first) || string(values[1]) != `{"changed":true}` || string(values[2]) != "null" {
		t.Fatalf("list mutation changed neighbor bytes/order: %q, %v", values, err)
	}
	if got, err := ListGet(bucket, "list", 0); err != nil || !bytes.Equal(got, first) {
		t.Fatalf("list element lookup changed raw JSON: %q, %v", got, err)
	}
	if length, err := ListLen(bucket, "list"); err != nil || length != 3 {
		t.Fatalf("list length: %d, %v", length, err)
	}
	if _, err := ListGet(bucket, "list", 4); err == nil {
		t.Fatal("out-of-range element accepted")
	}
	if helperTransactionID(t) != before {
		t.Fatal("list reads committed a write")
	}
	original, err := GetRaw(bucket, "list")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() error{
		"partial append":      func() error { return ListAppend(bucket, "list", []any{"would append", make(chan int)}) },
		"invalid replacement": func() error { return ListSet(bucket, "list", 0, make(chan int)) },
		"invalid removal":     func() error { return ListRemove(bucket, "list", []int{0, 99}) },
		"empty removal":       func() error { return ListRemove(bucket, "list", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); err == nil {
				t.Fatal("invalid list mutation succeeded")
			}
			helperRequireRaw(t, bucket, "list", original)
		})
	}
	missing := bucket + "-missing"
	if err := ListSet(missing, "list", 0, "value"); err == nil {
		t.Fatal("missing list was set")
	}
	if err := ListRemove(missing, "list", []int{0}); err == nil {
		t.Fatal("missing list was removed")
	}
	if err := Set(missing, "value", make(chan int)); err == nil {
		t.Fatal("invalid plain value was stored")
	}
	if err := database.handle.View(func(tx *bbolt.Tx) error {
		if tx.Bucket([]byte(missing)) != nil {
			t.Error("failed mutation created a missing bucket")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	helperPutRaw(t, bucket, "not-array", []byte(`{"number":9007199254740993}`))
	if _, err := ListGetAll(bucket, "not-array"); err == nil {
		t.Fatal("non-array value accepted by ListGetAll")
	}
	if _, err := ListLen(bucket, "not-array"); err == nil {
		t.Fatal("non-array value accepted by ListLen")
	}
}

func TestHelperSetPreservesGobAndRejectsNonStringMembers(t *testing.T) {
	bucket := t.Name()
	legacy := make(set)
	for _, value := range []string{"", "中文", "outbound-a"} {
		hash, err := toSha256(value)
		if err != nil {
			t.Fatal(err)
		}
		legacy[hash] = value
	}
	encoded, err := common.ToBytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	helperPutRaw(t, bucket, "names", encoded)
	before := helperTransactionID(t)
	members, err := StringSetGetAll(bucket, "names")
	sort.Strings(members)
	if err != nil || !reflect.DeepEqual(members, []string{"", "outbound-a", "中文"}) {
		t.Fatalf("legacy gob set changed: %v, %v", members, err)
	}
	helperRequireRaw(t, bucket, "names", encoded)
	if helperTransactionID(t) != before {
		t.Fatal("reading a gob set committed a write")
	}
	if err := SetAdd(bucket, "names", "中文"); err != nil {
		t.Fatal(err)
	}
	if err := SetAdd(bucket, "names", "new"); err != nil {
		t.Fatal(err)
	}
	if err := SetRemove(bucket, "names", "outbound-a"); err != nil {
		t.Fatal(err)
	}
	members, err = StringSetGetAll(bucket, "names")
	sort.Strings(members)
	if err != nil || !reflect.DeepEqual(members, []string{"", "new", "中文"}) {
		t.Fatalf("set membership changed: %v, %v", members, err)
	}
	badType, err := common.ToBytes(set{[32]byte{}: int64(9007199254740993)})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{"non-string": badType, "malformed": []byte("broken gob")} {
		t.Run(name, func(t *testing.T) {
			helperPutRaw(t, bucket, name, value)
			before := helperTransactionID(t)
			if members, err := StringSetGetAll(bucket, name); err == nil || members != nil {
				t.Fatal("invalid string set did not return an error with no members")
			}
			helperRequireRaw(t, bucket, name, value)
			if helperTransactionID(t) != before {
				t.Fatal("invalid set read committed a write")
			}
		})
	}
	if err := SetAdd(bucket, "malformed", "value"); err == nil {
		t.Fatal("invalid gob was silently replaced by SetAdd")
	}
	if err := SetRemove(bucket, "malformed", "value"); err == nil {
		t.Fatal("invalid gob was silently replaced by SetRemove")
	}
	helperRequireRaw(t, bucket, "malformed", []byte("broken gob"))
}
