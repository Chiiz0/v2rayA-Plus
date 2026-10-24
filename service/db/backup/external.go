package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type DockerCheck struct {
	Port            int      `json:"port"`
	Container       string   `json:"container"`
	Volume          string   `json:"volume"`
	ContainerDetail string   `json:"containerDetail,omitempty"`
	VolumeDetail    string   `json:"volumeDetail,omitempty"`
	Differences     []string `json:"differences,omitempty"`
}

type ExternalReport struct {
	Scope  string        `json:"scope"`
	Checks []DockerCheck `json:"checks"`
}

func (r ExternalReport) HasErrors() bool {
	for _, check := range r.Checks {
		if check.Container != "present" || check.Volume != "present" || len(check.Differences) != 0 {
			return true
		}
	}
	return false
}

// InspectDocker is an optional metadata-only check. It neither starts nor
// executes containers and never reads volume contents or protocol settings.
func InspectDocker(ctx context.Context, refs []DockerReference) ExternalReport {
	report := ExternalReport{Scope: "metadata-only; protocol configuration and volume contents not checked", Checks: []DockerCheck{}}
	for _, ref := range refs {
		check := DockerCheck{Port: ref.Port, Container: "unavailable", Volume: "unavailable"}
		name := "v2raya-socks-" + strconv.Itoa(ref.Port)
		if ref.Port < 1 || ref.Port > 65535 || ref.ContainerName != "" && ref.ContainerName != name || ref.VolumeName != "" && ref.VolumeName != name {
			check.Differences = []string{"invalid stored Docker proxy identity; inspection skipped"}
			report.Checks = append(report.Checks, check)
			continue
		}
		container, state, detail := inspectDockerObject(ctx, "container", name)
		check.Container, check.ContainerDetail = state, detail
		if state == "present" {
			var metadata dockerContainerMetadata
			if err := json.Unmarshal(container, &metadata); err != nil || metadata.Name != "/"+name && metadata.Name != name {
				check.Container, check.ContainerDetail = "unavailable", "invalid container inspection response"
			} else {
				check.Differences = inspectDockerMetadata(ref, name, metadata)
			}
		}
		volume, state, detail := inspectDockerObject(ctx, "volume", name)
		check.Volume, check.VolumeDetail = state, detail
		if state == "present" {
			var metadata struct{ Name string }
			if err := json.Unmarshal(volume, &metadata); err != nil || metadata.Name != name {
				check.Volume, check.VolumeDetail = "unavailable", "invalid volume inspection response"
			}
		}
		report.Checks = append(report.Checks, check)
	}
	return report
}

var errInspectOutputLimit = errors.New("Docker inspection output exceeds the limit")

type inspectOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *inspectOutput) Write(data []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if len(data) > remaining {
		n, _ := w.buffer.Write(data[:remaining])
		w.overflow = true
		w.cancel()
		return n, errInspectOutputLimit
	}
	return w.buffer.Write(data)
}

func inspectDockerObject(parent context.Context, kind, name string) (json.RawMessage, string, string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	stdout := &inspectOutput{limit: 1024 * 1024, cancel: cancel}
	stderr := &inspectOutput{limit: 64 * 1024, cancel: cancel}
	cmd := exec.CommandContext(ctx, "docker", kind, "inspect", "--", name)
	cmd.WaitDelay = time.Second
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, "unavailable", "Docker inspection output exceeded the limit"
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "unavailable", "Docker inspection timed out"
		}
		return nil, "unavailable", "Docker inspection canceled"
	}
	if err != nil {
		// An unreachable daemon is not evidence that a container/volume is
		// missing. Only explicit missing-object diagnostics establish that.
		diagnostic := strings.ToLower(stderr.buffer.String())
		if strings.Contains(diagnostic, "no such "+kind+": "+name) || kind == "volume" && strings.Contains(diagnostic, "get "+name+": no such volume") {
			return nil, "missing", ""
		}
		return nil, "unavailable", "Docker inspection failed; check CLI and daemon access"
	}
	var objects []json.RawMessage
	if err := json.Unmarshal(stdout.buffer.Bytes(), &objects); err != nil || objects == nil || len(objects) > 1 {
		return nil, "unavailable", "invalid Docker inspection response"
	}
	if len(objects) == 0 {
		return nil, "missing", ""
	}
	return objects[0], "present", ""
}

type dockerPortBinding struct {
	HostPort string
}

type dockerContainerMetadata struct {
	Name   string
	Config struct{ Image string }
	Mounts []struct {
		Type, Name, Destination string
	}
	HostConfig struct {
		PortBindings map[string][]dockerPortBinding
	}
	NetworkSettings struct {
		Ports map[string][]dockerPortBinding
	}
}

func inspectDockerMetadata(ref DockerReference, name string, metadata dockerContainerMetadata) []string {
	var differences []string
	image := metadata.Config.Image
	if at := strings.IndexByte(image, '@'); at >= 0 {
		image = image[:at]
	}
	if colon := strings.LastIndexByte(image, ':'); colon > strings.LastIndexByte(image, '/') {
		image = image[:colon]
	}
	image = strings.TrimPrefix(strings.TrimPrefix(image, "docker.io/"), "index.docker.io/")
	actualCore := ""
	switch image {
	case "teddysun/xray":
		actualCore = "xray"
	case "v2fly/v2fly-core":
		actualCore = "v2fly"
	}
	core := strings.ToLower(strings.TrimSpace(ref.Core))
	switch core {
	case "xray", "v2fly":
		if actualCore != core {
			differences = append(differences, "container image differs from the saved core's expected repository")
		}
	case "", "auto":
		core = actualCore
		if core == "" {
			differences = append(differences, "container image is unrecognized; auto core cannot be verified")
		}
	default:
		differences = append(differences, "saved core selection is unsupported")
	}
	mountPath := ""
	if core == "xray" {
		mountPath = "/etc/xray"
	} else if core == "v2fly" {
		mountPath = "/etc/v2ray"
	}
	foundMount := false
	for _, mount := range metadata.Mounts {
		if mount.Type == "volume" && mount.Name == name && mountPath != "" && mount.Destination == mountPath {
			foundMount = true
		}
	}
	if !foundMount {
		differences = append(differences, "expected proxy volume is not mounted at the core's configuration directory")
	}
	bindings := metadata.HostConfig.PortBindings["1080/tcp"]
	if len(bindings) == 0 {
		bindings = metadata.NetworkSettings.Ports["1080/tcp"]
	}
	matchingPorts := len(bindings) != 0
	for _, binding := range bindings {
		if binding.HostPort != strconv.Itoa(ref.Port) {
			matchingPorts = false
		}
	}
	if !matchingPorts {
		differences = append(differences, "1080/tcp host port mapping differs from the saved proxy port")
	}
	return differences
}
