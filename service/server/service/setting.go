package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/core/ipforward"
	"github.com/v2rayA/v2rayA/core/v2ray"
	"github.com/v2rayA/v2rayA/core/v2ray/asset"
	"github.com/v2rayA/v2rayA/db"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/db/store"
	"github.com/v2rayA/v2rayA/pkg/util/log"
	"time"
)

func GetSetting() *configure.Setting {
	s := configure.GetSettingNotNil()
	if s == nil {
		s = configure.NewSetting()
		_ = configure.SetSetting(s)
	}
	return s
}

// PatchSetting merges the caller's fields while holding the same lock as node
// and core operations. Raw unknown fields survive a modern or legacy UI save.
func PatchSetting(patch json.RawMessage) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	// Start stops the old core before launching its replacement. Capture the
	// recovery obligation now, not after a failed launch has left it stopped.
	wasRunning := v2ray.ProcessManager.Running()
	var reload func() error
	if wasRunning && configure.GetConnectedServers().Len() > 0 {
		reload = v2ray.UpdateV2RayConfig
	}
	return patchSettingLocked(patch, reload)
}

func patchSettingLocked(patch json.RawMessage, reload func() error) (err error) {
	var old []byte
	var setting *configure.Setting
	err = db.Update(func(tx store.WriteTx) error {
		var e error
		old, e = tx.Get([]byte("system"), []byte("setting"))
		if e != nil {
			return e
		}
		merged, value, e := mergeSetting(old, patch)
		if e != nil {
			return e
		}
		setting = value
		if (setting.Transparent == configure.TransparentGfwlist || setting.RulePortMode == configure.GfwlistMode) && !asset.DoesV2rayAssetExist("LoyalsoldierSite.dat") {
			return fmt.Errorf("cannot find GFWList files. update GFWList and try again")
		}
		return tx.Put([]byte("system"), []byte("setting"), merged)
	})
	if err != nil {
		return err
	}
	if setting.IpForward && !ipforward.IsIpForwardOn() {
		if e := ipforward.WriteIpForward(true); e != nil {
			log.Warn("UpdateSetting: %v", e)
		}
	}
	defer func() {
		if err == nil {
			return
		}
		restoreErr := db.Update(func(tx store.WriteTx) error {
			if old == nil {
				return tx.Delete([]byte("system"), []byte("setting"))
			}
			return tx.Put([]byte("system"), []byte("setting"), old)
		})
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore previous settings: %w", restoreErr))
			return
		}
		if reload != nil {
			if restoreErr = reload(); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("previous settings restored, but core recovery failed: %w", restoreErr))
			}
		}
	}()
	//如果v2ray正在运行且有连接，则重写配置并重启连接，使得对透明代理、TCPFastOpen等配置的修改立即生效
	if reload != nil {
		err = reload()
		if err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
	}
	if conf.TickerUpdateGFWList != nil {
		if setting.GFWListAutoUpdateMode == configure.AutoUpdateAtIntervals {
			conf.TickerUpdateGFWList.Reset(time.Duration(setting.GFWListAutoUpdateIntervalHour) * time.Hour)
		} else {
			conf.TickerUpdateGFWList.Reset(24 * time.Hour * 365 * 100)
		}
	}
	if conf.TickerUpdateSubscription != nil {
		if setting.SubscriptionAutoUpdateMode == configure.AutoUpdateAtIntervals {
			conf.TickerUpdateSubscription.Reset(time.Duration(setting.SubscriptionAutoUpdateIntervalHour) * time.Hour)
		} else {
			conf.TickerUpdateSubscription.Reset(24 * time.Hour * 365 * 100)
		}
	}
	return
}

// mergeSetting rejects malformed objects and null scalar values before changing
// state. It does not round JSON numbers through float64.
func mergeSetting(previous, patch []byte) ([]byte, *configure.Setting, error) {
	var updates map[string]json.RawMessage
	if err := json.Unmarshal(patch, &updates); err != nil || updates == nil {
		return nil, nil, fmt.Errorf("settings must be a JSON object")
	}
	defaults, _ := json.Marshal(configure.NewSetting())
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(defaults, &fields)
	known := make(map[string]bool, len(fields))
	for key := range fields {
		known[key] = true
	}
	if len(previous) > 0 {
		var stored map[string]json.RawMessage
		if err := json.Unmarshal(previous, &stored); err != nil || stored == nil {
			return nil, nil, fmt.Errorf("stored settings are invalid")
		}
		for key, value := range stored {
			fields[key] = value
		}
	}
	for key, value := range updates {
		// Unknown fields sent by a newer client must not alter stored state.
		if !known[key] {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, nil, fmt.Errorf("setting %s cannot be null", key)
		}
		fields[key] = value
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	var setting configure.Setting
	if err = json.Unmarshal(merged, &setting); err != nil {
		return nil, nil, err
	}
	if setting.MuxOn == configure.Yes && (setting.Mux < 1 || setting.Mux > 1024) {
		return nil, nil, fmt.Errorf("mux should be between 1 and 1024")
	}
	// A zero or overflowing duration must never reach time.Ticker.Reset.
	for _, value := range []struct {
		mode  configure.AutoUpdateMode
		hours int
	}{
		{setting.GFWListAutoUpdateMode, setting.GFWListAutoUpdateIntervalHour},
		{setting.SubscriptionAutoUpdateMode, setting.SubscriptionAutoUpdateIntervalHour},
	} {
		if value.mode == configure.AutoUpdateAtIntervals && (value.hours < 1 || value.hours > 876000) {
			return nil, nil, fmt.Errorf("automatic update interval must be between 1 and 876000 hours")
		}
	}
	return merged, &setting, nil
}
