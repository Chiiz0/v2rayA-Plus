package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/v2rayA/v2rayA/common/netTools/ports"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/pkg/util/log"
)

type SimpleInbound struct {
	Port     int            `json:"port"`
	Protocol string         `json:"protocol"`
	Listen   string         `json:"listen"`
	Settings SimpleSettings `json:"settings"`
	Tag      string         `json:"tag"`
}

type SimpleSettings struct {
	Auth string `json:"auth"`
	UDP  bool   `json:"udp"`
}

type SimpleRoutingRule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag"`
	OutboundTag string   `json:"outboundTag"`
}

type SimpleRouting struct {
	Rules []SimpleRoutingRule `json:"rules"`
}

type SimpleConfig struct {
	Log       *coreObj.Log             `json:"log,omitempty"`
	Inbounds  []SimpleInbound          `json:"inbounds"`
	Outbounds []coreObj.OutboundObject `json:"outbounds"`
	Routing   SimpleRouting            `json:"routing"`
}

type dockerProxyRuntime struct {
	ConfigBytes     []byte
	ImageName       string
	MountPath       string
	ServerName      string
	FrontServerName string
}

func runDockerCmd(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker command failed: %w (output: %s)", err, string(out))
	}
	return string(out), nil
}

func writeConfigToVolume(volumeName string, configBytes []byte) error {
	cmd := exec.Command(
		"docker", "run", "--rm", "-i",
		"--network", "none",
		"-v", volumeName+":/data",
		"alpine", "sh", "-c", "cat > /data/config.json",
	)
	cmd.Stdin = bytes.NewReader(configBytes)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to write config to volume: %w (output: %s)", err, string(out))
	}
	return nil
}

func findDockerProxy(port int) (configure.DockerProxy, bool, error) {
	proxies, err := configure.GetDockerProxies()
	if err != nil {
		return configure.DockerProxy{}, false, err
	}
	for _, p := range proxies {
		if p.Port == port {
			return p, true, nil
		}
	}
	return configure.DockerProxy{}, false, nil
}

func validateDockerProxy(port int, which configure.Which, frontWhich *configure.Which) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid docker proxy port %d", port)
	}
	if frontWhich != nil && which.TYPE == frontWhich.TYPE && which.ID == frontWhich.ID && which.Sub == frontWhich.Sub {
		return fmt.Errorf("front proxy cannot be the same as target proxy")
	}
	return nil
}

func getDockerProxyNetwork() string {
	network := strings.TrimSpace(conf.GetEnvironmentConfig().DockerProxyNetwork)
	if network == "" || strings.EqualFold(network, "none") {
		return ""
	}
	return network
}

func ensureDockerProxyNetwork(network string) error {
	if network == "" {
		return nil
	}
	if _, err := runDockerCmd("network", "inspect", network); err == nil {
		return nil
	}
	if _, err := runDockerCmd("network", "create", "--driver", "bridge", network); err != nil {
		return fmt.Errorf("failed to create docker proxy network %q: %w", network, err)
	}
	return nil
}

