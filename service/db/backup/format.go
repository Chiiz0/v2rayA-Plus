// Package backup preserves complete database trees without importing application
// configuration, opening a default database, or changing runtime state.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"time"

	"go.etcd.io/bbolt"
)

const (
	FormatName            = "v2raya-bolt-backup"
	FormatVersion         = 1
	SnapshotFormatName    = "v2raya-database-backup"
	SnapshotFormatVersion = 2
	LogicalDigestKind     = "logical-sha256"
	InitializationBucket  = "_v2raya_runtime"
	InitializationKey     = "needs-defaults"
	InitializationValue   = "1"
	MaxDepth              = 64
	MaxEntries            = 100000
	MaxDataBytes          = 128 << 20
	MaxArchiveBytes       = 256 << 20
)

type Archive struct {
	Format    string   `json:"format"`
	Version   int      `json:"version"`
	CreatedAt string   `json:"createdAt"`
	Source    Source   `json:"source"`
	Manifest  Manifest `json:"manifest"`
	Buckets   []Bucket `json:"buckets"`
}

type Source struct {
	Backend            string `json:"backend"`
	SHA256             string `json:"sha256"`
	DigestKind         string `json:"digestKind,omitempty"`
	InitializeDefaults bool   `json:"initializeDefaults,omitempty"`
}

type Manifest struct {
	Buckets       int    `json:"buckets"`
	Values        int    `json:"values"`
	ValueBytes    int64  `json:"valueBytes"`
	LogicalSHA256 string `json:"logicalSHA256"`
}

type Bucket struct {
	Name     []byte   `json:"name"`
	Sequence uint64   `json:"sequence"`
	Buckets  []Bucket `json:"buckets"`
	Values   []Value  `json:"values"`
}

