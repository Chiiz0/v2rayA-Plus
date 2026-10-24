package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	configurev4 "github.com/v2rayA/v2rayA-lib4/db/configure"
	"go.etcd.io/bbolt"
)

type legacyConfiguration struct {
	servers       []string
	subscriptions []string
}

// Decode from the locked source directly. Calling lib4's database singleton
// would reopen the source indefinitely and parse the command line a second time.
func readLegacyConfiguration(database *bbolt.DB) (result *legacyConfiguration, err error) {
	if database == nil {
		return nil, nil
	}
	err = database.View(func(tx *bbolt.Tx) error {
		system := tx.Bucket([]byte("system"))
		if system == nil || system.Stats().KeyN == 0 {
			return nil
		}
		result = &legacyConfiguration{}
		touch := tx.Bucket([]byte("touch"))
		if touch == nil {
			return nil
		}
		servers, err := legacyList(touch, "servers_v2")
		if err != nil {
			return err
		}
		for i, raw := range servers {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil || object == nil {
				return fmt.Errorf("legacy server %d is not an object", i+1)
			}
			var serverObject map[string]json.RawMessage
			if err := json.Unmarshal(object["serverObj"], &serverObject); err != nil || serverObject == nil {
				return fmt.Errorf("legacy server %d has no valid server object", i+1)
			}
			var protocol string
			if data := serverObject["protocol"]; len(data) > 0 {
				if err := json.Unmarshal(data, &protocol); err != nil {
					return fmt.Errorf("legacy server %d has an invalid protocol", i+1)
				}
			}
			// lib4 falls back to its own environment/parser for unknown
			// protocols. Only invoke its built-in decoders; stored plugin
			// records have the explicit plugin-manager protocol.
			switch protocol {
			case "", "vmess", "vless", "socks5", "http", "https", "http-proxy", "https-proxy", "trojan", "trojan-go", "shadowsocks", "ss", "shadowsocksr", "ssr", "ping-tunnel", "pingtunnel", "plugin-manager":
			default:
				return fmt.Errorf("legacy server %d uses an unsupported protocol", i+1)
			}
			// The legacy decoder uses gjson (first duplicate key), whereas
			// encoding/json validates the last one. Normalize both objects so
			// the decoder sees exactly the protocol that was checked above.
			object["serverObj"], err = json.Marshal(serverObject)
			if err != nil {
				return err
			}
			normalized, err := json.Marshal(object)
			if err != nil {
				return err
			}
			server, err := configurev4.Bytes2ServerRaw2(normalized)
			if err != nil || server == nil || server.ServerObj == nil {
				return fmt.Errorf("legacy server %d cannot be decoded", i+1)
			}
			address := server.ServerObj.ExportToURL()
			if address == "" {
				return fmt.Errorf("legacy server %d cannot be exported", i+1)
			}
			result.servers = append(result.servers, address)
		}
		subscriptions, err := legacyList(touch, "subscriptions_v2")
		if err != nil {
			return err
		}
		for i, raw := range subscriptions {
			var subscription struct {
				Address string `json:"address"`
			}
			if err := json.Unmarshal(raw, &subscription); err != nil || subscription.Address == "" {
				return fmt.Errorf("legacy subscription %d has no valid address", i+1)
			}
			result.subscriptions = append(result.subscriptions, subscription.Address)
		}
		return nil
	})
	return result, err
}

func legacyList(bucket *bbolt.Bucket, key string) ([]json.RawMessage, error) {
	if bucket.Bucket([]byte(key)) != nil {
		return nil, fmt.Errorf("legacy %s is a bucket instead of a list", key)
	}
	data := bucket.Get([]byte(key))
	if data == nil {
		return nil, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("invalid legacy %s list: %w", key, err)
	}
	return values, nil
}

// Preserve every bucket before the existing node conversion runs. A temporary
// transaction snapshot is published without overwriting a concurrently created
// bolt.db (for example by a release that predates the directory lock).
func publishLegacySnapshot(source *bbolt.DB, path string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".v2raya-legacy-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	err = source.View(func(tx *bbolt.Tx) error {
		var invalid bool
		for range tx.Check() {
			invalid = true
		}
		if invalid {
			return fmt.Errorf("legacy database failed its integrity check")
		}
		_, err := tx.WriteTo(file)
		return err
	})
	if err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Link(file.Name(), path); err != nil {
		return fmt.Errorf("publish legacy snapshot without overwrite: %w", err)
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("snapshot published, open directory for sync: %w", err)
		}
		defer directory.Close()
		if err = directory.Sync(); err != nil {
			return fmt.Errorf("snapshot published, sync directory: %w", err)
		}
	}
	return nil
}
