package environment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidRuntimeInfo = errors.New("Docker daemon returned incomplete runtime information")
var ErrUnsupportedRuntimeOS = errors.New("k3d requires a Linux container runtime")

// RuntimeResult reports daemon connectivity independently of CLI availability.
// Reachable does not guarantee that k3d can create clusters or that the Docker
// daemon is local; the probe honors the existing Docker CLI configuration.
type RuntimeResult struct {
	Path          string
	ServerVersion string
	OSType        string
	Reachable     bool
	Err           error
}

func (r RuntimeResult) Available() bool {
	return r.Reachable && r.Err == nil && r.OSType == "linux" && r.ServerVersion != ""
}

const runtimeInfoFormat = `{"serverVersion":{{json .ServerVersion}},"osType":{{json .OSType}}}`

// CheckRuntime queries the daemon using the Docker CLI without changing
// contexts, environment variables, permissions, or daemon configuration.
func (c *Checker) CheckRuntime(ctx context.Context) RuntimeResult {
	var result RuntimeResult
	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}
	path, err := c.lookup(string(Docker))
	if err != nil {
		result.Err = fmt.Errorf("locate Docker CLI on PATH: %w", err)
		return result
	}
	result.Path = path
	probe, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	output, err := c.run(probe, path, "info", "--format", runtimeInfoFormat)
	if err != nil {
		result.Err = fmt.Errorf("query Docker daemon: %w", err)
		return result
	}
	var info struct {
		ServerVersion string `json:"serverVersion"`
		OSType        string `json:"osType"`
	}
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		result.Err = fmt.Errorf("decode Docker runtime information: %w", err)
		return result
	}
	result.ServerVersion = strings.TrimSpace(info.ServerVersion)
	result.OSType = strings.TrimSpace(info.OSType)
	if result.ServerVersion == "" || result.OSType == "" {
		result.Err = ErrInvalidRuntimeInfo
		return result
	}
	result.Reachable = true
	if result.OSType != "linux" {
		result.Err = fmt.Errorf("%w: daemon reports %q", ErrUnsupportedRuntimeOS, result.OSType)
	}
	return result
}
