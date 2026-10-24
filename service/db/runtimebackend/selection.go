// Package runtimebackend manages offline, verified backend transitions. The
// selection file is the single authority; database files are never overwritten.
package runtimebackend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/v2rayA/v2rayA/db/backup"
)

const SelectionFilename = ".v2raya-database.json"

// Only runtime-init requests application defaults. Ordinary imports must not
// interpret similarly named unknown data as an initialization instruction.
const DefaultsBucket = backup.InitializationBucket
const DefaultsKey = backup.InitializationKey
const DefaultsValue = backup.InitializationValue

type Selection struct {
	Version    int    `json:"version"`
	Backend    string `json:"backend"`
	Database   string `json:"database"`
	Initialize bool   `json:"initialize,omitempty"`
	Present    bool   `json:"-"`
}

var databaseName = regexp.MustCompile(`^(bolt\.db|(sqlite|bolt)-[0-9a-f]{64}(-[0-9a-f]{16})?\.db)$`)

// ReadSelection never creates a database. Without a marker the existing Bolt
// behavior is preserved; a damaged marker or missing selected file is fatal.
// Callers that open or change the selected database must hold the instance lock.
func ReadSelection(configDir string) (Selection, error) {
	selection := Selection{Version: 1, Backend: "bolt", Database: "bolt.db"}
	file, err := openRegular(filepath.Join(configDir, SelectionFilename))
	if errors.Is(err, os.ErrNotExist) {
		return selection, nil
	}
	if err != nil {
		return Selection{}, fmt.Errorf("open backend selection: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return Selection{}, errors.New("backend selection cannot be read or exceeds size limit")
	}
	// DisallowUnknownFields alone accepts duplicate/case-aliased fields.
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return Selection{}, errors.New("invalid backend selection object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Selection{}, errors.New("invalid backend selection field")
		}
		key, ok := token.(string)
		if !ok || seen[key] || (key != "version" && key != "backend" && key != "database" && key != "initialize") {
			return Selection{}, errors.New("unknown or duplicate backend selection field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(value, []byte("null")) {
			return Selection{}, errors.New("invalid backend selection value")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return Selection{}, errors.New("invalid backend selection end")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Selection{}, errors.New("trailing backend selection data")
	}
	if !seen["version"] || !seen["backend"] || !seen["database"] {
		return Selection{}, errors.New("incomplete backend selection")
	}
	selection = Selection{}
	if err := json.Unmarshal(data, &selection); err != nil {
		return Selection{}, errors.New("invalid backend selection schema")
	}
	if err := validateSelection(selection); err != nil {
		return Selection{}, err
	}
	selected, err := openRegular(filepath.Join(configDir, selection.Database))
	if err != nil {
		return Selection{}, fmt.Errorf("selected database unavailable; refusing fallback: %w", err)
	}
	if err := selected.Close(); err != nil {
		return Selection{}, err
	}
	selection.Present = true
	return selection, nil
}

func validateSelection(selection Selection) error {
	if selection.Version != 1 || (selection.Backend != "bolt" && selection.Backend != "sqlite") || !databaseName.MatchString(selection.Database) {
		return errors.New("unsupported backend selection")
	}
	if (selection.Backend == "sqlite") != bytes.HasPrefix([]byte(selection.Database), []byte("sqlite-")) {
		return errors.New("selected database name does not match its backend")
	}
	return nil
}

func publishSelection(dir string, selection Selection) error {
	if err := validateSelection(selection); err != nil {
		return err
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".v2raya-selection-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, SelectionFilename)); err != nil {
		return fmt.Errorf("activate verified database: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("database selected but directory sync failed; run runtime-verify before retrying: %w", err)
	}
	return nil
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
