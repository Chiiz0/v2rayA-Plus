package backup

import (
	"encoding/json"
	"strings"
	"testing"

	"go.etcd.io/bbolt"
)

func copyArchive(t *testing.T, archive *Archive) *Archive {
	t.Helper()
	data, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	var copy Archive
	if err := json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	return &copy
}

func TestValidateArchiveDetectsTamperingAndInvalidStructure(t *testing.T) {
	original := fixtureArchive(t)
	for _, test := range []struct {
		name string
		edit func(*Archive)
	}{
		{"format", func(a *Archive) { a.Format = "other" }},
		{"version", func(a *Archive) { a.Version++ }},
		{"creation time", func(a *Archive) { a.CreatedAt = "yesterday" }},
		{"backend", func(a *Archive) { a.Source.Backend = "sqlite" }},
		{"source digest", func(a *Archive) { a.Source.SHA256 = "bad" }},
		{"uppercase digest", func(a *Archive) { a.Source.SHA256 = strings.ToUpper(a.Source.SHA256) }},
		{"value count", func(a *Archive) { a.Manifest.Values++ }},
		{"bucket count", func(a *Archive) { a.Manifest.Buckets++ }},
		{"value bytes", func(a *Archive) { a.Manifest.ValueBytes++ }},
		{"logical digest", func(a *Archive) { a.Manifest.LogicalSHA256 = strings.Repeat("0", 64) }},
		{"sequence", func(a *Archive) { a.Buckets[0].Sequence++ }},
		{"value data", func(a *Archive) { a.Buckets[1].Values[1].Data[0] ^= 1 }},
		{"value digest", func(a *Archive) { a.Buckets[1].Values[0].SHA256 = "" }},
		{"empty bucket name", func(a *Archive) { a.Buckets[0].Name = nil }},
		{"long bucket name", func(a *Archive) { a.Buckets[0].Name = make([]byte, bbolt.MaxKeySize+1) }},
		{"empty key", func(a *Archive) { a.Buckets[1].Values[0].Key = nil }},
		{"long key", func(a *Archive) { a.Buckets[1].Values[0].Key = make([]byte, bbolt.MaxKeySize+1) }},
		{"duplicate bucket", func(a *Archive) { a.Buckets = append(a.Buckets[:1], a.Buckets[0]) }},
		{"unordered buckets", func(a *Archive) { a.Buckets[0], a.Buckets[1] = a.Buckets[1], a.Buckets[0] }},
		{"duplicate key", func(a *Archive) { a.Buckets[1].Values[1].Key = a.Buckets[1].Values[0].Key }},
		{"unordered keys", func(a *Archive) {
			a.Buckets[1].Values[0], a.Buckets[1].Values[1] = a.Buckets[1].Values[1], a.Buckets[1].Values[0]
		}},
		{"bucket key conflict", func(a *Archive) { a.Buckets[1].Buckets = []Bucket{{Name: a.Buckets[1].Values[0].Key}} }},
		{"duplicate nested bucket", func(a *Archive) { a.Buckets[2].Buckets = append(a.Buckets[2].Buckets, a.Buckets[2].Buckets[0]) }},
		{"entry limit", func(a *Archive) { a.Buckets[0].Values = make([]Value, MaxEntries+1) }},
		{"depth limit", func(a *Archive) {
			current := Bucket{Name: []byte("leaf")}
			for i := 0; i < MaxDepth; i++ {
				current = Bucket{Name: []byte("parent"), Buckets: []Bucket{current}}
			}
			a.Buckets = []Bucket{current}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := copyArchive(t, original)
			test.edit(archive)
			if err := Validate(archive); err == nil {
				t.Fatal("accepted tampered or invalid archive")
			}
		})
	}
	if err := Validate(nil); err == nil {
		t.Fatal("accepted nil archive")
	}
}

func TestLogicalDigestBindsNamesValuesHierarchyAndSequence(t *testing.T) {
	original := fixtureArchive(t)
	for _, test := range []struct {
		name string
		edit func(*Archive)
	}{
		{"bucket name", func(a *Archive) { a.Buckets[0].Name = []byte("empty-renamed") }},
		{"sequence", func(a *Archive) { a.Buckets[0].Sequence++ }},
		{"key", func(a *Archive) { a.Buckets[1].Values[1].Key = []byte("renamed-json") }},
		{"value", func(a *Archive) {
			v := &a.Buckets[1].Values[1]
			v.Data = append(v.Data, ' ')
			v.SHA256 = digest(v.Data)
		}},
		{"hierarchy", func(a *Archive) { a.Buckets[0].Buckets = a.Buckets[2].Buckets; a.Buckets[2].Buckets = nil }},
		{"empty value removed", func(a *Archive) { a.Buckets[1].Values = a.Buckets[1].Values[1:] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := copyArchive(t, original)
			test.edit(archive)
			manifest, err := manifestFor(archive.Buckets)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.LogicalSHA256 == original.Manifest.LogicalSHA256 {
				t.Fatal("logical digest did not cover the changed data")
			}
		})
	}
}

func TestLogicalDigestUsesUnambiguousBoundaries(t *testing.T) {
	makeManifest := func(name, key, value string) Manifest {
		manifest, err := manifestFor([]Bucket{{Name: []byte(name), Values: []Value{{Key: []byte(key), Data: []byte(value), SHA256: digest([]byte(value))}}}})
		if err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	first, second := makeManifest("a", "bc", "d"), makeManifest("ab", "c", "d")
	third := makeManifest("a", "b", "cd")
	if first.LogicalSHA256 == second.LogicalSHA256 || first.LogicalSHA256 == third.LogicalSHA256 {
		t.Fatal("different byte boundaries produced the same logical digest")
	}
}
