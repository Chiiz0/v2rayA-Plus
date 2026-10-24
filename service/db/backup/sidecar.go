package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const DNSRecoveryFilename = "resolv.conf.v2raya_backup"

type DNSReport struct {
	Present bool   `json:"present"`
	Bytes   int64  `json:"bytes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

func dnsRecoveryPath(sourceDir string) (string, error) {
	info, err := os.Lstat(sourceDir)
	if err != nil {
		return "", fmt.Errorf("inspect DNS recovery directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("DNS recovery source must be a directory, not a symlink")
	}
	return filepath.Join(sourceDir, DNSRecoveryFilename), nil
}

// InspectDNSRecovery examines only metadata of the fixed recovery filename.
// It never parses the recovery data or reads/applies the system resolver file.
func InspectDNSRecovery(sourceDir string) (DNSReport, error) {
	path, err := dnsRecoveryPath(sourceDir)
	if err != nil {
		return DNSReport{}, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return DNSReport{}, nil
	}
	if err != nil {
		return DNSReport{}, fmt.Errorf("inspect DNS recovery file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return DNSReport{}, fmt.Errorf("DNS recovery file must be a regular file, not a symlink")
	}
	return DNSReport{Present: true, Bytes: info.Size()}, nil
}

// CopyDNSRecovery copies opaque recovery bytes to a new, explicit destination.
// DNS state is external to the database; this is a separate operation, not part
// of the database archive's snapshot or restore transaction.
func CopyDNSRecovery(sourceDir, destination string) (DNSReport, error) {
	path, err := dnsRecoveryPath(sourceDir)
	if err != nil {
		return DNSReport{}, err
	}
	source, err := openRegularReadOnly(path)
	if os.IsNotExist(err) {
		return DNSReport{}, nil
	}
	if err != nil {
		return DNSReport{}, fmt.Errorf("open DNS recovery file: %w", err)
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil {
		return DNSReport{}, err
	}
	report := DNSReport{Present: true, Bytes: before.Size()}
	if destination == "" {
		return report, fmt.Errorf("DNS recovery destination is required")
	}
	output, err := os.CreateTemp(filepath.Dir(destination), ".v2raya-dns-recovery-*")
	if err != nil {
		return report, fmt.Errorf("create DNS recovery copy: %w", err)
	}
	defer os.Remove(output.Name())
	defer output.Close()
	if err = output.Chmod(0600); err != nil {
		return report, err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(output, hash), source)
	if err != nil {
		return report, fmt.Errorf("copy DNS recovery file: %w", err)
	}
	after, err := source.Stat()
	if err != nil {
		return report, err
	}
	if written != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return report, fmt.Errorf("DNS recovery file changed while copying; retry with the service stopped")
	}
	report.Bytes = written
	report.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err = output.Sync(); err != nil {
		return report, fmt.Errorf("sync DNS recovery copy: %w", err)
	}
	if err = output.Close(); err != nil {
		return report, fmt.Errorf("close DNS recovery copy: %w", err)
	}
	if err = publishNoReplace(output.Name(), destination); err != nil {
		return report, fmt.Errorf("publish DNS recovery copy: %w", err)
	}
	return report, nil
}
