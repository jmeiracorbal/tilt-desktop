// Package environment inspects the external tools required by Tilt Desktop.
// It does not install tools, provision clusters, or depend on the desktop UI.
package environment

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Tool string

const (
	Docker Tool = "docker"
	K3d    Tool = "k3d"
	Tilt   Tool = "tilt"
)

var ErrEmptyVersion = errors.New("version command produced no output")
var ErrOutputLimit = errors.New("diagnostic command output exceeded its limit")

// ToolResult reports executable discovery and the outcome of its version
// command. A successful result does not imply that the container daemon
// is reachable or that this tool version is supported.
type ToolResult struct {
	Tool    Tool
	Path    string
	Version string
	Err     error
}

func (r ToolResult) Available() bool {
	return r.Err == nil && r.Path != "" && r.Version != ""
}

// Checker runs read-only probes using the current process environment.
// Construct it with NewChecker.
type Checker struct {
	lookup  func(string) (string, error)
	run     func(context.Context, string, ...string) (string, error)
	timeout time.Duration
}

func NewChecker() *Checker {
	return &Checker{
		lookup: exec.LookPath,
		run: runCommand,
		timeout: 5 * time.Second,
	}
}

// CheckTools checks every required CLI in stable order, recording individual
// errors rather than stopping at the first missing dependency. Each version
// command has its own deadline and also honors the caller's context.
func (c *Checker) CheckTools(ctx context.Context) []ToolResult {
	tools := []struct {
		tool Tool
		arg  string
	}{
		{Docker, "--version"},
		{K3d, "version"},
		{Tilt, "version"},
	}
	results := make([]ToolResult, 0, len(tools))
	for _, tool := range tools {
		results = append(results, c.checkTool(ctx, tool.tool, tool.arg))
	}
	return results
}

func (c *Checker) checkTool(ctx context.Context, tool Tool, arg string) ToolResult {
	result := ToolResult{Tool: tool}
	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}
	path, err := c.lookup(string(tool))
	if err != nil {
		result.Err = fmt.Errorf("locate %s on PATH: %w", tool, err)
		return result
	}
	result.Path = path
	probe, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	output, err := c.run(probe, path, arg)
	if err != nil {
		result.Err = fmt.Errorf("%s version probe: %w", tool, err)
		return result
	}
	result.Version = strings.TrimSpace(output)
	if result.Version == "" {
		result.Err = ErrEmptyVersion
	}
	return result
}

// Both streams are bounded because a broken executable may print indefinitely.
// The command is invoked directly, with no shell or alternate binary.
func runCommand(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	output := &limitedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	value := strings.TrimSpace(string(output.data))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if output.truncated {
		return "", errors.Join(ErrOutputLimit, err)
	}
	if err != nil {
		if value != "" {
			return "", fmt.Errorf("%w: %s", err, value)
		}
		return "", err
	}
	return value, nil
}

const outputLimit = 8192

type limitedOutput struct {
	data      []byte
	truncated bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := outputLimit - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
