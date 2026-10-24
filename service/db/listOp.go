package db

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	jsoniter "github.com/json-iterator/go"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/v2rayA/v2rayA/db/store"
)

func ListSet(bucket string, key string, index int, val interface{}) (err error) {
	return Update(func(tx store.WriteTx) error {
		b, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		if b == nil {
			return fmt.Errorf("ListSet: can't set element to an empty list")
		}

		if b, e := sjson.SetBytes(b, strconv.Itoa(index), val); e != nil {
			return e
		} else {
			return tx.Put([]byte(bucket), []byte(key), b)
		}
	})
}

func ListGet(bucket string, key string, index int) (b []byte, err error) {
	err = View(func(tx store.ReadTx) error {
		v, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		if v == nil {
			return fmt.Errorf("ListGet: can't get element from an empty list")
		}
		r := gjson.GetBytes(v, strconv.Itoa(index))
		if r.Exists() {
			b = []byte(r.Raw)
			return nil
		} else {
			return fmt.Errorf("ListGet: no such element")
		}
	})
	return b, err
}

func ListAppend(bucket string, key string, val interface{}) (err error) {
	return Update(func(tx store.WriteTx) error {
		bList, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		if bList == nil {
			bList = []byte("[]")
		}
		v := reflect.ValueOf(val)
		if v.Kind() == reflect.Slice {
			sliceLen := v.Len()
			for i := 0; i < sliceLen; i++ {
				if bList, err = sjson.SetBytes(bList, "-1", v.Index(i).Interface()); err != nil {
					return err
				}
			}
		} else {
			if bList, err = sjson.SetBytes(bList, "-1", val); err != nil {
				return err
			}
		}
		return tx.Put([]byte(bucket), []byte(key), bList)
	})
}

func ListGetAll(bucket string, key string) (list [][]byte, err error) {
	err = View(func(tx store.ReadTx) error {
		b, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		if b == nil {
			return nil
		}
		parsed := gjson.ParseBytes(b)
		if !parsed.IsArray() {
			return fmt.Errorf("ListGetAll: is not array")
		}
		results := parsed.Array()
		for _, r := range results {
			list = append(list, []byte(r.Raw))
		}
		return nil
	})
	return list, err
}

func ListRemove(bucket, key string, indexes []int) error {
	if len(indexes) == 0 {
		return fmt.Errorf("ListRemove: nothing to remove")
	}
	return Update(func(tx store.WriteTx) error {
		b, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}

		// Keep untouched entries verbatim; decoding into interface{} rounds
		// unknown node parameters above float64's integer precision.
		var list []json.RawMessage
		if err := jsoniter.Unmarshal(b, &list); err != nil {
			return err
		}
		sort.Ints(indexes)
		maxIndexToDelete := indexes[len(indexes)-1]
		if maxIndexToDelete >= len(list) || indexes[0] < 0 {
			return fmt.Errorf("ListRemove: index out of range")
		}
		j := 1
		dist := 1
		for i := indexes[0]; i+dist < len(list); i++ {
			for j < len(indexes) && i+dist >= indexes[j] {
				if indexes[j] != indexes[j-1] {
					dist++
				}
				j++
			}
			if i+dist >= len(list) {
				break
			}
			list[i] = list[i+dist]
		}
		list = list[:len(list)-dist]

		b, err = jsoniter.Marshal(list)
		if err != nil {
			return err
		}
		return tx.Put([]byte(bucket), []byte(key), b)
	})
}

func ListLen(bucket string, key string) (length int, err error) {
	err = View(func(tx store.ReadTx) error {
		b, err := tx.Get([]byte(bucket), []byte(key))
		if err != nil {
			return err
		}
		if b == nil {
			return nil
		}
		parsed := gjson.ParseBytes(b)
		if !parsed.IsArray() {
			return fmt.Errorf("ListLen: is not array")
		}
		length = len(parsed.Array())
		return nil
	})
	return length, err
}
