package service

import (
	"fmt"

	"github.com/v2rayA/v2rayA/db/configure"
)

func DeleteWhich(ws []*configure.Which) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	removed, err := configure.DeleteTouchesAtomic(ws)
	if err != nil {
		return err
	}
	if removed {
		if err := refreshCoreAfterNodeChangeLocked(); err != nil {
			return fmt.Errorf("nodes were deleted, but failed to reload the core: %w", err)
		}
	}
	return nil
}
