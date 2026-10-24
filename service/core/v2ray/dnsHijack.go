package v2ray

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/pkg/util/log"
)

const (
	checkInterval = 3 * time.Second
	HijackFlag    = "# v2rayA DNS hijack"
)

var (
	resolvPath       = "/etc/resolv.conf"
	resolvBackupPath string // Empty uses the persistent configuration directory.
	resolvMu         sync.Mutex
	hijackerMu       sync.Mutex
	hijacker         *ResolvHijacker
)

// Adapted from upstream bd0f3898, 90825094, d537477a and 7c482856.
// Keep the original in the persistent config volume, and never intercept DNS
// until that original has been safely recorded. A missing file is also a state.
type resolvSnapshot struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Content []byte `json:"content,omitempty"`
	Target  string `json:"target,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
}

type ResolvHijacker struct {
	ticker    *time.Ticker
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
}

func NewResolvHijacker() (*ResolvHijacker, error) {
	return newResolvHijacker(checkInterval)
}

func newResolvHijacker(interval time.Duration) (*ResolvHijacker, error) {
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	h := &ResolvHijacker{done: make(chan struct{})}
	if err := h.HijackResolv(); err != nil {
		return nil, errors.Join(err, restoreResolv())
	}
	h.ticker = time.NewTicker(interval)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			select {
			case <-h.done:
				return
			case <-h.ticker.C:
				if err := h.HijackResolv(); err != nil {
					log.Warn("DNS interception: %v", err)
				}
			}
		}
	}()
	return h, nil
}

// Wait for any in-flight writer before the caller restores the original.
func (h *ResolvHijacker) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		if h.ticker != nil {
			h.ticker.Stop()
		}
		if h.done != nil {
			close(h.done)
		}
		h.mu.Unlock()
	})
	h.wg.Wait()
	return nil
}

func resolvSnapshotPath() string {
	if resolvBackupPath != "" {
		return resolvBackupPath
	}
	return filepath.Join(conf.GetEnvironmentConfig().Config, "resolv.conf.v2raya_backup")
}

func readResolvSnapshot() (*resolvSnapshot, error) {
	path := resolvSnapshotPath()
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("DNS backup is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var saved resolvSnapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("invalid DNS backup: %w", err)
	}
	if saved.Version != 1 || saved.Mode > 0777 {
		return nil, fmt.Errorf("unsupported DNS backup version or mode")
	}
	switch saved.Kind {
	case "file":
		if bytes.HasPrefix(saved.Content, []byte(HijackFlag)) {
			return nil, fmt.Errorf("DNS backup contains intercepted DNS instead of the original")
		}
	case "symlink":
		if saved.Target == "" {
			return nil, fmt.Errorf("DNS backup has an empty symlink target")
		}
	case "missing":
	default:
		return nil, fmt.Errorf("invalid DNS backup kind %q", saved.Kind)
	}
	return &saved, nil
}

// The caller holds resolvMu. Publishing with a hard link is atomic and cannot
// replace an earlier backup, including one left by an interrupted process.
func backupResolv() error {
	if _, err := readResolvSnapshot(); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	saved := resolvSnapshot{Version: 1}
	info, err := os.Lstat(resolvPath)
	switch {
	case os.IsNotExist(err):
		saved.Kind = "missing"
		err = nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		saved.Kind = "symlink"
		saved.Target, err = os.Readlink(resolvPath)
		// Older versions followed the link when writing. Such a target has
		// already lost its original DNS data and must not become our baseline.
		if err == nil {
			if target, e := os.Stat(resolvPath); e == nil && target.Mode().IsRegular() {
				var content []byte
				content, err = os.ReadFile(resolvPath)
				if err == nil && bytes.HasPrefix(content, []byte(HijackFlag)) {
					return fmt.Errorf("resolv.conf symlink target was already intercepted without a backup; restore the original DNS configuration before starting")
				}
			}
		}
	case info.Mode().IsRegular():
		saved.Kind = "file"
		saved.Mode = uint32(info.Mode().Perm())
		saved.Content, err = os.ReadFile(resolvPath)
		if err == nil && bytes.HasPrefix(saved.Content, []byte(HijackFlag)) {
			return fmt.Errorf("resolv.conf was already intercepted without a backup; restore the original DNS configuration before starting")
		}
	default:
		return fmt.Errorf("refusing to intercept non-regular resolv.conf")
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	path := resolvSnapshotPath()
	f, err := os.CreateTemp(filepath.Dir(path), ".resolv-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return err
	}
	return syncResolvDirectory(filepath.Dir(path))
}

func syncResolvDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (h *ResolvHijacker) HijackResolv() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	resolvMu.Lock()
	defer resolvMu.Unlock()
	if err := backupResolv(); err != nil {
		return fmt.Errorf("cannot back up DNS configuration: %w", err)
	}
	content := []byte(HijackFlag + "\nnameserver 127.2.0.17\nnameserver 119.29.29.29\n")
	if info, err := os.Lstat(resolvPath); err == nil && info.Mode().IsRegular() {
		if current, err := os.ReadFile(resolvPath); err == nil && bytes.Equal(current, content) {
			return nil
		}
	}
	if err := replaceResolvFile(content, 0644); err != nil {
		return fmt.Errorf("cannot intercept DNS: %w", err)
	}
	return nil
}

// Never follow symlinks. Existing regular files are written in place because
// Docker's single-file bind mounts cannot be replaced with rename(2).
func replaceResolvFile(content []byte, mode os.FileMode) error {
	info, err := os.Lstat(resolvPath)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(resolvPath); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return writeResolvFileNoFollow(resolvPath, content, mode)
}

func restoreResolv() error {
	resolvMu.Lock()
	defer resolvMu.Unlock()
	saved, err := readResolvSnapshot()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	switch saved.Kind {
	case "file":
		err = replaceResolvFile(saved.Content, os.FileMode(saved.Mode))
	case "missing":
		err = os.Remove(resolvPath)
		if os.IsNotExist(err) {
			err = nil
		}
	case "symlink":
		var f *os.File
		f, err = os.CreateTemp(filepath.Dir(resolvPath), ".resolv-restore-*")
		if err == nil {
			name := f.Name()
			defer os.Remove(name)
			err = f.Close()
			if err == nil {
				err = os.Remove(name)
			}
			if err == nil {
				err = os.Symlink(saved.Target, name)
			}
			if err == nil {
				err = os.Rename(name, resolvPath)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("cannot restore DNS configuration (backup retained): %w", err)
	}
	if err := syncResolvDirectory(filepath.Dir(resolvPath)); err != nil {
		return err
	}
	path := resolvSnapshotPath()
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("DNS restored but cannot remove its backup: %w", err)
	}
	return syncResolvDirectory(filepath.Dir(path))
}

// RecoverResolvConf also works without a live hijacker after an abnormal exit.
// Call only after acquiring the application's exclusive configuration lock.
func RecoverResolvConf() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	hijackerMu.Lock()
	defer hijackerMu.Unlock()
	if hijacker != nil {
		return nil
	}
	return restoreResolv()
}

func resetResolvHijacker() error {
	hijackerMu.Lock()
	defer hijackerMu.Unlock()
	if hijacker != nil {
		hijacker.Close()
		hijacker = nil
	}
	var err error
	hijacker, err = NewResolvHijacker()
	return err
}

func removeResolvHijacker() {
	if runtime.GOOS != "linux" {
		return
	}
	hijackerMu.Lock()
	defer hijackerMu.Unlock()
	if hijacker != nil {
		hijacker.Close()
		hijacker = nil
	}
	if err := restoreResolv(); err != nil {
		log.Warn("restore DNS configuration: %v", err)
	}
}