func buildDockerProxyRuntime(which configure.Which, frontWhich *configure.Which) (*dockerProxyRuntime, error) {
	sr, err := which.LocateServerRaw()
	if err != nil {
		return nil, fmt.Errorf("failed to locate server: %w", err)
	}

	variant, coreVersion, _ := where.GetV2rayServiceVersion()
	c, err := sr.ServerObj.Configuration(serverObj.PriorInfo{
		Variant:     variant,
		CoreVersion: coreVersion,
		Tag:         "proxy",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate server configuration: %w", err)
	}

	var frontServerName string
	var outbounds []coreObj.OutboundObject

	if frontWhich != nil {
		frontSr, err := frontWhich.LocateServerRaw()
		if err != nil {
			return nil, fmt.Errorf("failed to locate front server: %w", err)
		}
		frontServerName = frontSr.ServerObj.GetName()

		frontC, err := frontSr.ServerObj.Configuration(serverObj.PriorInfo{
			Variant:     variant,
			CoreVersion: coreVersion,
			Tag:         "pre-proxy",
		})
		if err != nil {
			return nil, fmt.Errorf("failed to generate front server configuration: %w", err)
		}

		// Find the target proxy's final outbound to point to "pre-proxy"
		var leafOutbound *coreObj.OutboundObject = &c.CoreOutbound
		for {
			if leafOutbound.ProxySettings != nil && leafOutbound.ProxySettings.Tag != "" {
				found := false
				for i := range c.ExtraOutbounds {
					if c.ExtraOutbounds[i].Tag == leafOutbound.ProxySettings.Tag {
						leafOutbound = &c.ExtraOutbounds[i]
						found = true
						break
					}
				}
				if !found {
					break
				}
			} else {
				break
			}
		}
		leafOutbound.ProxySettings = &coreObj.ProxySettings{
			Tag: "pre-proxy",
		}

		outbounds = append(outbounds, c.CoreOutbound)
		outbounds = append(outbounds, c.ExtraOutbounds...)
		outbounds = append(outbounds, frontC.CoreOutbound)
		outbounds = append(outbounds, frontC.ExtraOutbounds...)
	} else {
		outbounds = append(outbounds, c.CoreOutbound)
		outbounds = append(outbounds, c.ExtraOutbounds...)
	}

	// 5. Construct config JSON
	config := SimpleConfig{
		Inbounds: []SimpleInbound{
			{
				Port:     1080,
				Protocol: "socks",
				Listen:   "0.0.0.0",
				Settings: SimpleSettings{
					Auth: "noauth",
					UDP:  true,
				},
				Tag: "socks-in",
			},
		},
		Outbounds: outbounds,
		Routing: SimpleRouting{
			Rules: []SimpleRoutingRule{
				{
					Type:        "field",
					InboundTag:  []string{"socks-in"},
					OutboundTag: "proxy",
				},
			},
		},
	}
	configBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	imageName := "v2fly/v2fly-core:latest"
	if coreVersion != "" {
		versionTag := coreVersion
		if !strings.HasPrefix(versionTag, "v") {
			versionTag = "v" + versionTag
		}
		imageName = "v2fly/v2fly-core:" + versionTag
	}
	mountPath := "/etc/v2ray"
	if variant == where.Xray {
		imageName = "teddysun/xray:latest"
		mountPath = "/etc/xray"
	}

	return &dockerProxyRuntime{
		ConfigBytes:     configBytes,
		ImageName:       imageName,
		MountPath:       mountPath,
		ServerName:      sr.ServerObj.GetName(),
		FrontServerName: frontServerName,
	}, nil
}

func setupDockerProxyRuntime(port int, runtime *dockerProxyRuntime, recreateVolume bool, pauseAfterStart bool) error {
	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	volumeName := fmt.Sprintf("v2raya-socks-%d", port)

	_, _ = runDockerCmd("rm", "-f", containerName)
	if recreateVolume {
		_, _ = runDockerCmd("volume", "rm", "-f", volumeName)
	}

	_, err := runDockerCmd("volume", "create", volumeName)
	if err != nil {
		return fmt.Errorf("failed to create docker volume: %w", err)
	}

	proxyNetwork := getDockerProxyNetwork()
	if err = ensureDockerProxyNetwork(proxyNetwork); err != nil {
		return err
	}

	err = writeConfigToVolume(volumeName, runtime.ConfigBytes)
	if err != nil {
		return err
	}

	runArgs := []string{
		"run", "-d",
		"--name", containerName,
		"--restart", "always",
		"-p", fmt.Sprintf("%d:1080", port),
		"-v", fmt.Sprintf("%s:%s", volumeName, runtime.MountPath),
	}
	if proxyNetwork != "" {
		runArgs = append(runArgs, "--network", proxyNetwork)
	}
	runArgs = append(runArgs, runtime.ImageName)
	if strings.HasPrefix(runtime.ImageName, "v2fly/v2fly-core:") {
		runArgs = append(runArgs, "run", "-c", "/etc/v2ray/config.json")
	}
	_, err = runDockerCmd(runArgs...)
	if err != nil {
		return fmt.Errorf("failed to start docker container: %w", err)
	}

	if pauseAfterStart {
		if _, err = runDockerCmd("pause", containerName); err != nil {
			return fmt.Errorf("failed to pause docker container: %w", err)
		}
	}
	return nil
}

func CreateDockerProxy(which configure.Which, frontWhich *configure.Which, port int) error {
	if err := validateDockerProxy(port, which, frontWhich); err != nil {
		return err
	}

	proxies, _ := configure.GetDockerProxies()
	for _, p := range proxies {
		if p.Port == port {
			return fmt.Errorf("port %d is already used by another docker proxy", port)
		}
	}

	occupied, _, err := ports.IsPortOccupied([]string{strconv.Itoa(port) + ":tcp"})
	if occupied {
		return fmt.Errorf("port %d is already occupied on the host", port)
	}

	runtime, err := buildDockerProxyRuntime(which, frontWhich)
	if err != nil {
		return err
	}
	if err = setupDockerProxyRuntime(port, runtime, true, false); err != nil {
		_, _ = runDockerCmd("volume", "rm", "-f", fmt.Sprintf("v2raya-socks-%d", port))
		return err
	}

	proxy := configure.DockerProxy{
		Port:            port,
		Which:           which,
		FrontWhich:      frontWhich,
		ServerName:      runtime.ServerName,
		FrontServerName: runtime.FrontServerName,
		ContainerName:   fmt.Sprintf("v2raya-socks-%d", port),
		Status:          "running",
	}
	err = configure.SaveDockerProxy(proxy)
	if err != nil {
		log.Error("failed to save docker proxy to db: %v", err)
	}

	return nil
}

func UpdateDockerProxy(port int, which configure.Which, frontWhich *configure.Which) error {
	if err := validateDockerProxy(port, which, frontWhich); err != nil {
		return err
	}

	proxy, ok, err := findDockerProxy(port)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("docker proxy on port %d does not exist", port)
	}

	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	out, inspectErr := runDockerCmd("inspect", "--format", "{{.State.Paused}}", containerName)
	wasPaused := strings.TrimSpace(out) == "true" || proxy.Status == "paused"
	if inspectErr != nil && proxy.Status == "paused" {
		wasPaused = true
	}

	runtime, err := buildDockerProxyRuntime(which, frontWhich)
	if err != nil {
		return err
	}
	if err = setupDockerProxyRuntime(port, runtime, false, wasPaused); err != nil {
		return err
	}

	proxy.Which = which
	proxy.FrontWhich = frontWhich
	proxy.ServerName = runtime.ServerName
	proxy.FrontServerName = runtime.FrontServerName
	proxy.ContainerName = containerName
	if wasPaused {
		proxy.Status = "paused"
	} else {
		proxy.Status = "running"
	}
	return configure.SaveDockerProxy(proxy)
}

