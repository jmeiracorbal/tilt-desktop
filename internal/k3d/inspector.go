package k3d

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ContainerInspector reads labels from actual container runtime objects.
type ContainerInspector interface {
	Labels(context.Context, string) (map[string]string, error)
}

// DockerInspector uses the Docker CLI already required by a Docker-backed k3d
// installation. It never invokes a shell or mutates containers.
type DockerInspector struct {
	Binary string
}

func (d DockerInspector) Labels(ctx context.Context, containerName string) (map[string]string, error) {
	if strings.TrimSpace(d.Binary) == "" {
		return nil, errors.New("Docker executable is required")
	}
	if containerName == "" {
		return nil, errors.New("container name is required")
	}
	cmd := exec.CommandContext(ctx, d.Binary, "container", "inspect", "--format", "{{json .Config.Labels}}", containerName)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("inspect Docker container %q: %w: %s", containerName, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("inspect Docker container %q: %w", containerName, err)
	}
	var labels map[string]string
	if err := json.Unmarshal(output, &labels); err != nil {
		return nil, fmt.Errorf("decode Docker labels for %q: %w", containerName, err)
	}
	if labels == nil {
		return nil, fmt.Errorf("container %q has no Docker labels", containerName)
	}
	return labels, nil
}
