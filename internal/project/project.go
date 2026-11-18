// Package project defines application metadata independently of the desktop UI
// and the concrete Kubernetes/Tilt integrations.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClusterMode specifies whether an application owns its own cluster or shares one.
type ClusterMode string

const (
	Dedicated ClusterMode = "dedicated"
	Shared    ClusterMode = "shared"
)

// ClusterAssignment identifies the cluster assigned to a project.
type ClusterAssignment struct {
	Mode ClusterMode
	Name string
}

func (a ClusterAssignment) validate() error {
	if a.Mode != Dedicated && a.Mode != Shared {
		return fmt.Errorf("unsupported cluster mode %q", a.Mode)
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("cluster name is required")
	}
	return nil
}

// Definition identifies a development project and the cluster it uses.
// This is a validated value, not a persisted or running application.
type Definition struct {
	ID        string
	Directory string
	Tiltfile  string
	Cluster   ClusterAssignment
}

// NewDefinition validates a local development project without creating or
// modifying any cluster, context, registry, or Tilt process.
func NewDefinition(id, directory string, cluster ClusterAssignment) (Definition, error) {
	if strings.TrimSpace(id) == "" {
		return Definition{}, fmt.Errorf("project ID is required")
	}
	if strings.TrimSpace(directory) == "" {
		return Definition{}, fmt.Errorf("project directory is required")
	}
	if err := cluster.validate(); err != nil {
		return Definition{}, err
	}

	root, err := filepath.Abs(directory)
	if err != nil {
		return Definition{}, fmt.Errorf("resolve project directory: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return Definition{}, fmt.Errorf("inspect project directory: %w", err)
	}
	if !info.IsDir() {
		return Definition{}, fmt.Errorf("project path %q is not a directory", root)
	}

	tiltfile := filepath.Join(root, "Tiltfile")
	info, err = os.Stat(tiltfile)
	if err != nil {
		return Definition{}, fmt.Errorf("inspect Tiltfile: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Definition{}, fmt.Errorf("Tiltfile %q is not a regular file", tiltfile)
	}

	return Definition{
		ID:        id,
		Directory: root,
		Tiltfile:  tiltfile,
		Cluster:   cluster,
	}, nil
}
