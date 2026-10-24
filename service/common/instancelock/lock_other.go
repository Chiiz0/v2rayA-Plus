//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package instancelock

import (
	"fmt"
	"os"
	"runtime"
)

func acquire(string) (*os.File, error) {
	return nil, fmt.Errorf("%w: %s", ErrUnsupported, runtime.GOOS)
}
