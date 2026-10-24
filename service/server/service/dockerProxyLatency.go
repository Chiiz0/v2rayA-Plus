package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/v2rayA/v2rayA/db/configure"
)

const dockerProxyHTTPTestImage = "curlimages/curl:latest"

type DockerProxyLatency struct {
	Port    int    `json:"port"`
	Latency string `json:"pingLatency"`
}

func selectDockerProxiesByPorts(proxies []configure.DockerProxy, ports []int) ([]configure.DockerProxy, error) {
	if len(ports) == 0 || len(ports) > 32 {
		return nil, fmt.Errorf("select between 1 and 32 Docker proxies to test")
	}
	byPort := make(map[int]configure.DockerProxy, len(proxies))
	for _, proxy := range proxies {
		byPort[proxy.Port] = proxy
	}
	selected := make([]configure.DockerProxy, 0, len(ports))
	seen := make(map[int]bool, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid Docker proxy port %d", port)
		}
		proxy, ok := byPort[port]
		if !ok {
			return nil, fmt.Errorf("Docker proxy on port %d does not exist", port)
		}
		if !seen[port] {
			selected = append(selected, proxy)
			seen[port] = true
		}
	}
	return selected, nil
}

// TestDockerProxyHttpLatency tests the running proxies, without regenerating
// node configurations or starting/reconfiguring the main service's core.
func TestDockerProxyHttpLatency(ctx context.Context, ports []int, timeout time.Duration) ([]DockerProxyLatency, error) {
	if len(ports) == 0 || len(ports) > 32 {
		return nil, fmt.Errorf("select between 1 and 32 Docker proxies to test")
	}
	proxies, err := configure.GetDockerProxies()
	if err != nil {
		return nil, err
	}
	selected, err := selectDockerProxiesByPorts(proxies, ports)
	if err != nil {
		return nil, err
	}
	results := make([]DockerProxyLatency, len(selected))
	limit := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, proxy := range selected {
		wg.Add(1)
		go func(i int, proxy configure.DockerProxy) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				results[i] = DockerProxyLatency{Port: proxy.Port, Latency: "CANCELED"}
				return
			}
			results[i] = testDockerProxyHttpLatency(ctx, proxy, timeout, HttpTestURL)
		}(i, proxy)
	}
	wg.Wait()
	return results, nil
}

func testDockerProxyHttpLatency(parent context.Context, proxy configure.DockerProxy, timeout time.Duration, testURL string) DockerProxyLatency {
	result := DockerProxyLatency{Port: proxy.Port}
	// Allow time for Docker startup/image retrieval in addition to curl's limit.
	ctx, cancel := context.WithTimeout(parent, timeout+30*time.Second)
	defer cancel()
	containerName := fmt.Sprintf("v2raya-socks-%d", proxy.Port)
	state, err := exec.CommandContext(ctx, "docker", "inspect", "--type", "container",
		"--format", "{{.Id}} {{.State.Running}} {{.State.Paused}}", containerName).Output()
	if err != nil {
		result.Latency = "CONTAINER UNAVAILABLE"
		if ctx.Err() != nil {
			result.Latency = "TIMEOUT"
		}
		return result
	}
	fields := strings.Fields(string(state))
	if len(fields) != 3 || (fields[1] != "true" && fields[1] != "false") || (fields[2] != "true" && fields[2] != "false") {
		result.Latency = "INVALID CONTAINER STATE"
		return result
	}
	if fields[2] == "true" {
		result.Latency = "PAUSED"
		return result
	}
	if fields[1] != "true" {
		result.Latency = "STOPPED"
		return result
	}

	helperName := fmt.Sprintf("v2raya-http-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	defer func() {
		// Killing the Docker CLI alone does not stop a container. Use a fresh
		// context so cancellation of the HTTP request cannot skip cleanup.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", helperName).Run()
	}()
	seconds := strconv.FormatFloat(timeout.Seconds(), 'f', 3, 64)
	output, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", helperName,
		"--network", "container:"+fields[0], dockerProxyHTTPTestImage,
		"--silent", "--show-error", "--output", "/dev/null",
		"--write-out", "%{http_code} %{time_total}",
		"--max-time", seconds, "--connect-timeout", seconds,
		"--socks5-hostname", "127.0.0.1:1080", "--noproxy", "",
		"--proto", "=http,https", "--url", testURL).Output()
	if err != nil {
		switch {
		case ctx.Err() != nil:
			result.Latency = "TIMEOUT"
		case dockerHTTPExitCode(err) == 28:
			result.Latency = "TIMEOUT"
		case dockerHTTPExitCode(err) == 125 || dockerHTTPExitCode(err) == 126 || dockerHTTPExitCode(err) == 127:
			result.Latency = "TEST ERROR: check Docker and the curl test image"
		default:
			result.Latency = "HTTP TEST FAILED"
		}
		return result
	}
	fields = strings.Fields(string(output))
	if len(fields) != 2 {
		result.Latency = "INVALID TEST RESULT"
		return result
	}
	status, statusErr := strconv.Atoi(fields[0])
	elapsed, timeErr := time.ParseDuration(fields[1] + "s")
	if statusErr != nil || timeErr != nil || elapsed < 0 {
		result.Latency = "INVALID TEST RESULT"
	} else if status < 200 || status >= 400 {
		result.Latency = "BAD RESPONSE"
	} else {
		// Curl's time excludes image pulls and helper-container startup.
		result.Latency = fmt.Sprintf("%.0fms", elapsed.Seconds()*1000)
	}
	return result
}

func dockerHTTPExitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
