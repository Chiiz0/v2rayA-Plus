//go:build !linux

package sqlitebackup

import (
	"errors"
	"os"
)

func openRegularReadOnly(string) (*os.File, error) {
	return nil, errors.New("safe offline SQLite file access is currently supported only on Linux")
}
