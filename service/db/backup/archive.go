package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

// Export takes a shared Bolt lock with a finite timeout. The application's
// existing exclusive lock therefore prevents exporting a live writable source.
func Export(sourcePath string, timeout time.Duration) (*Archive, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("backup lock timeout must be positive")
	}
	var source *os.File
	database, err := bbolt.Open(sourcePath, 0600, &bbolt.Options{
		ReadOnly: true, Timeout: timeout,
		OpenFile: func(path string, _ int, _ os.FileMode) (*os.File, error) {
			// bbolt normally adds O_CREATE even in read-only mode. Override it
			// so a missing source can never be created or a symlink followed.
			var err error
			source, err = openRegularReadOnly(path)
			return source, err
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open backup source: %w", err)
	}
	defer database.Close()
	archive := &Archive{Format: FormatName, Version: FormatVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Source: Source{Backend: "bbolt"}}
	err = database.View(func(tx *bbolt.Tx) error {
		var err error
		archive.Buckets, err = snapshotBuckets(tx)
		if err != nil {
			return err
		}
		return checkTransaction(tx)
	})
	if err != nil {
		return nil, fmt.Errorf("read backup source: %w", err)
	}
	archive.Manifest, err = manifestFor(archive.Buckets)
	if err != nil {
		return nil, err
	}
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.NewSectionReader(source, 0, info.Size())); err != nil {
		return nil, fmt.Errorf("hash backup source: %w", err)
	}
	archive.Source.SHA256 = hex.EncodeToString(h.Sum(nil))
	if err = Validate(archive); err != nil {
		return nil, err
	}
	return archive, nil
}

func snapshotBuckets(tx *bbolt.Tx) ([]Bucket, error) {
	result := []Bucket{}
	budget := archiveBudget{}
	err := tx.ForEach(func(name []byte, bucket *bbolt.Bucket) error {
		copy, err := snapshotBucket(name, bucket, 1, &budget)
		if err != nil {
			return err
		}
		result = append(result, copy)
		return nil
	})
	return result, err
}

func snapshotBucket(name []byte, source *bbolt.Bucket, depth int, budget *archiveBudget) (Bucket, error) {
	if depth > MaxDepth {
		return Bucket{}, fmt.Errorf("source bucket nesting exceeds the depth limit")
	}
	if err := budget.add(int64(len(name))); err != nil {
		return Bucket{}, err
	}
	bucket := Bucket{Name: cloneBytes(name), Sequence: source.Sequence(), Buckets: []Bucket{}, Values: []Value{}}
	err := source.ForEach(func(key, data []byte) error {
		if child := source.Bucket(key); child != nil {
			copy, err := snapshotBucket(key, child, depth+1, budget)
			if err != nil {
				return err
			}
			bucket.Buckets = append(bucket.Buckets, copy)
			return nil
		}
		if err := budget.add(int64(len(key)) + int64(len(data))); err != nil {
			return err
		}
		bucket.Values = append(bucket.Values, Value{Key: cloneBytes(key), Data: cloneBytes(data), SHA256: digest(data)})
		return nil
	})
	return bucket, err
}

func cloneBytes(value []byte) []byte {
	result := make([]byte, len(value))
	copy(result, value)
	return result
}

func checkTransaction(tx *bbolt.Tx) error {
	failed := false
	// Always drain Check's channel before closing the transaction. Diagnostics
	// may include raw keys, so return only a generic integrity error.
	for err := range tx.Check() {
		if err != nil {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("Bolt integrity check failed")
	}
	return nil
}

// Read rejects unknown fields, duplicate object fields, trailing JSON and
// excessive size/depth before checking the complete archive manifest.
func Read(path string) (*Archive, error) {
	file, err := openRegularReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxArchiveBytes {
		return nil, fmt.Errorf("backup archive exceeds the file size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxArchiveBytes {
		return nil, fmt.Errorf("backup archive exceeds the file size limit")
	}
	if err := checkArchiveJSON(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var archive Archive
	if err := decoder.Decode(&archive); err != nil {
		return nil, fmt.Errorf("invalid backup JSON schema")
	}
	if err := Validate(&archive); err != nil {
		return nil, err
	}
	return &archive, nil
}

func checkArchiveJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data after backup JSON")
	}
	return nil
}

func checkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > MaxDepth*2+10 {
		return fmt.Errorf("backup JSON nesting exceeds the depth limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid backup JSON")
	}
	delimiter, nested := token.(json.Delim)
	if !nested {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] || !archiveJSONField(name) {
				return fmt.Errorf("invalid or duplicate backup JSON field")
			}
			seen[name] = true
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid backup JSON")
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("invalid backup JSON")
	}
	return nil
}

