// Package k3d manages local clusters without shell scripts or global kubeconfig changes.
package k3d

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

const (
	managedLabel = "dev.tilt-desktop.managed-by"
	ownerLabel   = "dev.tilt-desktop.management-id"
	managedValue = "tilt-desktop"
)

var (
	ErrUnmanaged = errors.New("cluster is not managed by Tilt Desktop")
	ErrNotFound  = errors.New("cluster not found")
	validName    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Runner invokes the k3d executable without a shell, returning stdout only.
type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}

// Spec identifies a cluster and its stable management identity. Multiple
// applications may share a Spec without owning the cluster individually.
type Spec struct {
	Name, ManagementID string
}

func (s Spec) validate() error {
	if len(s.Name) > 63 || !validName.MatchString(s.Name) {
		return fmt.Errorf("invalid cluster name %q", s.Name)
	}
	if len(s.ManagementID) > 63 || !validName.MatchString(s.ManagementID) {
		return fmt.Errorf("invalid cluster management ID %q", s.ManagementID)
	}
	return nil
}

// Node contains only the k3d JSON fields needed for lifecycle decisions.
type Node struct {
	Name string `json:"name"`
	Role string `json:"role"`
	State         struct {
		Running bool `json:"Running"`
	} `json:"State"`
}

// Cluster is a read-only view of an existing k3d cluster.
type Cluster struct {
	Name  string `json:"name"`
	Nodes []Node `json:"nodes"`
}

func (c Cluster) allRunning() bool {
	hasServer := false
	for _, n := range c.Nodes {
		if n.Role == "server" {
			hasServer = true
		}
		if !n.State.Running {
			return false
		}
	}
	return hasServer
}

func (c Cluster) anyRunning() bool {
	for _, n := range c.Nodes {
		if n.State.Running {
			return true
		}
	}
	return false
}

// checkOwnership inspects labels on the actual runtime containers. k3d's
// cluster/node listings omit user-supplied runtime labels in their JSON.
func (c Cluster) checkOwnership(ctx context.Context, s Spec, inspector ContainerInspector) error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("%w: cluster %q has no nodes", ErrUnmanaged, s.Name)
	}
	hasServer := false
	for _, n := range c.Nodes {
		if n.Role == "server" {
			hasServer = true
		}
		if !strings.HasPrefix(n.Name, "k3d-"+s.Name+"-") {
			return fmt.Errorf("%w: unexpected node %q", ErrUnmanaged, n.Name)
		}
		labels, err := inspector.Labels(ctx, n.Name)
		if err != nil {
			return fmt.Errorf("inspect container %q: %w", n.Name, err)
		}
		if labels[managedLabel] != managedValue || labels[ownerLabel] != s.ManagementID {
			return fmt.Errorf("%w: %s (node %q)", ErrUnmanaged, s.Name, n.Name)
		}
	}
	if !hasServer {
		return fmt.Errorf("%w: cluster %q has no server", ErrUnmanaged, s.Name)
	}
	return nil
}

// Client serializes lifecycle mutations within one process. The persistent
// layer must coordinate access between processes.
type Client struct {
	runner Runner
	inspector ContainerInspector
	mu sync.Mutex
}

func NewClient(r Runner, inspector ContainerInspector) (*Client, error) {
	if r == nil {
		return nil, errors.New("k3d runner is required")
	}
	if inspector == nil {
		return nil, errors.New("container inspector is required")
	}
	return &Client{runner: r, inspector: inspector}, nil
}

func (c *Client) List(ctx context.Context) ([]Cluster, error) {
	data, err := c.runner.Run(ctx, "cluster", "list", "--output", "json")
	if err != nil {
		return nil, fmt.Errorf("list k3d clusters: %w", err)
	}
	var clusters []Cluster
	if err := json.Unmarshal(data, &clusters); err != nil {
		return nil, fmt.Errorf("decode k3d clusters: %w", err)
	}
	if clusters == nil {
		clusters = []Cluster{}
	}
	return clusters, nil
}

