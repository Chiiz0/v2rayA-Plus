package configure

import (
	"errors"
	"reflect"
	"testing"

	"github.com/v2rayA/v2rayA/db/store"
	"go.etcd.io/bbolt"
)

func TestDockerProxyStorePreservesMissingAndCRUDSemantics(t *testing.T) {
	database := referenceTestDB(t)
	backend := store.NewBolt(database)
	list, err := getDockerProxies(backend)
	if err != nil || list != nil {
		t.Fatalf("missing list = %v, %v", list, err)
	}
	if err := removeDockerProxy(backend, 1080); err != nil {
		t.Fatal(err)
	}
	if contents := referenceTestContents(t, database); len(contents) != 0 {
		t.Fatal("reading or removing a missing Docker record created a bucket")
	}
	proxy := DockerProxy{Port: 1080, Which: Which{TYPE: ServerType, ID: 2}, Core: DockerProxyCoreXray, Status: "paused", ContainerName: "v2raya-socks-1080"}
	checked := &referenceCheckedStore{Store: backend}
	if err := saveDockerProxy(checked, proxy); err != nil {
		t.Fatal(err)
	}
	if checked.views != 0 || checked.updates != 1 {
		t.Fatalf("save used %d views and %d updates", checked.views, checked.updates)
	}
	list, err = getDockerProxies(backend)
	if err != nil || !reflect.DeepEqual(list, []DockerProxy{proxy}) {
		t.Fatalf("saved list = %v, %v", list, err)
	}
	before := referenceTestContents(t, database)
	for _, operation := range []string{"put", "delete"} {
		t.Run(operation+" failure", func(t *testing.T) {
			injected := errors.New("injected Docker record write failure")
			checked := &referenceCheckedStore{Store: backend, fault: func(op, bucket, key string) error {
				if op == operation && bucket == "dockerProxies" && key == "1080" {
					return injected
				}
				return nil
			}}
			var err error
			if operation == "put" {
				changed := proxy
				changed.Status = "running"
				err = saveDockerProxy(checked, changed)
			} else {
				err = removeDockerProxy(checked, proxy.Port)
			}
			if !errors.Is(err, injected) {
				t.Fatalf("write error = %v, want original store error", err)
			}
			if checked.views != 0 || checked.updates != 1 || !reflect.DeepEqual(before, referenceTestContents(t, database)) {
				t.Fatal("failed Docker operation escaped its single transaction")
			}
		})
	}
	if err := removeDockerProxy(backend, proxy.Port); err != nil {
		t.Fatal(err)
	}
	list, err = getDockerProxies(backend)
	if err != nil || len(list) != 0 {
		t.Fatalf("removed list = %v, %v", list, err)
	}
}

func TestDockerProxyStoreRejectsNestedRecordWithoutChangingIt(t *testing.T) {
	database := referenceTestDB(t)
	if err := database.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("dockerProxies"))
		if err != nil {
			return err
		}
		_, err = bucket.CreateBucket([]byte("1080"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := referenceTestContents(t, database)
	if list, err := getDockerProxies(store.NewBolt(database)); err == nil || list != nil {
		t.Fatalf("nested record list = %v, %v", list, err)
	}
	if !reflect.DeepEqual(before, referenceTestContents(t, database)) {
		t.Fatal("Docker record read changed a nested bucket")
	}
}
