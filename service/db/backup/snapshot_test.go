package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSnapshotCanonicalizesAndOwnsCompleteTree(t *testing.T) {
	input := []Bucket{
		{Name: []byte{255, 0}, Sequence: math.MaxUint64, Values: []Value{
			{Key: []byte("z"), Data: []byte(`{"huge":18446744073709551615,"fraction":1.2300,"false":false,"zero":0,"negative":-1}`), SHA256: "untrusted"},
			{Key: []byte{0, 255}, Data: nil},
		}, Buckets: []Bucket{{Name: []byte("z-child")}, {Name: []byte("a-child"), Sequence: 1 << 63, Values: []Value{{Key: []byte("raw"), Data: []byte{0, 255, 1}}}}}},
		{Name: []byte("a-empty")},
	}
	before, _ := json.Marshal(input)
	archive, err := NewSnapshot("sqlite", input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) {
		t.Fatal("snapshot sorted or changed caller-owned input")
	}
	if archive.Format != SnapshotFormatName || archive.Version != SnapshotFormatVersion || archive.Source.Backend != "sqlite" || archive.Source.DigestKind != LogicalDigestKind || archive.Source.SHA256 != archive.Manifest.LogicalSHA256 {
		t.Fatal("snapshot source contract is incorrect")
	}
	if string(archive.Buckets[0].Name) != "a-empty" || archive.Buckets[1].Sequence != math.MaxUint64 || string(archive.Buckets[1].Buckets[0].Name) != "a-child" || archive.Buckets[1].Buckets[0].Sequence != 1<<63 {
		t.Fatal("bucket order or unsigned sequences changed")
	}
	if value := archive.Buckets[1].Values[0]; !bytes.Equal(value.Key, []byte{0, 255}) || value.Data == nil || len(value.Data) != 0 || value.SHA256 != digest(nil) {
		t.Fatal("empty non-UTF8 keyed value was lost")
	}
	if archive.Buckets[1].Values[1].SHA256 != digest(input[0].Values[0].Data) || archive.Manifest.Buckets != 4 || archive.Manifest.Values != 3 {
		t.Fatal("snapshot did not rebuild hashes or counts")
	}
	ownedBefore, _ := json.Marshal(archive)
	input[0].Name[0] = 1
	input[0].Values[0].Key[0] = 'x'
	input[0].Values[0].Data[0] = '!'
	input[0].Buckets[1].Values[0].Data[0] = 9
	ownedAfter, _ := json.Marshal(archive)
	if !bytes.Equal(ownedBefore, ownedAfter) || Validate(archive) != nil {
		t.Fatal("snapshot retained caller-owned byte slices")
	}
}

func TestSnapshotRoundTripRestoresNewBoltWithSameLogicalManifest(t *testing.T) {
	legacy := fixtureArchive(t)
	legacyBytes, _ := json.Marshal(legacy)
	archive, err := NewSnapshot("sqlite", legacy.Buckets)
	if err != nil || archive.Manifest != legacy.Manifest {
		t.Fatalf("snapshot changed logical identity: %v", err)
	}
	path := filepath.Join(t.TempDir(), "snapshot-v2.json")
	if err := Write(path, archive); err != nil {
		t.Fatal(err)
	}
	read, err := Read(path)
	if err != nil || !reflect.DeepEqual(archive, read) {
		t.Fatalf("v2 wire round trip: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(destination, read); err != nil {
		t.Fatal(err)
	}
	back, err := Export(destination, time.Second)
	if err != nil || back.Manifest != archive.Manifest || !reflect.DeepEqual(back.Buckets, archive.Buckets) {
		t.Fatalf("v2 to Bolt lost raw tree content: %v", err)
	}
	if back.Version != FormatVersion || back.Format != FormatName || back.Source.Backend != "bbolt" || back.Source.DigestKind != "" {
		t.Fatal("existing Bolt Export no longer uses the original v1 contract")
	}
	after, _ := json.Marshal(legacy)
	if !bytes.Equal(legacyBytes, after) {
		t.Fatal("snapshot modified the legacy archive")
	}
	if err := Restore(destination, read); !errors.Is(err, os.ErrExist) {
		t.Fatalf("v2 restore overwrote an existing file: %v", err)
	}
}

func TestSnapshotSourceContractsRejectAmbiguity(t *testing.T) {
	archive, err := NewSnapshot("sqlite", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Archive){
		"missing kind":    func(a *Archive) { a.Source.DigestKind = "" },
		"physical kind":   func(a *Archive) { a.Source.DigestKind = "file-sha256" },
		"wrong digest":    func(a *Archive) { a.Source.SHA256 = strings.Repeat("0", 64) },
		"unknown backend": func(a *Archive) { a.Source.Backend = "unknown" },
		"legacy format":   func(a *Archive) { a.Format = FormatName },
		"legacy version":  func(a *Archive) { a.Version = FormatVersion },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := copyArchive(t, archive)
			edit(invalid)
			if err := Validate(invalid); err == nil {
				t.Fatal("ambiguous snapshot source was accepted")
			}
		})
	}
	if _, err := NewSnapshot("bbolt", nil); err != nil {
		t.Fatalf("backend-neutral Bolt snapshot failed: %v", err)
	}
	if _, err := NewSnapshot("unknown", nil); err == nil {
		t.Fatal("unknown snapshot backend accepted")
	}
	legacy := fixtureArchive(t)
	legacy.Source.DigestKind = LogicalDigestKind
	if err := Validate(legacy); err == nil {
		t.Fatal("v2 source metadata accepted by frozen v1 contract")
	}
	legacy.Source.DigestKind = ""
	raw, _ := json.Marshal(legacy)
	raw = bytes.Replace(raw, []byte(`"source":{`), []byte(`"source":{"digestKind":"",`), 1)
	path := filepath.Join(t.TempDir(), "invalid-v1.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("v1 wire format accepted a newly introduced field")
	}
}

func TestSnapshotRejectsInvalidOrExcessiveTrees(t *testing.T) {
	cycle := []Bucket{{Name: []byte("cycle")}}
	cycle[0].Buckets = cycle
	for name, buckets := range map[string][]Bucket{
		"empty name":             {{Name: nil}},
		"duplicate bucket":       {{Name: []byte("same")}, {Name: []byte("same")}},
		"duplicate key":          {{Name: []byte("root"), Values: []Value{{Key: []byte("same")}, {Key: []byte("same")}}}},
		"bucket key collision":   {{Name: []byte("root"), Buckets: []Bucket{{Name: []byte("same")}}, Values: []Value{{Key: []byte("same")}}}},
		"empty key":              {{Name: []byte("root"), Values: []Value{{}}}},
		"entry limit":            {{Name: []byte("root"), Values: make([]Value, MaxEntries)}},
		"recursive caller input": cycle,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSnapshot("sqlite", buckets); err == nil {
				t.Fatal("invalid tree accepted")
			}
		})
	}
}
