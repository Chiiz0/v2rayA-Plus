package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
	"go.etcd.io/bbolt"
)

func fixtureDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	database, err := bbolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		values := map[string]map[string][]byte{
			"system":   {"running": []byte(`true`), "future": {0, 255, 1}},
			"accounts": {"admin": []byte(`"credential-hash-must-not-be-printed"`)},
			"touch": {
				"servers":       []byte(`[{"serverObj":{"protocol":"vless","name":"xhttp","add":"example.invalid","port":443,"id":"11111111-1111-4111-8111-111111111111","net":"xhttp","encryption":"none","xhttpExtra":{"future":9007199254740993,"disabled":false}},"latency":"1ms"}]`),
				"subscriptions": []byte(`[]`),
			},
			"outbound.中":    {"connectedServers": []byte(`{"touches":[{"_type":"server","id":1,"outbound":"中"}]}`), "setting": []byte(`{"probeInterval":"10s","probeURL":"https://example.invalid","type":"leastping"}`)},
			"dockerProxies": {"19080": []byte(`{"port":19080,"which":{"_type":"server","id":1},"core":"xray","serverName":"xhttp","containerName":"v2raya-socks-19080","status":"paused"}`)},
		}
		for name, items := range values {
			bucket, err := tx.CreateBucket([]byte(name))
			if err != nil {
				return err
			}
			for key, data := range items {
				if err := bucket.Put([]byte(key), data); err != nil {
					return err
				}
			}
		}
		names, err := tx.CreateBucket([]byte("outbounds"))
		if err != nil {
			return err
		}
		var encoded bytes.Buffer
		var member bytes.Buffer
		if err := gob.NewEncoder(&member).Encode("中"); err != nil {
			return err
		}
		if err := gob.NewEncoder(&encoded).Encode(map[[32]byte]interface{}{sha256.Sum256(member.Bytes()): "中"}); err != nil {
			return err
		}
		if err := names.Put([]byte("names"), encoded.Bytes()); err != nil {
			return err
		}
		unknown, err := tx.CreateBucket([]byte{255, 0, 1})
		if err != nil {
			return err
		}
		if err := unknown.SetSequence(9007199254740993); err != nil {
			return err
		}
		nested, err := unknown.CreateBucket([]byte("nested"))
		if err != nil {
			return err
		}
		return nested.Put([]byte{0, 255}, []byte{})
	})
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("fixture: %v, close: %v", err, closeErr)
	}
	return path
}

func invoke(t *testing.T, expected int, args ...string) commandReport {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != expected {
		t.Fatalf("%v: exit %d, want %d; stdout=%s stderr=%s", args, code, expected, &stdout, &stderr)
	}
	if strings.Contains(stdout.String()+stderr.String(), "credential-hash-must-not-be-printed") {
		t.Fatal("command report printed account credentials")
	}
	var report commandReport
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("invalid command JSON: %v", err)
		}
	}
	return report
}

func TestBackupCommandRoundTripAndCompare(t *testing.T) {
	source := fixtureDatabase(t)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "archive.json")
	exported := invoke(t, 0, "export", "--db", source, "--out", archive)
	if exported.Manifest == nil || exported.Audit == nil || exported.Audit.HasErrors() || !exported.Success {
		t.Fatalf("unexpected export report: %+v", exported)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	invoke(t, 0, "restore", "--in", archive, "--db", restored)
	verified := invoke(t, 0, "verify", "--in", archive, "--db", restored)
	if verified.Comparison == nil || !verified.Comparison.Match || verified.External != nil {
		t.Fatalf("restore did not compare all logical contents: %+v", verified)
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("backup changed source database")
	}
	for _, path := range []string{archive, restored} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("output permissions: %s: %v", path, err)
		}
	}
	invoke(t, 1, "restore", "--in", archive, "--db", source)
	invoke(t, 1, "export", "--db", source, "--out", archive)
	after, _ = os.ReadFile(source)
	if !bytes.Equal(before, after) {
		t.Fatal("refused overwrite altered source")
	}
	// Equal counts alone must not make changed raw data compare equal.
	database, err := bbolt.Open(restored, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("system")).Put([]byte("future"), []byte{1, 255, 0})
	})
	_ = database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if report := invoke(t, 2, "verify", "--in", archive, "--db", restored); report.Comparison.Match {
		t.Fatal("changed bytes with unchanged counts passed comparison")
	}
}

func TestBackupCommandPreservesButDoesNotRestoreInvalidReferences(t *testing.T) {
	source := fixtureDatabase(t)
	database, err := bbolt.Open(source, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("dockerProxies")).Put([]byte("19081"), []byte(`{"port":19081,"which":{"_type":"server","id":99},"core":"xray","containerName":"v2raya-socks-19081","status":"paused"}`))
	})
	_ = database.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "invalid-reference.json")
	exported := invoke(t, 2, "export", "--db", source, "--out", archive)
	if exported.Audit == nil || !exported.Audit.HasErrors() {
		t.Fatal("dangling reference was not reported")
	}
	if _, err := backup.Read(archive); err != nil {
		t.Fatalf("damaged application data should still have a valid archive: %v", err)
	}
	invoke(t, 2, "verify", "--in", archive)
	target := filepath.Join(t.TempDir(), "must-not-exist.db")
	invoke(t, 2, "restore", "--in", archive, "--db", target)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("invalid references produced a restored database")
	}
}

func TestBackupCommandLockTimeoutAndArguments(t *testing.T) {
	source := fixtureDatabase(t)
	database, err := bbolt.Open(source, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	target := filepath.Join(t.TempDir(), "archive.json")
	started := time.Now()
	invoke(t, 1, "export", "--db", source, "--out", target, "--lock-timeout", "100ms")
	if time.Since(started) > 2*time.Second {
		t.Fatal("active database lock was not bounded")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("lock failure produced an archive")
	}
	for _, args := range [][]string{{}, {"no-such-command"}, {"export"}, {"restore", "--in", "missing"}, {"export", "--db", source, "--out", target, "--lock-timeout", "0"}, {"verify", "--in", "missing", "unexpected"}} {
		var output bytes.Buffer
		if code := run(args, &output, &output); code != 2 {
			t.Fatalf("invalid command %v exited %d", args, code)
		}
	}
}

func TestDNSCommandKeepsRecoverySeparate(t *testing.T) {
	directory := t.TempDir()
	contents := []byte(`{"kind":"symlink","link":"/run/systemd/resolve/stub-resolv.conf","future":9007199254740993}`)
	if err := os.WriteFile(filepath.Join(directory, backup.DNSRecoveryFilename), contents, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "host-dns-recovery.json")
	report := invoke(t, 0, "dns-backup", "--config-dir", directory, "--out", target)
	if report.DNS == nil || !report.DNS.Present || report.DNS.SHA256 == "" {
		t.Fatal("DNS recovery copy was not reported")
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatal("DNS recovery bytes changed")
	}
	invoke(t, 1, "dns-backup", "--config-dir", directory, "--out", target)
	missing := filepath.Join(t.TempDir(), "missing.json")
	report = invoke(t, 0, "dns-backup", "--config-dir", t.TempDir(), "--out", missing)
	if report.DNS.Present {
		t.Fatal("missing recovery data was reported as present")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("missing recovery data created an output")
	}
}
