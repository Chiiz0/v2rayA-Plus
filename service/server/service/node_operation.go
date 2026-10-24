package service

import (
	"sync"

	"github.com/v2rayA/v2rayA/core/v2ray"
)

// Coordinate operations that resolve positional node references and later
// persist them. Keep subscription downloads outside this lock and revalidate
// their snapshots before committing. Never hold a Bolt transaction while
// running Docker commands or restarting the core.
var nodeOperationMu sync.Mutex

// The node/reference transaction has already committed. A reload failure must
// not restore old positional references against the new node lists.
func refreshCoreAfterNodeChangeLocked() error {
	if !v2ray.ProcessManager.Running() {
		return nil
	}
	return v2ray.UpdateV2RayConfig()
}
