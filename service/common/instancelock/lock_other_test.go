//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package instancelock

import (
	"errors"
	"testing"
)

func TestUnsupportedPlatform(t *testing.T) {
	lock, err := Acquire(t.TempDir())
	if lock != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Acquire = %v, %v; want ErrUnsupported", lock, err)
	}
}