func archiveJSONField(name string) bool {
	// encoding/json otherwise accepts case-insensitive aliases even with
	// DisallowUnknownFields, including a second spelling of the same field.
	switch name {
	case "format", "version", "createdAt", "source", "manifest", "buckets", "backend", "sha256",
		"values", "valueBytes", "logicalSHA256", "name", "sequence", "key", "data":
		return true
	default:
		return false
	}
}

func Write(path string, archive *Archive) error {
	if err := Validate(archive); err != nil {
		return err
	}
	file, err := createArchiveTemp(path)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	encoder := json.NewEncoder(&archiveWriter{writer: file})
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(archive); err != nil {
		return fmt.Errorf("write backup archive: %w", err)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return publishNoReplace(file.Name(), path)
}

type archiveWriter struct {
	writer io.Writer
	size   int64
}

func (w *archiveWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > MaxArchiveBytes-w.size {
		return 0, fmt.Errorf("backup archive exceeds the file size limit")
	}
	n, err := w.writer.Write(data)
	w.size += int64(n)
	return n, err
}

func createArchiveTemp(target string) (*os.File, error) {
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("backup destination already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	return file, nil
}

// publishNoReplace leaves an already-published complete file in place if the
// directory sync fails. It never deletes or replaces an existing destination.
func publishNoReplace(tempPath, target string) error {
	if err := os.Link(tempPath, target); err != nil {
		return fmt.Errorf("publish backup without overwriting: %w", err)
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("backup published, but opening its directory for sync failed: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("backup published, but directory sync failed: %w", err)
	}
	return nil
}

// Restore creates a verified new Bolt database. It never opens an existing
// destination, applies application configuration, or changes the source archive.
func Restore(path string, archive *Archive) error {
	if err := Validate(archive); err != nil {
		return err
	}
	file, err := createArchiveTemp(path)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	database, err := bbolt.Open(file.Name(), 0600, &bbolt.Options{
		Timeout:  time.Second,
		OpenFile: func(string, int, os.FileMode) (*os.File, error) { return file, nil },
	})
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("create restored database: %w", err)
	}
	defer database.Close()
	if err := database.Update(func(tx *bbolt.Tx) error {
		for _, bucket := range archive.Buckets {
			target, err := tx.CreateBucket(bucket.Name)
			if err != nil {
				return err
			}
			if err := restoreBucket(target, bucket); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("restore database contents: %w", err)
	}
	if err := database.View(func(tx *bbolt.Tx) error {
		if err := checkTransaction(tx); err != nil {
			return err
		}
		buckets, err := snapshotBuckets(tx)
		if err != nil {
			return err
		}
		manifest, err := manifestFor(buckets)
		if err != nil {
			return err
		}
		if manifest != archive.Manifest {
			return fmt.Errorf("restored database does not match the archive logical snapshot")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("verify restored database: %w", err)
	}
	if err := database.Sync(); err != nil {
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	return publishNoReplace(file.Name(), path)
}

func restoreBucket(target *bbolt.Bucket, bucket Bucket) error {
	if err := target.SetSequence(bucket.Sequence); err != nil {
		return err
	}
	for _, value := range bucket.Values {
		if err := target.Put(value.Key, value.Data); err != nil {
			return err
		}
	}
	for _, child := range bucket.Buckets {
		nested, err := target.CreateBucket(child.Name)
		if err != nil {
			return err
		}
		if err := restoreBucket(nested, child); err != nil {
			return err
		}
	}
	return nil
}
