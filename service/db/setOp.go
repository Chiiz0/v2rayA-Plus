package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"fmt"

	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/db/store"
)

type set map[[32]byte]interface{}

func bytes2Sha256(b []byte) [32]byte {
	h := sha256.New()
	h.Write(b)
	var hash [32]byte
	copy(hash[:], h.Sum(nil))
	return hash
}

func toSha256(val interface{}) (hash [32]byte, err error) {
	b, err := common.ToBytes(val)
	if err != nil {
		return hash, err
	}
	hash = bytes2Sha256(b)
	return hash, nil
}

func readSet(tx store.ReadTx, bucket, key string) (set, error) {
	v, err := tx.Get([]byte(bucket), []byte(key))
	if err != nil {
		return nil, err
	}
	m := make(set)
	if v != nil {
		if err := gob.NewDecoder(bytes.NewReader(v)).Decode(&m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func setOp(bucket string, key string, f func(m set)) error {
	return Update(func(tx store.WriteTx) error {
		m, err := readSet(tx, bucket, key)
		if err != nil {
			return err
		}
		f(m)
		b, err := common.ToBytes(m)
		if err != nil {
			return err
		}
		return tx.Put([]byte(bucket), []byte(key), b)
	})
}

func SetAdd(bucket string, key string, val interface{}) (err error) {
	h, err := toSha256(val)
	if err != nil {
		return err
	}
	return setOp(bucket, key, func(m set) {
		m[h] = val
	})
}

func SetRemove(bucket string, key string, val interface{}) (err error) {
	h, err := toSha256(val)
	if err != nil {
		return err
	}
	return setOp(bucket, key, func(m set) {
		if _, ok := m[h]; ok {
			delete(m, h)
		}
	})
}

func StringSetGetAll(bucket string, key string) (members []string, err error) {
	err = View(func(tx store.ReadTx) error {
		m, err := readSet(tx, bucket, key)
		if err != nil {
			return err
		}
		for _, v := range m {
			member, ok := v.(string)
			if !ok {
				return fmt.Errorf("StringSetGetAll: member is not a string")
			}
			members = append(members, member)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return members, nil
}
