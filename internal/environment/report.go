package environment

import "context"

// CheckResult is a transport-neutral diagnostic suitable for native dialogs
// and installer output. Error is a user-facing description, never a fallback.
type CheckResult struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	OSType string `json:"osType,omitempty"`
	Available bool `json:"available"`
	Error string `json:"error,omitempty"`
}

// Report is a point-in-time assessment, not a guarantee of future cluster
// provisioning or version compatibility.
type Report struct {
	Ready bool `json:"ready"`
	Checks []CheckResult `json:"checks"`
}

// Check evaluates all required dependencies and daemon connectivity using
// the same implementation for the desktop and installation preflight.
func (c *Checker) Check(ctx context.Context) Report {
	report := Report{Ready:true, Checks:make([]CheckResult,0,4)}
	for _, tool := range c.CheckTools(ctx) {
		check := CheckResult{
			Name:string(tool.Tool), Path:tool.Path, Version:tool.Version,
			Available:tool.Available(),
		}
		if tool.Err != nil { check.Error = tool.Err.Error() }
		report.Checks = append(report.Checks,check)
		report.Ready = report.Ready && check.Available
	}
	runtime := c.CheckRuntime(ctx)
	check := CheckResult{
		Name:"Docker daemon", Path:runtime.Path, Version:runtime.ServerVersion,
		OSType:runtime.OSType, Available:runtime.Available(),
	}
	if runtime.Err != nil { check.Error = runtime.Err.Error() }
	report.Checks = append(report.Checks,check)
	report.Ready = report.Ready && check.Available
	return report
}
