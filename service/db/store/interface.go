// Package store defines the top-level bucket operations used by the runtime.
// It does not select a backend or open the application's database.
package store

// Store runs each callback in one consistent transaction. Returning an error
// from Update rolls back all changes; a panic also rolls back and propagates.
// Callbacks must use their supplied transaction, not start another transaction.
type Store interface {
	View(func(ReadTx) error) error
	Update(func(WriteTx) error) error
}

// ReadTx is valid only during its callback. Missing buckets read as empty and
// reads never create buckets. Bytes returned by Get or passed to callbacks are
// owned by the caller and remain valid after the transaction ends.
type ReadTx interface {
	// Get returns nil for a missing key or a nested bucket, and a non-nil empty
	// slice for an existing empty value.
	Get(bucket, key []byte) ([]byte, error)
	// ForEach visits direct entries in byte order. A nested bucket has nil
	// value; an existing empty value is non-nil. Do not modify the bucket while
	// iterating it. Callback errors stop iteration and are returned unchanged.
	ForEach(bucket []byte, fn func(key, value []byte) error) error
	ForEachBucket(fn func(name []byte) error) error
	// BucketLen counts keys recursively, including nested bucket entries,
	// matching bbolt.Bucket.Stats().KeyN rather than just direct values.
	BucketLen(bucket []byte) (int, error)
}

// WriteTx can change top-level buckets. Untouched nested buckets, unknown
// values, and bucket sequences are preserved. Missing deletes are no-ops.
type WriteTx interface {
	ReadTx
	// Put creates the bucket if absent. A nil value stores an empty value.
	// The supplied value must remain unchanged until the callback returns.
	Put(bucket, key, value []byte) error
	Delete(bucket, key []byte) error
	DeleteBucket(bucket []byte) error
}
