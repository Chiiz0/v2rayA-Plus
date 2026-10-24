package service

import (
	"fmt"

	"github.com/v2rayA/v2rayA/db/configure"
)

func AddOutbound(name string) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	return configure.AddOutbound(name)
}

func UpdateOutboundSetting(name string, setting configure.OutboundSetting) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	if err := configure.SetOutboundSetting(name, setting); err != nil {
		return err
	}
	if err := refreshCoreAfterNodeChangeLocked(); err != nil {
		return fmt.Errorf("outbound settings were saved, but failed to reload the core: %w", err)
	}
	return nil
}

func DeleteOutbound(name string) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	if name == "" || name == "proxy" {
		return fmt.Errorf("outbound %q cannot be deleted", name)
	}
	if configure.GetConnectedServersByOutbound(name).Len() > 0 {
		if err := disconnectLocked(configure.Which{Outbound: name}, true); err != nil {
			return err
		}
	}
	return configure.RemoveOutbound(name)
}
