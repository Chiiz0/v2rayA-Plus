//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package sqlitestore

import (
	"errors"
	"os"
)

func openRegularReadOnly(string) (*os.File, error) {
	return nil, errors.New("safe SQLite runtime files are unsupported on this platform")
}
