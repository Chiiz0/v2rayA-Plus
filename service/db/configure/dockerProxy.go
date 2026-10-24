package configure

import (
	"encoding/json"
	"strconv"

	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/store"
)

type DockerProxyCore string

const (
	DockerProxyCoreAuto  DockerProxyCore = "auto"
	DockerProxyCoreV2Fly DockerProxyCore = "v2fly"
	DockerProxyCoreXray  DockerProxyCore = "xray"
)

type DockerProxy struct {
	Port            int             `json:"port"`
	Which           Which           `json:"which"`
	FrontWhich      *Which          `json:"frontWhich,omitempty"`
	Core            DockerProxyCore `json:"core"`
	ServerName      string          `json:"serverName"`
	FrontServerName string          `json:"frontServerName,omitempty"`
	ContainerName   string          `json:"containerName"`
	Status          string          `json:"status"`
}

func GetDockerProxies() ([]DockerProxy, error) {
	database, err := db.CurrentStore()
	if err != nil {
		return nil, err
	}
	return getDockerProxies(database)
}

func getDockerProxies(database store.Store) ([]DockerProxy, error) {
	var list []DockerProxy
	err := database.View(func(tx store.ReadTx) error {
		return tx.ForEach([]byte("dockerProxies"), func(k, v []byte) error {
			dp, err := decodeDockerProxy(k, v)
			if err != nil {
				return err
			}
			list = append(list, dp)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return list, err
}

func SaveDockerProxy(dp DockerProxy) error {
	database, err := db.CurrentStore()
	if err != nil {
		return err
	}
	return saveDockerProxy(database, dp)
}

func saveDockerProxy(database store.Store, dp DockerProxy) error {
	b, err := json.Marshal(dp)
	if err != nil {
		return err
	}
	return database.Update(func(tx store.WriteTx) error {
		return tx.Put([]byte("dockerProxies"), []byte(strconv.Itoa(dp.Port)), b)
	})
}

func RemoveDockerProxy(port int) error {
	database, err := db.CurrentStore()
	if err != nil {
		return err
	}
	return removeDockerProxy(database, port)
}

func removeDockerProxy(database store.Store, port int) error {
	return database.Update(func(tx store.WriteTx) error {
		return tx.Delete([]byte("dockerProxies"), []byte(strconv.Itoa(port)))
	})
}
