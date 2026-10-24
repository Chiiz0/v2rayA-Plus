//go:build !linux

package backup

import (
	"fmt"
	"os"
)

func openRegularReadOnly(string) (*os.File, error) {
	return nil, fmt.Errorf("safe no-follow file access is currently supported only on Linux")
}
