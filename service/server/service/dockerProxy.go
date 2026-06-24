package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/v2rayA/v2rayA/common/netTools/ports"
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
	Log       *coreObj.Log            `json:"log,omitempty"`
	Inbounds  []SimpleInbound         `json:"inbounds"`
	Outbounds []coreObj.OutboundObject `json:"outbounds"`
	Routing   SimpleRouting           `json:"routing"`
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
	cmd := exec.Command("docker", "run", "--rm", "-i", "-v", volumeName+":/data", "alpine", "sh", "-c", "cat > /data/config.json")
	cmd.Stdin = bytes.NewReader(configBytes)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to write config to volume: %w (output: %s)", err, string(out))
	}
	return nil
}

func CreateDockerProxy(which configure.Which, frontWhich *configure.Which, port int) error {
	// 1. Check if the port is already configured in DB
	proxies, _ := configure.GetDockerProxies()
	for _, p := range proxies {
		if p.Port == port {
			return fmt.Errorf("port %d is already used by another docker proxy", port)
		}
	}

	// 2. Check if the port is occupied on the host
	occupied, _, err := ports.IsPortOccupied([]string{strconv.Itoa(port) + ":tcp"})
	if occupied {
		return fmt.Errorf("port %d is already occupied on the host", port)
	}

	// 3. Locate ServerRaw
	sr, err := which.LocateServerRaw()
	if err != nil {
		return fmt.Errorf("failed to locate server: %w", err)
	}

	// 4. Generate outbound config
	variant, coreVersion, _ := where.GetV2rayServiceVersion()
	c, err := sr.ServerObj.Configuration(serverObj.PriorInfo{
		Variant:     variant,
		CoreVersion: coreVersion,
		Tag:         "proxy",
	})
	if err != nil {
		return fmt.Errorf("failed to generate server configuration: %w", err)
	}

	var frontServerName string
	var outbounds []coreObj.OutboundObject

	if frontWhich != nil {
		frontSr, err := frontWhich.LocateServerRaw()
		if err != nil {
			return fmt.Errorf("failed to locate front server: %w", err)
		}
		frontServerName = frontSr.ServerObj.GetName()

		frontC, err := frontSr.ServerObj.Configuration(serverObj.PriorInfo{
			Variant:     variant,
			CoreVersion: coreVersion,
			Tag:         "pre-proxy",
		})
		if err != nil {
			return fmt.Errorf("failed to generate front server configuration: %w", err)
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
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// 6. Set up docker container and volume
	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	volumeName := fmt.Sprintf("v2raya-socks-%d", port)

	// Clean up existing container/volume with same name if any
	_, _ = runDockerCmd("rm", "-f", containerName)
	_, _ = runDockerCmd("volume", "rm", "-f", volumeName)

	// Create volume
	_, err = runDockerCmd("volume", "create", volumeName)
	if err != nil {
		return fmt.Errorf("failed to create docker volume: %w", err)
	}

	// Determine image and mount path
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

	// Write config to volume
	err = writeConfigToVolume(volumeName, configBytes)
	if err != nil {
		// Clean up
		_, _ = runDockerCmd("volume", "rm", "-f", volumeName)
		return err
	}

	// Run container
	runArgs := []string{
		"run", "-d",
		"--name", containerName,
		"--restart", "always",
		"-p", fmt.Sprintf("%d:1080", port),
		"-v", fmt.Sprintf("%s:%s", volumeName, mountPath),
		imageName,
	}
	if variant == where.V2ray {
		runArgs = append(runArgs, "run", "-c", "/etc/v2ray/config.json")
	}
	_, err = runDockerCmd(runArgs...)
	if err != nil {
		// Clean up
		_, _ = runDockerCmd("volume", "rm", "-f", volumeName)
		return fmt.Errorf("failed to start docker container: %w", err)
	}

	// 7. Save to DB
	proxy := configure.DockerProxy{
		Port:            port,
		Which:           which,
		FrontWhich:      frontWhich,
		ServerName:      sr.ServerObj.GetName(),
		FrontServerName: frontServerName,
		ContainerName:   containerName,
		Status:          "running",
	}
	err = configure.SaveDockerProxy(proxy)
	if err != nil {
		log.Error("failed to save docker proxy to db: %v", err)
	}

	return nil
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
			if strings.HasPrefix(status, "Up") {
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

	// Stop and remove container
	_, _ = runDockerCmd("stop", containerName)
	_, _ = runDockerCmd("rm", containerName)

	// Remove volume
	_, _ = runDockerCmd("volume", "rm", volumeName)

	// Remove from DB
	err := configure.RemoveDockerProxy(port)
	if err != nil {
		return fmt.Errorf("failed to remove docker proxy from db: %w", err)
	}

	return nil
}
