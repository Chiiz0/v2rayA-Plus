package store

import (
	"bytes"
	"errors"

	"go.etcd.io/bbolt"
)

// NewBolt wraps an existing handle without taking ownership of its lifetime.
// The caller opens and closes the handle and manages its file/instance locks.
func NewBolt(handle *bbolt.DB) Store {
	return boltStore{handle: handle}
}

type boltStore struct {
	handle *bbolt.DB
}

func (store boltStore) View(fn func(ReadTx) error) error {
	if store.handle == nil {
		return bbolt.ErrDatabaseNotOpen
	}
	return store.handle.View(func(tx *bbolt.Tx) error {
		// Do not pass boltWriteTx behind the read interface: callers must not
		// be able to regain write access with a type assertion.
		return fn(boltReadTx{tx: tx})
	})
}

func (store boltStore) Update(fn func(WriteTx) error) error {
	if store.handle == nil {
		return bbolt.ErrDatabaseNotOpen
	}
	return store.handle.Update(func(tx *bbolt.Tx) error {
		return fn(boltWriteTx{boltReadTx{tx: tx}})
	})
}

type boltReadTx struct {
	tx *bbolt.Tx
}

func (tx boltReadTx) Get(bucket, key []byte) ([]byte, error) {
	bkt := tx.tx.Bucket(bucket)
	if bkt == nil {
		return nil, nil
	}
	return bytes.Clone(bkt.Get(key)), nil
}

func (tx boltReadTx) ForEach(bucket []byte, fn func(key, value []byte) error) error {
	bkt := tx.tx.Bucket(bucket)
	if bkt == nil {
		return nil
	}
	return bkt.ForEach(func(key, value []byte) error {
		return fn(bytes.Clone(key), bytes.Clone(value))
	})
}

func (tx boltReadTx) ForEachBucket(fn func(name []byte) error) error {
	return tx.tx.ForEach(func(name []byte, _ *bbolt.Bucket) error {
		return fn(bytes.Clone(name))
	})
}

func (tx boltReadTx) BucketLen(bucket []byte) (int, error) {
	bkt := tx.tx.Bucket(bucket)
	if bkt == nil {
		return 0, nil
	}
	return bkt.Stats().KeyN, nil
}

type boltWriteTx struct {
	boltReadTx
}

func (tx boltWriteTx) Put(bucket, key, value []byte) error {
	bkt, err := tx.tx.CreateBucketIfNotExists(bucket)
	if err != nil {
		return err
	}
	return bkt.Put(key, value)
}

func (tx boltWriteTx) Delete(bucket, key []byte) error {
	bkt := tx.tx.Bucket(bucket)
	if bkt == nil {
		return nil
	}
	return bkt.Delete(key)
}

func (tx boltWriteTx) DeleteBucket(bucket []byte) error {
	err := tx.tx.DeleteBucket(bucket)
	if errors.Is(err, bbolt.ErrBucketNotFound) {
		return nil
	}
	return err
}

var _ Store = boltStore{}
var _ ReadTx = boltReadTx{}
var _ WriteTx = boltWriteTx{}
