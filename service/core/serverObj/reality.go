package serverObj

import (
	"fmt"
	"strings"

	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"golang.org/x/mod/semver"
)

// ValidateRealityMLDSA65Core prevents old cores from silently ignoring pqv.
// Xray introduced this verification in v25.7.26 (XTLS/Xray-core#4915).
// Docker builds initially have no version; their image is checked before use.
func ValidateRealityMLDSA65Core(info PriorInfo) error {
	if info.Variant != where.Xray {
		return fmt.Errorf("REALITY ML-DSA-65 requires Xray; please select an Xray core")
	}
	version := "v" + strings.TrimPrefix(info.CoreVersion, "v")
	if semver.IsValid(version) && semver.Compare(version, "v25.7.26") < 0 {
		return fmt.Errorf("REALITY ML-DSA-65 requires Xray 25.7.26 or newer; detected %s", info.CoreVersion)
	}
	return nil
}