type Value struct {
	Key    []byte `json:"key"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

// Validate checks the container contract, not application-specific semantics.
// Bucket/key ordering is canonical byte order; raw values are never decoded.
func Validate(archive *Archive) error {
	if archive == nil {
		return fmt.Errorf("backup archive is nil")
	}
	legacy := archive.Format == FormatName && archive.Version == FormatVersion
	snapshot := archive.Format == SnapshotFormatName && archive.Version == SnapshotFormatVersion
	if !legacy && !snapshot {
		return fmt.Errorf("unsupported backup format or version")
	}
	if _, err := time.Parse(time.RFC3339Nano, archive.CreatedAt); err != nil {
		return fmt.Errorf("invalid backup creation time")
	}
	if !validDigest(archive.Source.SHA256) ||
		legacy && (archive.Source.Backend != "bbolt" || archive.Source.DigestKind != "" || archive.Source.InitializeDefaults) ||
		snapshot && (archive.Source.Backend != "bbolt" && archive.Source.Backend != "sqlite" || archive.Source.DigestKind != LogicalDigestKind) {
		return fmt.Errorf("invalid backup source metadata")
	}
	manifest, err := manifestFor(archive.Buckets)
	if err != nil {
		return err
	}
	if archive.Manifest != manifest {
		return fmt.Errorf("backup manifest or logical checksum does not match its contents")
	}
	if snapshot && archive.Source.SHA256 != manifest.LogicalSHA256 {
		return fmt.Errorf("backup logical source checksum does not match its contents")
	}
	if archive.Source.InitializeDefaults && !hasInitializationMarker(archive.Buckets) {
		return fmt.Errorf("backup initialization intent requires its pending-defaults marker")
	}
	return nil
}

func hasInitializationMarker(buckets []Bucket) bool {
	for _, bucket := range buckets {
		if !bytes.Equal(bucket.Name, []byte(InitializationBucket)) {
			continue
		}
		for _, value := range bucket.Values {
			if bytes.Equal(value.Key, []byte(InitializationKey)) {
				return bytes.Equal(value.Data, []byte(InitializationValue))
			}
		}
	}
	return false
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type archiveBudget struct {
	entries int
	bytes   int64
}

func (budget *archiveBudget) add(size int64) error {
	if budget.entries >= MaxEntries || size < 0 || size > MaxDataBytes-budget.bytes {
		return fmt.Errorf("backup exceeds the entry or raw-data size limit")
	}
	budget.entries++
	budget.bytes += size
	return nil
}

func hashNumber(h hash.Hash, number uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], number)
	_, _ = h.Write(data[:])
}

func hashBytes(h hash.Hash, data []byte) {
	hashNumber(h, uint64(len(data)))
	_, _ = h.Write(data)
}

func manifestFor(buckets []Bucket) (Manifest, error) {
	manifest := Manifest{}
	budget := archiveBudget{}
	h := sha256.New()
	// Versioned domain separation and length prefixes bind names, sequences,
	// hierarchy and value boundaries without depending on JSON formatting.
	_, _ = h.Write([]byte("v2raya-bolt-backup/logical/v1\x00"))
	if err := hashBuckets(h, buckets, 1, &budget, &manifest); err != nil {
		return Manifest{}, err
	}
	manifest.LogicalSHA256 = hex.EncodeToString(h.Sum(nil))
	return manifest, nil
}

func hashBuckets(h hash.Hash, buckets []Bucket, depth int, budget *archiveBudget, manifest *Manifest) error {
	if len(buckets) != 0 && depth > MaxDepth {
		return fmt.Errorf("backup bucket nesting exceeds the depth limit")
	}
	if len(buckets) > MaxEntries-budget.entries {
		return fmt.Errorf("backup exceeds the entry limit")
	}
	hashNumber(h, uint64(len(buckets)))
	for i, bucket := range buckets {
		if len(bucket.Name) == 0 || len(bucket.Name) > bbolt.MaxKeySize {
			return fmt.Errorf("invalid backup bucket name length")
		}
		if i != 0 && bytes.Compare(buckets[i-1].Name, bucket.Name) >= 0 {
			return fmt.Errorf("backup bucket names must be unique and in byte order")
		}
		if err := budget.add(int64(len(bucket.Name))); err != nil {
			return err
		}
		manifest.Buckets++
		_, _ = h.Write([]byte{'B'})
		hashBytes(h, bucket.Name)
		hashNumber(h, bucket.Sequence)
		hashNumber(h, uint64(len(bucket.Values)))
		if len(bucket.Values) > MaxEntries-budget.entries || len(bucket.Buckets) > MaxEntries-budget.entries-len(bucket.Values) {
			return fmt.Errorf("backup exceeds the entry limit")
		}
		children := make(map[string]struct{}, len(bucket.Buckets))
		for _, child := range bucket.Buckets {
			children[string(child.Name)] = struct{}{}
		}
		for j, value := range bucket.Values {
			if len(value.Key) == 0 || len(value.Key) > bbolt.MaxKeySize || int64(len(value.Data)) > bbolt.MaxValueSize {
				return fmt.Errorf("invalid backup key or value length")
			}
			if j != 0 && bytes.Compare(bucket.Values[j-1].Key, value.Key) >= 0 {
				return fmt.Errorf("backup value keys must be unique and in byte order")
			}
			if _, exists := children[string(value.Key)]; exists {
				return fmt.Errorf("backup key conflicts with a nested bucket")
			}
			if err := budget.add(int64(len(value.Key)) + int64(len(value.Data))); err != nil {
				return err
			}
			if value.SHA256 != digest(value.Data) {
				return fmt.Errorf("backup value checksum does not match its contents")
			}
			manifest.Values++
			manifest.ValueBytes += int64(len(value.Data))
			_, _ = h.Write([]byte{'V'})
			hashBytes(h, value.Key)
			hashBytes(h, value.Data)
		}
		if err := hashBuckets(h, bucket.Buckets, depth+1, budget, manifest); err != nil {
			return err
		}
		_, _ = h.Write([]byte{'E'})
	}
	return nil
}