func PauseDockerProxy(port int) error {
	proxy, ok, err := findDockerProxy(port)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("docker proxy on port %d does not exist", port)
	}
	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	if _, err = runDockerCmd("pause", containerName); err != nil {
		if !strings.Contains(err.Error(), "is already paused") {
			return fmt.Errorf("failed to pause docker proxy on port %d: %w", port, err)
		}
	}
	proxy.Status = "paused"
	return configure.SaveDockerProxy(proxy)
}

func ResumeDockerProxy(port int) error {
	proxy, ok, err := findDockerProxy(port)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("docker proxy on port %d does not exist", port)
	}
	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	out, inspectErr := runDockerCmd("inspect", "--format", "{{.State.Running}}\t{{.State.Paused}}", containerName)
	if inspectErr != nil {
		runtime, err := buildDockerProxyRuntime(proxy.Which, proxy.FrontWhich)
		if err != nil {
			return err
		}
		if err = setupDockerProxyRuntime(port, runtime, false, false); err != nil {
			return err
		}
	} else {
		parts := strings.Split(strings.TrimSpace(out), "\t")
		running := len(parts) > 0 && parts[0] == "true"
		paused := len(parts) > 1 && parts[1] == "true"
		if paused {
			if _, err = runDockerCmd("unpause", containerName); err != nil {
				return fmt.Errorf("failed to resume docker proxy on port %d: %w", port, err)
			}
		} else if !running {
			if _, err = runDockerCmd("start", containerName); err != nil {
				return fmt.Errorf("failed to start docker proxy on port %d: %w", port, err)
			}
		}
	}
	proxy.Status = "running"
	return configure.SaveDockerProxy(proxy)
}

func GetDockerProxies() ([]configure.DockerProxy, error) {
	proxies, err := configure.GetDockerProxies()
	if err != nil {
		return nil, err
	}

	// Sync status with actual docker container status
	out, err := runDockerCmd("ps", "-a", "--filter", "name=v2raya-socks-", "--format", "{{.Names}}\t{{.Status}}")
	if err != nil {
		return proxies, nil
	}

	statusMap := make(map[string]string)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			statusMap[parts[0]] = parts[1]
		}
	}

	for i, p := range proxies {
		if status, ok := statusMap[p.ContainerName]; ok {
			if strings.Contains(status, "Paused") {
				proxies[i].Status = "paused"
			} else if strings.HasPrefix(status, "Up") {
				proxies[i].Status = "running"
			} else {
				proxies[i].Status = status
			}
		} else {
			proxies[i].Status = "stopped"
		}
	}

	return proxies, nil
}

func DeleteDockerProxy(port int) error {
	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	volumeName := fmt.Sprintf("v2raya-socks-%d", port)

	// Force removal also handles paused containers.
	_, _ = runDockerCmd("rm", "-f", containerName)

	// Remove volume
	_, _ = runDockerCmd("volume", "rm", "-f", volumeName)

	// Remove from DB
	err := configure.RemoveDockerProxy(port)
	if err != nil {
		return fmt.Errorf("failed to remove docker proxy from db: %w", err)
	}

	return nil
}
