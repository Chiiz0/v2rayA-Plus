package backup

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"go.etcd.io/bbolt"
)

// NewSnapshot creates a backend-neutral logical archive of a complete tree.
// It owns every returned byte, sorts names and keys, and recomputes all hashes;
// caller-provided hashes are not trusted. The logical digest remains compatible
// with v1 so a restored Bolt database can be compared without a format conversion.
func NewSnapshot(backend string, buckets []Bucket) (*Archive, error) {
	if backend != "bbolt" && backend != "sqlite" {
		return nil, fmt.Errorf("unsupported snapshot backend")
	}
	budget := archiveBudget{}
	owned, err := canonicalSnapshotBuckets(buckets, 1, &budget)
	if err != nil {
		return nil, err
	}
	manifest, err := manifestFor(owned)
	if err != nil {
		return nil, err
	}
	archive := &Archive{
		Format: SnapshotFormatName, Version: SnapshotFormatVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Source:    Source{Backend: backend, SHA256: manifest.LogicalSHA256, DigestKind: LogicalDigestKind},
		Manifest:  manifest, Buckets: owned,
	}
	return archive, Validate(archive)
}

func canonicalSnapshotBuckets(buckets []Bucket, depth int, budget *archiveBudget) ([]Bucket, error) {
	if len(buckets) != 0 && depth > MaxDepth {
		return nil, fmt.Errorf("backup bucket nesting exceeds the depth limit")
	}
	if len(buckets) > MaxEntries-budget.entries {
		return nil, fmt.Errorf("backup exceeds the entry limit")
	}
	result := make([]Bucket, 0, len(buckets))
	for _, source := range buckets {
		if len(source.Name) == 0 || len(source.Name) > bbolt.MaxKeySize {
			return nil, fmt.Errorf("invalid backup bucket name length")
		}
		if err := budget.add(int64(len(source.Name))); err != nil {
			return nil, err
		}
		if len(source.Values) > MaxEntries-budget.entries || len(source.Buckets) > MaxEntries-budget.entries-len(source.Values) {
			return nil, fmt.Errorf("backup exceeds the entry limit")
		}
		bucket := Bucket{Name: cloneBytes(source.Name), Sequence: source.Sequence, Values: make([]Value, 0, len(source.Values))}
		for _, value := range source.Values {
			if len(value.Key) == 0 || len(value.Key) > bbolt.MaxKeySize || int64(len(value.Data)) > bbolt.MaxValueSize {
				return nil, fmt.Errorf("invalid backup key or value length")
			}
			if err := budget.add(int64(len(value.Key)) + int64(len(value.Data))); err != nil {
				return nil, err
			}
			owned := Value{Key: cloneBytes(value.Key), Data: cloneBytes(value.Data)}
			owned.SHA256 = digest(owned.Data)
			bucket.Values = append(bucket.Values, owned)
		}
		sort.Slice(bucket.Values, func(i, j int) bool { return bytes.Compare(bucket.Values[i].Key, bucket.Values[j].Key) < 0 })
		var err error
		bucket.Buckets, err = canonicalSnapshotBuckets(source.Buckets, depth+1, budget)
		if err != nil {
			return nil, err
		}
		result = append(result, bucket)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i].Name, result[j].Name) < 0 })
	return result, nil
}
