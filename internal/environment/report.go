package environment

import (
	"context"
	"errors"
	"fmt"
)

// Requirement identifies the external dependencies of an operation.
type Requirement uint8

const (
	StartProject Requirement = iota + 1
	ManageCluster
	ObserveTilt
)

var ErrRequirements = errors.New("environment requirements are not satisfied")

// CheckResult is a transport-neutral diagnostic suitable for native dialogs
// and installer output.
type CheckResult struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	OSType    string `json:"osType,omitempty"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// Report is a point-in-time assessment, not a guarantee of future cluster
// provisioning or version compatibility.
type Report struct {
	Ready  bool          `json:"ready"`
	Checks []CheckResult `json:"checks"`
}

// Check evaluates all required dependencies and daemon connectivity using
// the same implementation for the desktop and installation preflight.
func (c *Checker) Check(ctx context.Context) Report {
	report, _ := c.check(ctx, StartProject)
	return report
}

// Require evaluates only the tools needed for the operation and fails
// explicitly before the caller mutates the environment. It never caches
// a successful check or assumes that a previous result is still valid.
func (c *Checker) Require(ctx context.Context, requirement Requirement) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	report, err := c.check(ctx, requirement)
	if err != nil {
		return err
	}
	if report.Ready {
		return ctx.Err()
	}
	failures := []error{ErrRequirements}
	for _, check := range report.Checks {
		if !check.Available {
			failures = append(failures, fmt.Errorf("%s: %s", check.Name, check.Error))
		}
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (c *Checker) check(ctx context.Context, requirement Requirement) (Report, error) {
	var tools []Tool
	var daemon bool
	switch requirement {
	case StartProject:
		tools, daemon = []Tool{Docker, K3d, Tilt}, true
	case ManageCluster:
		tools, daemon = []Tool{Docker, K3d}, true
	case ObserveTilt:
		tools = []Tool{Tilt}
	default:
		return Report{}, fmt.Errorf("%w: unknown requirement %d", ErrRequirements, requirement)
	}
	report := Report{Ready: true, Checks: make([]CheckResult, 0, len(tools)+1)}
	for _, tool := range c.checkTools(ctx, tools) {
		check := CheckResult{
			Name: string(tool.Tool), Path: tool.Path, Version: tool.Version,
			Available: tool.Available(),
		}
		if tool.Err != nil {
			check.Error = tool.Err.Error()
		}
		report.Checks = append(report.Checks, check)
		report.Ready = report.Ready && check.Available
	}
	if daemon {
		runtime := c.CheckRuntime(ctx)
		check := CheckResult{
			Name: "Docker daemon", Path: runtime.Path, Version: runtime.ServerVersion,
			OSType: runtime.OSType, Available: runtime.Available(),
		}
		if runtime.Err != nil {
			check.Error = runtime.Err.Error()
		}
		report.Checks = append(report.Checks, check)
		report.Ready = report.Ready && check.Available
	}
	return report, nil
}
