package configure

import (
	"encoding/json"
	"strconv"
	"go.etcd.io/bbolt"
	"github.com/v2rayA/v2rayA/db"
)

type DockerProxy struct {
	Port            int    `json:"port"`
	Which           Which  `json:"which"`
	FrontWhich      *Which `json:"frontWhich,omitempty"`
	ServerName      string `json:"serverName"`
	FrontServerName string `json:"frontServerName,omitempty"`
	ContainerName   string `json:"containerName"`
	Status          string `json:"status"`
}

func GetDockerProxies() ([]DockerProxy, error) {
	var list []DockerProxy
	err := db.Transaction(db.DB(), func(tx *bbolt.Tx) (bool, error) {
		dirty := false
		bkt, err := db.CreateBucketIfNotExists(tx, []byte("dockerProxies"), &dirty)
		if err != nil {
			return dirty, err
		}
		err = bkt.ForEach(func(k, v []byte) error {
			var dp DockerProxy
			if err := json.Unmarshal(v, &dp); err == nil {
				list = append(list, dp)
			}
			return nil
		})
		return dirty, err
	})
	return list, err
}

func SaveDockerProxy(dp DockerProxy) error {
	b, err := json.Marshal(dp)
	if err != nil {
		return err
	}
	return db.DB().Update(func(tx *bbolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte("dockerProxies"))
		if err != nil {
			return err
		}
		return bkt.Put([]byte(strconv.Itoa(dp.Port)), b)
	})
}

func RemoveDockerProxy(port int) error {
	return db.DB().Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte("dockerProxies"))
		if bkt == nil {
			return nil
		}
		return bkt.Delete([]byte(strconv.Itoa(port)))
	})
}
