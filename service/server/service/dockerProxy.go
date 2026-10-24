package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/v2rayA/v2rayA/common/netTools/ports"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/core/serverObj"
	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"github.com/v2rayA/v2rayA/db/configure"
	"golang.org/x/mod/semver"
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
	Core            configure.DockerProxyCore
	RequestedCore   configure.DockerProxyCore
	ServerName      string
	FrontServerName string
	VLESSEncryption bool
	XHTTP           bool
}

type dockerProxyCoreRuntime struct {
	RequestedCore configure.DockerProxyCore
	Core          configure.DockerProxyCore
	Variant       where.Variant
	CoreVersion   string
	ImageName     string
	MountPath     string
}

func runDockerCmd(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return runDockerCmdContext(ctx, args...)
}

func runDockerCmdContext(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("docker command failed: %w", ctx.Err())
	}
	if err != nil {
		return string(out), fmt.Errorf("docker command failed: %w (output: %s)", err, string(out))
	}
	return string(out), nil
}

func writeConfigToVolume(volumeName string, configBytes []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return writeConfigToVolumeContext(ctx, volumeName, configBytes)
}

func writeConfigToVolumeContext(ctx context.Context, volumeName string, configBytes []byte) error {
	helperName := fmt.Sprintf("v2raya-config-write-%d-%d", os.Getpid(), time.Now().UnixNano())
	defer func() {
		// Canceling the Docker CLI does not stop its container. Only remove
		// this write helper, using a fresh deadline after the request expires.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanup := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", helperName)
		cleanup.WaitDelay = 3 * time.Second
		_ = cleanup.Run()
	}()
	cmd := exec.CommandContext(
		ctx, "docker", "run", "--rm", "--name", helperName, "-i",
		"--network", "none",
		"-v", volumeName+":/data",
		"alpine", "sh", "-c", "cat > /data/config.json",
	)
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdin = bytes.NewReader(configBytes)
	// Docker diagnostics can echo the configuration received on stdin.
	// Never return or log them because the configuration contains credentials.
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("failed to write config to volume: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("failed to write config to volume: %w", err)
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

func selectDockerProxyCore(core configure.DockerProxyCore, detectedVariant where.Variant, detectedVersion string) (dockerProxyCoreRuntime, error) {
	core = configure.DockerProxyCore(strings.ToLower(strings.TrimSpace(string(core))))
	if core == "" {
		core = configure.DockerProxyCoreAuto
	}
	if core != configure.DockerProxyCoreAuto && core != configure.DockerProxyCoreV2Fly && core != configure.DockerProxyCoreXray {
		return dockerProxyCoreRuntime{}, fmt.Errorf("invalid docker proxy core %q: expected auto, v2fly, or xray", core)
	}

	actualCore := core
	if actualCore == configure.DockerProxyCoreAuto {
		if detectedVariant == where.Xray {
			actualCore = configure.DockerProxyCoreXray
		} else {
			actualCore = configure.DockerProxyCoreV2Fly
		}
	}

	runtime := dockerProxyCoreRuntime{
		RequestedCore: core,
		Core:          actualCore,
	}
	if actualCore == configure.DockerProxyCoreXray {
		runtime.Variant = where.Xray
		runtime.ImageName = "teddysun/xray:latest"
		runtime.MountPath = "/etc/xray"
		// The Docker image can differ from the main service's Xray version.
		// Validate XHTTP and encrypted VLESS against the actual image.
		return runtime, nil
	}

	runtime.Variant = where.V2ray
	runtime.ImageName = "v2fly/v2fly-core:latest"
	runtime.MountPath = "/etc/v2ray"
	if detectedVariant == where.V2ray && detectedVersion != "" {
		runtime.CoreVersion = detectedVersion
		versionTag := detectedVersion
		if !strings.HasPrefix(versionTag, "v") {
			versionTag = "v" + versionTag
		}
		runtime.ImageName = "v2fly/v2fly-core:" + versionTag
	}
	return runtime, nil
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

func buildDockerProxyRuntime(which configure.Which, frontWhich *configure.Which, core configure.DockerProxyCore) (*dockerProxyRuntime, error) {
	sr, err := which.LocateServerRaw()
	if err != nil {
		return nil, fmt.Errorf("failed to locate server: %w", err)
	}

	detectedVariant, detectedVersion, _ := where.GetV2rayServiceVersion()
	coreRuntime, err := selectDockerProxyCore(core, detectedVariant, detectedVersion)
	if err != nil {
		return nil, err
	}
	var front serverObj.ServerObj
	if frontWhich != nil {
		frontSr, err := frontWhich.LocateServerRaw()
		if err != nil {
			return nil, fmt.Errorf("failed to locate front server: %w", err)
		}
		front = frontSr.ServerObj
	}
	return buildDockerProxyRuntimeForServers(sr.ServerObj, front, coreRuntime)
}

func buildDockerProxyRuntimeForServers(server, front serverObj.ServerObj, coreRuntime dockerProxyCoreRuntime) (*dockerProxyRuntime, error) {
	c, err := server.Configuration(serverObj.PriorInfo{
		Variant:     coreRuntime.Variant,
		CoreVersion: coreRuntime.CoreVersion,
		Tag:         "proxy",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate server configuration: %w", err)
	}

	var frontServerName string
	var outbounds []coreObj.OutboundObject

	if front != nil {
		frontServerName = front.GetName()

		frontC, err := front.Configuration(serverObj.PriorInfo{
			Variant:     coreRuntime.Variant,
			CoreVersion: coreRuntime.CoreVersion,
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
		if stream := leafOutbound.StreamSettings; stream != nil && strings.EqualFold(stream.Network, "xhttp") {
			// Legacy proxySettings bypasses the target's transport. XHTTP must
			// send both its upload and independent download sockets via the front.
			if stream.Sockopt == nil {
				stream.Sockopt = &coreObj.Sockopt{}
			}
			stream.Sockopt.DialerProxy = "pre-proxy"
			leafOutbound.ProxySettings = nil
			if stream.XHTTPSettings != nil && len(stream.XHTTPSettings.Extra) != 0 {
				extra, err := chainXHTTPDownloads(stream.XHTTPSettings.Extra, "pre-proxy", 0)
				if err != nil {
					return nil, fmt.Errorf("failed to chain XHTTP downloads: %w", err)
				}
				stream.XHTTPSettings.Extra = extra
			}
		} else {
			leafOutbound.ProxySettings = &coreObj.ProxySettings{Tag: "pre-proxy"}
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
	var vlessEncryption, xhttp bool
	for _, outbound := range outbounds {
		if outbound.StreamSettings != nil && strings.EqualFold(outbound.StreamSettings.Network, "xhttp") {
			xhttp = true
		}
		if outbound.Protocol != "vless" {
			continue
		}
		nextServers, _ := outbound.Settings.Vnext.([]coreObj.Vnext)
		for _, next := range nextServers {
			for _, user := range next.Users {
				if user.Encryption != "" && user.Encryption != "none" {
					vlessEncryption = true
				}
			}
		}
	}

	return &dockerProxyRuntime{
		ConfigBytes:     configBytes,
		ImageName:       coreRuntime.ImageName,
		MountPath:       coreRuntime.MountPath,
		Core:            coreRuntime.Core,
		RequestedCore:   coreRuntime.RequestedCore,
		ServerName:      server.GetName(),
		FrontServerName: frontServerName,
		VLESSEncryption: vlessEncryption,
		XHTTP:           xhttp,
	}, nil
}

// Preserve the complete extra object, including unknown fields and large JSON
// numbers, while ensuring split download sockets obey the selected front proxy.
func chainXHTTPDownloads(extra json.RawMessage, tag string, depth int) (json.RawMessage, error) {
	if depth > 16 {
		return nil, fmt.Errorf("download settings are nested too deeply")
	}
	if !gjson.ValidBytes(extra) || !gjson.ParseBytes(extra).IsObject() {
		return nil, fmt.Errorf("extra must be a JSON object")
	}
	download := gjson.GetBytes(extra, "downloadSettings")
	if !download.Exists() {
		return extra, nil
	}
	if !download.IsObject() {
		return nil, fmt.Errorf("downloadSettings must be a JSON object")
	}
	if options := download.Get("sockopt"); options.Exists() && options.Type != gjson.Null && !options.IsObject() {
		return nil, fmt.Errorf("downloadSettings.sockopt must be a JSON object")
	}
	stream, err := sjson.SetBytes([]byte(download.Raw), "sockopt.dialerProxy", tag)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"xhttpSettings", "splithttpSettings"} {
		settings := download.Get(key)
		if !settings.Exists() {
			continue
		}
		if !settings.IsObject() {
			return nil, fmt.Errorf("downloadSettings.%s must be a JSON object", key)
		}
		path, raw := key, settings.Raw
		if nested := settings.Get("extra"); nested.Exists() {
			path, raw = key+".extra", nested.Raw
		}
		updated, err := chainXHTTPDownloads(json.RawMessage(raw), tag, depth+1)
		if err != nil {
			return nil, err
		}
		stream, err = sjson.SetRawBytes(stream, path, updated)
		if err != nil {
			return nil, err
		}
	}
	return sjson.SetRawBytes(extra, "downloadSettings", stream)
}

func validateDockerProxyConfig(runtime *dockerProxyRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return validateDockerProxyConfigContext(ctx, runtime)
}

func validateDockerProxyConfigContext(ctx context.Context, runtime *dockerProxyRuntime) error {
	var features []string
	if runtime.VLESSEncryption {
		features = append(features, "VLESS Encryption")
	}
	if runtime.XHTTP {
		features = append(features, "XHTTP")
	}
	if len(features) == 0 {
		return nil
	}
	feature := strings.Join(features, " / ")
	if runtime.Core != configure.DockerProxyCoreXray {
		return fmt.Errorf("%s requires Xray; select Xray for this Docker proxy", feature)
	}
	if runtime.XHTTP {
		output, err := runDockerProxyCoreCheck(ctx, runtime.ImageName, nil, "version")
		if err != nil {
			return fmt.Errorf("failed to read Xray version from Docker image %q: %w", runtime.ImageName, err)
		}
		fields := strings.Fields(string(output))
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Xray") || !semver.IsValid("v"+strings.TrimPrefix(fields[1], "v")) {
			return fmt.Errorf("cannot determine Xray version from Docker image %q; update the image", runtime.ImageName)
		}
		var config SimpleConfig
		if err := json.Unmarshal(runtime.ConfigBytes, &config); err != nil {
			return fmt.Errorf("invalid generated Docker proxy configuration")
		}
		for _, outbound := range config.Outbounds {
			stream := outbound.StreamSettings
			if stream == nil || !strings.EqualFold(stream.Network, "xhttp") {
				continue
			}
			settings := stream.XHTTPSettings
			if settings == nil {
				return fmt.Errorf("missing XHTTP settings in generated Docker proxy configuration")
			}
			if err := serverObj.ValidateXHTTPCore(settings.Mode, settings.Extra, serverObj.PriorInfo{Variant: where.Xray, CoreVersion: fields[1]}); err != nil {
				return fmt.Errorf("Docker Xray image %q: %w", runtime.ImageName, err)
			}
		}
	}
	if _, err := runDockerProxyCoreCheck(ctx, runtime.ImageName, runtime.ConfigBytes, "run", "-test", "-config", "stdin:"); err != nil {
		return fmt.Errorf("failed to validate %s with Docker image %q; ensure Docker is available and update the Xray image to a version supporting the node's settings: %w", feature, runtime.ImageName, err)
	}
	return nil
}

func runDockerProxyCoreCheck(ctx context.Context, image string, config []byte, args ...string) ([]byte, error) {
	helperName := fmt.Sprintf("v2raya-config-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	defer func() {
		// A canceled Docker CLI can leave its container running. Cleanup must
		// use a fresh context and may only remove this invocation's helper.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", helperName).Run()
	}()
	dockerArgs := []string{"run", "--rm", "--name", helperName, "--network", "none"}
	if config != nil {
		dockerArgs = append(dockerArgs, "-i")
	}
	dockerArgs = append(dockerArgs, "--entrypoint", "xray", image)
	dockerArgs = append(dockerArgs, args...)
	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdin = bytes.NewReader(config)
	// Core diagnostics may include credentials or custom headers. Only the
	// version command needs stdout; never include diagnostics in returned errors.
	var output bytes.Buffer
	if config == nil {
		cmd.Stdout = &output
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output.Bytes(), err
}

func setupDockerProxyRuntime(port int, runtime *dockerProxyRuntime, recreateVolume bool, pauseAfterStart bool) (err error) {
	// Check before removing the current container or changing its volume.
	if err := validateDockerProxyConfig(runtime); err != nil {
		return err
	}
	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	volumeName := fmt.Sprintf("v2raya-socks-%d", port)

	_, _ = runDockerCmd("rm", "-f", containerName)
	if recreateVolume {
		_, _ = runDockerCmd("volume", "rm", "-f", volumeName)
	}

	_, err = runDockerCmd("volume", "create", volumeName)
	if err != nil {
		return fmt.Errorf("failed to create docker volume: %w", err)
	}
	defer func() {
		if err != nil && recreateVolume {
			_, _ = runDockerCmd("volume", "rm", "-f", volumeName)
		}
	}()

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
	if runtime.Core == configure.DockerProxyCoreV2Fly {
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

func CreateDockerProxy(which configure.Which, frontWhich *configure.Which, port int, core configure.DockerProxyCore) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
	if err := validateDockerProxy(port, which, frontWhich); err != nil {
		return err
	}

	proxies, err := configure.GetDockerProxies()
	if err != nil {
		return err
	}
	for _, p := range proxies {
		if p.Port == port {
			return fmt.Errorf("port %d is already used by another docker proxy", port)
		}
	}

	occupied, _, err := ports.IsPortOccupied([]string{strconv.Itoa(port) + ":tcp"})
	if err != nil {
		return err
	}
	if occupied {
		return fmt.Errorf("port %d is already occupied on the host", port)
	}

	runtime, err := buildDockerProxyRuntime(which, frontWhich, core)
	if err != nil {
		return err
	}
	if err = setupDockerProxyRuntime(port, runtime, true, false); err != nil {
		return err
	}

	proxy := configure.DockerProxy{
		Port:            port,
		Which:           which,
		FrontWhich:      frontWhich,
		Core:            runtime.RequestedCore,
		ServerName:      runtime.ServerName,
		FrontServerName: runtime.FrontServerName,
		ContainerName:   fmt.Sprintf("v2raya-socks-%d", port),
		Status:          "running",
	}
	err = configure.SaveDockerProxy(proxy)
	if err != nil {
		return fmt.Errorf("Docker proxy started, but failed to save its references: %w", err)
	}

	return nil
}

func UpdateDockerProxy(port int, which configure.Which, frontWhich *configure.Which, core configure.DockerProxyCore) error {
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
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
	if strings.TrimSpace(string(core)) == "" {
		core = proxy.Core
	}

	containerName := fmt.Sprintf("v2raya-socks-%d", port)
	out, inspectErr := runDockerCmd("inspect", "--format", "{{.State.Paused}}", containerName)
	wasPaused := strings.TrimSpace(out) == "true" || proxy.Status == "paused"
	if inspectErr != nil && proxy.Status == "paused" {
		wasPaused = true
	}

	runtime, err := buildDockerProxyRuntime(which, frontWhich, core)
	if err != nil {
		return err
	}
	if err = setupDockerProxyRuntime(port, runtime, false, wasPaused); err != nil {
		return err
	}

	proxy.Which = which
	proxy.FrontWhich = frontWhich
	proxy.Core = runtime.RequestedCore
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
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
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
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
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
		runtime, err := buildDockerProxyRuntime(proxy.Which, proxy.FrontWhich, proxy.Core)
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
	for i := range proxies {
		if proxies[i].Core == "" {
			proxies[i].Core = configure.DockerProxyCoreAuto
		}
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
	nodeOperationMu.Lock()
	defer nodeOperationMu.Unlock()
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
