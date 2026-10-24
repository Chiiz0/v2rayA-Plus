package service

import (
	"errors"
	"fmt"

	"github.com/v2rayA/v2rayA/core/v2ray"
	coreservice "github.com/v2rayA/v2rayA/core/v2ray/service"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
)

func UpdateOutboundConnections(outbound string, touches []*configure.Which) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	limit := 0
	if len(touches) > 0 {
		if err := checkAssetsExist(GetSetting()); err != nil {
			return err
		}
		variant, err := coreservice.CheckV5()
		if err != nil {
			return err
		}
		if variant != where.V2ray {
			limit = 1
		}
	}
	var reload func() error
	if v2ray.ProcessManager.Running() {
		reload = v2ray.UpdateV2RayConfig
	}
	return replaceOutboundConnectionsLocked(outbound, touches, limit, reload)
}

func replaceOutboundConnectionsLocked(outbound string, touches []*configure.Which, limit int, reload func() error) error {
	previous, err := configure.ReplaceOutboundConnections(outbound, touches, limit)
	if err != nil {
		return err
	}
	if reload == nil {
		return nil
	}
	if err := reload(); err != nil {
		failure := fmt.Errorf("failed to reload the core after replacing outbound connections: %w", err)
		// Restore even an empty group, and even if the failed reload stopped the
		// process. Checking Running here would strand the failed selection.
		if rollbackErr := previous.Restore(); rollbackErr != nil {
			return errors.Join(failure, fmt.Errorf("failed to restore previous connections: %w", rollbackErr))
		}
		if rollbackErr := reload(); rollbackErr != nil {
			return errors.Join(failure, fmt.Errorf("previous connections restored, but core recovery failed: %w", rollbackErr))
		}
		return fmt.Errorf("previous connections restored: %w", failure)
	}
	return nil
}