func (c *Client) find(ctx context.Context, name string) (*Cluster, error) {
	all, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Ensure creates, starts, or leaves a managed cluster unchanged. A cluster
// with the same name but different ownership is never adopted.
func (c *Client) Ensure(ctx context.Context, s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current, err := c.find(ctx, s.Name)
	if err != nil {
		return err
	}
	if current != nil {
		if err := current.checkOwnership(ctx, s, c.inspector); err != nil {
			return err
		}
		if current.allRunning() {
			return nil
		}
		if _, err := c.runner.Run(ctx, "cluster", "start", s.Name, "--wait", "--timeout", "120s"); err != nil {
			return fmt.Errorf("start cluster %q: %w", s.Name, err)
		}
	} else {
		_, err = c.runner.Run(ctx, "cluster", "create", s.Name,
			"--wait", "--timeout", "120s",
			"--kubeconfig-update-default=false", "--kubeconfig-switch-context=false",
			"--runtime-label", managedLabel+"="+managedValue+"@all",
			"--runtime-label", ownerLabel+"="+s.ManagementID+"@all",
			"--registry-create", s.Name+"-registry:127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("create cluster %q: %w", s.Name, err)
		}
	}
	actual, err := c.find(ctx, s.Name)
	if err != nil {
		return err
	}
	if actual == nil {
		return fmt.Errorf("cluster %q absent after ensure", s.Name)
	}
	if err := actual.checkOwnership(ctx, s, c.inspector); err != nil {
		return err
	}
	if !actual.allRunning() {
		return fmt.Errorf("cluster %q not running after ensure", s.Name)
	}
	return nil
}

// Stop stops only the intended managed cluster and is idempotent for a
// cluster that is already fully stopped.
func (c *Client) Stop(ctx context.Context, s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current, err := c.find(ctx, s.Name)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("%w: %s", ErrNotFound, s.Name)
	}
	if err := current.checkOwnership(ctx, s, c.inspector); err != nil {
		return err
	}
	if !current.anyRunning() {
		return nil
	}
	if _, err := c.runner.Run(ctx, "cluster", "stop", s.Name); err != nil {
		return fmt.Errorf("stop cluster %q: %w", s.Name, err)
	}
	actual, err := c.find(ctx, s.Name)
	if err != nil {
		return err
	}
	if actual == nil {
		return fmt.Errorf("cluster %q absent after stop", s.Name)
	}
	if err := actual.checkOwnership(ctx, s, c.inspector); err != nil {
		return err
	}
	if actual.anyRunning() {
		return fmt.Errorf("cluster %q still running after stop", s.Name)
	}
	return nil
}

// Delete permanently removes a managed cluster. The UI must collect explicit
// user confirmation before invoking it. The CLI runner isolates KUBECONFIG.
func (c *Client) Delete(ctx context.Context, s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current, err := c.find(ctx, s.Name)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if err := current.checkOwnership(ctx, s, c.inspector); err != nil {
		return err
	}
	if _, err := c.runner.Run(ctx, "cluster", "delete", s.Name); err != nil {
		return fmt.Errorf("delete cluster %q: %w", s.Name, err)
	}
	actual, err := c.find(ctx, s.Name)
	if err != nil {
		return err
	}
	if actual != nil {
		return fmt.Errorf("cluster %q still exists after delete", s.Name)
	}
	return nil
}

// Kubeconfig fetches cluster credentials without writing global configuration.
// The caller is responsible for protecting the returned credentials.
func (c *Client) Kubeconfig(ctx context.Context, s Spec) ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	current, err := c.find(ctx, s.Name)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, s.Name)
	}
	if err := current.checkOwnership(ctx, s, c.inspector); err != nil {
		return nil, err
	}
	data, err := c.runner.Run(ctx, "kubeconfig", "get", s.Name)
	if err != nil {
		return nil, fmt.Errorf("get kubeconfig for %q: %w", s.Name, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty kubeconfig for %q", s.Name)
	}
	return data, nil
}
