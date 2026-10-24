//go:build !linux

package v2ray

import (
	"fmt"
	"os"
)

func writeResolvFileNoFollow(string, []byte, os.FileMode) error {
	return fmt.Errorf("DNS interception is only supported on Linux")
}
