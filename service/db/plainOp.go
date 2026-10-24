package db

import (
	"fmt"

	jsoniter "github.com/json-iterator/go"
	"github.com/v2rayA/v2rayA/db/store"
	"github.com/v2rayA/v2rayA/pkg/util/log"
)

func Get(bucket string, key string, val interface{}) (err error) {
	return View(func(tx store.ReadTx) error {
		v, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		if v == nil {
			return fmt.Errorf("Get: key is not found")
		}
		return jsoniter.Unmarshal(v, val)
	})
}

func GetRaw(bucket string, key string) (b []byte, err error) {
	err = View(func(tx store.ReadTx) error {
		b, err = tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		if b == nil {
			return fmt.Errorf("GetRaw: key is not found")
		}
		return nil
	})
	return b, err
}

func Exists(bucket string, key string) (exists bool) {
	if err := View(func(tx store.ReadTx) error {
		v, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		exists = v != nil
		return nil
	}); err != nil {
		log.Warn("%v", err)
		return false
	}
	return exists
}

func GetBucketLen(bucket string) (length int, err error) {
	err = View(func(tx store.ReadTx) error {
		length, err = tx.BucketLen([]byte(bucket))
		return err
	})
	return length, err
}

func GetBucketKeys(bucket string) (keys []string, err error) {
	err = View(func(tx store.ReadTx) error {
		return tx.ForEach([]byte(bucket), func(k, v []byte) error {
			keys = append(keys, string(k))
			return nil
		})
	})
	return keys, err
}

func Set(bucket string, key string, val interface{}) (err error) {
	b, err := jsoniter.Marshal(val)
	if err != nil {
		return
	}
	return Update(func(tx store.WriteTx) error {
		return tx.Put([]byte(bucket), []byte(key), b)
	})
}

func BucketClear(bucket string) error {
	return Update(func(tx store.WriteTx) error {
		return tx.DeleteBucket([]byte(bucket))
	})
}
