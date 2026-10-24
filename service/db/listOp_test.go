package db

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.etcd.io/bbolt"
)

// All database tests use a private Bolt file, never the configured user DB.
func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "v2raya-db-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	db, err = bbolt.Open(filepath.Join(directory, "bolt.db"), 0600, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(directory)
		os.Exit(1)
	}
	once.Do(func() {})
	code := m.Run()
	db.Close()
	os.RemoveAll(directory)
	os.Exit(code)
}

func TestListRemovePreservesNeighborJSON(t *testing.T) {
	server := json.RawMessage(`{"serverObj":{"protocol":"vless","net":"xhttp","xhttpExtra":{"future":9007199254740993,"huge":9223372036854775807,"fraction":1.2300,"noGRPCHeader":false,"scMinPostsIntervalMs":0,"xmux":{"hKeepAlivePeriod":-1}}}}`)
	subscription := json.RawMessage(`{"address":"https://example.com/sub","servers":[` + string(server) + `]}`)
	cleared := json.RawMessage(`{"serverObj":{"protocol":"vless","net":"xhttp","xhttpExtra":{}}}`)
	fixtures := []json.RawMessage{server, subscription, cleared}
	for _, tt := range []struct {
		name      string
		indexes   []int
		remaining []int
	}{
		{name: "remove first", indexes: []int{0}, remaining: []int{1, 2}},
		{name: "remove last", indexes: []int{2}, remaining: []int{0, 1}},
		{name: "remove middle twice", indexes: []int{1, 1}, remaining: []int{0, 2}},
		{name: "remove both neighbors", indexes: []int{2, 0}, remaining: []int{1}},
		{name: "remove all", indexes: []int{0, 1, 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := ListAppend(t.Name(), "nodes", fixtures); err != nil {
				t.Fatal(err)
			}
			if err := ListRemove(t.Name(), "nodes", tt.indexes); err != nil {
				t.Fatal(err)
			}
			got, err := ListGetAll(t.Name(), "nodes")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.remaining) {
				t.Fatalf("got %d nodes, want %d", len(got), len(tt.remaining))
			}
			for i, index := range tt.remaining {
				if string(got[i]) != string(fixtures[index]) {
					t.Fatalf("remaining node changed:\n%s\nwant:\n%s", got[i], fixtures[index])
				}
			}
		})
	}
}
