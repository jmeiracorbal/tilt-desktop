package k3d

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecRunner runs k3d directly with an isolated kubeconfig. No shell is used.
type ExecRunner struct {
	Binary         string
	KubeconfigPath string
}

// NewLocalClient isolates its kubeconfig under the user's config directory.
func NewLocalClient() (*Client, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find config directory: %w", err)
	}
	dir := filepath.Join(root, "tilt-desktop")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	return NewClient(ExecRunner{Binary: "k3d", KubeconfigPath: filepath.Join(dir, "kubeconfig")})
}

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	if strings.TrimSpace(r.Binary) == "" {
		return nil, errors.New("k3d executable is required")
	}
	if !filepath.IsAbs(r.KubeconfigPath) {
		return nil, errors.New("absolute private kubeconfig path is required")
	}
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+r.KubeconfigPath)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("k3d %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("run k3d: %w", err)
	}
	return out, nil
}
