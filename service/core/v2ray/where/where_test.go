package where

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/v2rayA/v2rayA/conf"
)

func TestAutoCorePrefersXrayAndRespectsExplicitBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX executable permissions")
	}
	t.Setenv("V2RAYA_CONFIG", t.TempDir())
	config := conf.GetEnvironmentConfig()
	previous := config.V2rayBin
	t.Cleanup(func() { config.V2rayBin = previous })
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("PATH", dir)
	config.V2rayBin = ""
	for _, binary := range []string{"xray", "v2ray"} {
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if path, err := GetV2rayBinPath(); err != nil || path != filepath.Join(dir, "xray") {
		t.Fatalf("auto = %q, %v; want bundled Xray", path, err)
	}
	config.V2rayBin = filepath.Join(dir, "v2ray")
	if path, err := GetV2rayBinPath(); err != nil || path != config.V2rayBin {
		t.Fatalf("explicit V2Fly selection changed: %q, %v", path, err)
	}
	config.V2rayBin = ""
	if err := os.Remove(filepath.Join(dir, "xray")); err != nil {
		t.Fatal(err)
	}
	if path, err := GetV2rayBinPath(); err != nil || path != filepath.Join(dir, "v2ray") {
		t.Fatalf("auto fallback = %q, %v; want V2Fly", path, err)
	}
}
