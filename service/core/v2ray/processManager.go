package v2ray

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/core/specialMode"
	"github.com/v2rayA/v2rayA/core/v2ray/asset"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/pkg/util/log"
)

type CoreProcessManager struct {
	p  *Process
	mu sync.Mutex
}

var ProcessManager CoreProcessManager
var newCoreProcess = NewProcess
var cleanupFailedCoreStart = func() {
	// Startup hooks may have failed after host state was installed. Roll
	// back directly so another failing hook cannot interrupt the cleanup.
	removeResolvHijacker()
	deleteTransparentProxyRules()
	specialMode.StopDNSSupervisor()
}

func (m *CoreProcessManager) beforeStop(p *Process) {
	setting := p.template.Setting
	if setting == nil {
		// Callers can hold m.mu, so do not let the public stop method try
		// to retrieve the running template by taking the same lock again.
		setting = configure.GetSettingNotNil()
	}
	m.CheckAndStopTransparentProxy(setting)
	specialMode.StopDNSSupervisor()

	if corehook := conf.GetEnvironmentConfig().CoreHook; corehook != "" {
		hook := strings.Split(corehook, " ")
		hook = append(hook, "--stage=pre-stop", fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
		log.Info("Execute the core pre stop hook: %v", hook)
		b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
		if len(b) > 0 {
			log.Info("Executing the core pre stop hook: %v", string(b))
		}
		if err != nil {
			log.Warn("Error when executing the core pre stop hook: %v", err)
			return
		}
	}
}

func (m *CoreProcessManager) GetRunningTemplate() *Template {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.p == nil {
		return nil
	}
	return m.p.template
}

func (m *CoreProcessManager) CheckAndSetupTransparentProxy(checkRunning bool, setting *configure.Setting) (err error) {
	defer func() {
		if err != nil {
			removeResolvHijacker()
		}
	}()
	if setting != nil {
		setting.FillEmpty()
	} else {
		setting = configure.GetSettingNotNil()
	}
	if (!checkRunning || ProcessManager.Running()) && IsTransparentOn(setting) {
		deleteTransparentProxyRules()

		if thook := conf.GetEnvironmentConfig().TransparentHook; thook != "" {
			hook := strings.Split(thook, " ")
			hook = append(hook,
				fmt.Sprintf("--transparent-type=%v", setting.TransparentType),
				"--stage=pre-start",
				fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
			log.Info("Execute the transparent pre start hook: %v", hook)
			b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
			if len(b) > 0 {
				log.Info("Executing the transparent pre start hook: %v", string(b))
			}
			if err != nil {
				return fmt.Errorf("error when executing the transparent pre start hook: %w", err)
			}
		}

		err = writeTransparentProxyRules()

		if thook := conf.GetEnvironmentConfig().TransparentHook; thook != "" {
			hook := strings.Split(thook, " ")
			hook = append(hook,
				fmt.Sprintf("--transparent-type=%v", setting.TransparentType),
				"--stage=post-start",
				fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
			log.Info("Execute the transparent post start hook: %v", hook)
			b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
			if len(b) > 0 {
				log.Info("Executing the transparent post start hook: %v", string(b))
			}
			if err != nil {
				return fmt.Errorf("error when executing the transparent post start hook: %w", err)
			}
		}
	}
	return
}

func (m *CoreProcessManager) CheckAndStopTransparentProxy(setting *configure.Setting) {
	// Restore even if the core never finished starting or a stop hook fails.
	defer removeResolvHijacker()
	if setting == nil {
		if t := m.GetRunningTemplate(); t != nil {
			setting = t.Setting
		} else {
			return
		}
	}
	if setting.Transparent != configure.TransparentClose {
		if thook := conf.GetEnvironmentConfig().TransparentHook; thook != "" {
			hook := strings.Split(thook, " ")
			hook = append(hook,
				fmt.Sprintf("--transparent-type=%v", setting.TransparentType),
				"--stage=pre-stop",
				fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
			log.Info("Execute the transparent pre stop hook: %v", hook)
			b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
			if len(b) > 0 {
				log.Info("Executing the transparent pre stop hook: %v", string(b))
			}
			if err != nil {
				log.Warn("Error when executing the transparent pre stop hook: %v", err)
				return
			}
		}

		deleteTransparentProxyRules()

		if thook := conf.GetEnvironmentConfig().TransparentHook; thook != "" {
			hook := strings.Split(thook, " ")
			hook = append(hook,
				fmt.Sprintf("--transparent-type=%v", setting.TransparentType),
				"--stage=post-stop",
				fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
			log.Info("Execute the transparent post stop hook: %v", hook)
			b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
			if len(b) > 0 {
				log.Info("Executing the transparent post stop hook: %v", string(b))
			}
			if err != nil {
				log.Warn("Error when executing the transparent post stop hook: %v", err)
				return
			}
		}
	}
}

func (m *CoreProcessManager) afterStop(p *Process) {
	if corehook := conf.GetEnvironmentConfig().CoreHook; corehook != "" {
		hook := strings.Split(corehook, " ")
		hook = append(hook, "--stage=post-stop", fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
		log.Info("Execute the core post stop hook: %v", hook)
		b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
		if len(b) > 0 {
			log.Info("Executing the core post stop hook: %v", string(b))
		}
		if err != nil {
			log.Warn("Error when executing the core post stop hook: %v", err)
			return
		}
	}
}

func (m *CoreProcessManager) Stop(saveRunning bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stop(saveRunning)
	if m.p != nil {
		for _, pm := range m.p.pluginManagers {
			_ = pm.Kill()
		}
		m.p.pluginManagers = nil
	}
}

func (m *CoreProcessManager) stop(saveRunning bool) {
	if m.p == nil {
		removeResolvHijacker()
		return
	}

	m.beforeStop(m.p)

	err := m.p.Close()
	if err != nil {
		log.Warn("CoreProcessManager.Stop: %v", err)
	}
	if saveRunning {
		configure.SetRunning(false)
	}

	m.afterStop(m.p)

	m.p = nil
}

func (m *CoreProcessManager) beforeStart(t *Template) (err error) {
	if (t.Setting.Transparent == configure.TransparentGfwlist || t.Setting.RulePortMode == configure.GfwlistMode) && !asset.DoesV2rayAssetExist("LoyalsoldierSite.dat") {
		return fmt.Errorf("cannot find GFWList files. update GFWList and try again")
	}

	if err = t.CheckInboundPortsOccupied(); err != nil {
		return err
	}

	if corehook := conf.GetEnvironmentConfig().CoreHook; corehook != "" {
		hook := strings.Split(corehook, " ")
		hook = append(hook, "--stage=pre-start", fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
		log.Info("Execute the core pre start hook: %v", hook)
		b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
		if len(b) > 0 {
			log.Info("Executing the core pre start hook: %v", string(b))
		}
		if err != nil {
			return fmt.Errorf("error when executing the core pre start hook: %w", err)
		}
	}
	return nil
}

func (m *CoreProcessManager) afterStart(t *Template) (err error) {
	if err = m.CheckAndSetupTransparentProxy(false, t.Setting); err != nil {
		return err
	}
	specialMode.CheckAndSetupDNSSupervisor()

	if corehook := conf.GetEnvironmentConfig().CoreHook; corehook != "" {
		hook := strings.Split(corehook, " ")
		hook = append(hook, "--stage=post-start", fmt.Sprintf("--v2raya-confdir=%v", conf.GetEnvironmentConfig().Config))
		log.Info("Execute the core post start hook: %v", hook)
		b, err := exec.Command(hook[0], hook[1:]...).CombinedOutput()
		if len(b) > 0 {
			log.Info("Executing the core post start hook: %v", string(b))
		}
		if err != nil {
			return fmt.Errorf("error when executing the core post start hook: %w", err)
		}
	}
	return nil
}

func (m *CoreProcessManager) Start(t *Template) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stop(true)

	process, err := newCoreProcess(t, func() error {
		return m.beforeStart(t)
	}, func() error {
		return m.afterStart(t)
	}, m.handleUnexpectedStop)
	if err != nil {
		// afterStart can install the hijacker before a post-start hook or
		// readiness check fails, while m.p has not been assigned yet.
		cleanupFailedCoreStart()
		return err
	}
	m.p = process
	defer func() {
		if err != nil {
			m.stop(true)
		}
	}()

	configure.SetRunning(true)
	return nil
}

func (m *CoreProcessManager) handleUnexpectedStop(p *Process) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// An old process can finish waiting after a replacement has started.
	// Only the current owner may remove the host's proxy and DNS state.
	if m.p != p {
		return
	}
	m.beforeStop(p)
	m.afterStop(p)
}

// Running reports if v2ray-core is running.
func (m *CoreProcessManager) Running() bool {
	return m.p != nil
}

func (m *CoreProcessManager) Process() *Process {
	return m.p
}
