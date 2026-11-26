package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
)

// clusterManager owns cluster discovery and lifecycle independently of project
// and Tilt session identity. Its identity is the managed cluster name; existing
// dedicated clusters retain their original runtime ownership labels.
type clusterManager struct {
	runtime    Clusters
	registries Registries
	ports      Ports
}

func (m *clusterManager) discover(ctx context.Context, name string) (k3d.Spec, bool, error) {
	all, err := m.runtime.List(ctx)
	if err != nil {
		return k3d.Spec{}, false, fmt.Errorf("discover k3d clusters: %w", err)
	}
	for _, c := range all {
		if c.Name != name {
			continue
		}
		port, err := m.registries.RegistryPort(ctx, name)
		if err != nil {
			return k3d.Spec{}, true, fmt.Errorf("discover registry port for %q: %w", name, err)
		}
		if port == 0 {
			return k3d.Spec{}, true, fmt.Errorf("registry for %q has no published port", name)
		}
		return k3d.Spec{Name: name, ManagementID: name, RegistryPort: port}, true, nil
	}
	return k3d.Spec{}, false, nil
}

// prepare resolves the cluster's own identity and registry, validates the
// assignment before changing runtime state, and returns its private credentials.
// Only resources newly created by this attempt may be rolled back.
func (m *clusterManager) prepare(ctx context.Context, name string, reserved uint16, validate func(k3d.Spec,bool) error) (k3d.Spec, bool, []byte, error) {
	spec, existed, err := m.discover(ctx, name)
	if err != nil {
		return k3d.Spec{}, existed, nil, err
	}
	if existed && reserved!=0 && spec.RegistryPort!=reserved {
		return k3d.Spec{}, true, nil, fmt.Errorf("cluster %q registry port differs from catalog: %d != %d", name,spec.RegistryPort,reserved)
	}
	if !existed {
		port:=reserved
		if port==0{
			var err error
			port,err=m.ports.Available()
			if err!=nil{return k3d.Spec{},false,nil,fmt.Errorf("allocate registry port: %w",err)}
		}
		if port == 0 {
			return k3d.Spec{}, false, nil, errors.New("port allocator returned zero registry port")
		}
		spec = k3d.Spec{Name: name, ManagementID: name, RegistryPort: port}
	}
	if err := validate(spec,existed); err != nil {
		return k3d.Spec{}, existed, nil, err
	}
	if err := m.runtime.Ensure(ctx, spec); err != nil {
		return k3d.Spec{}, existed, nil, m.rollback(spec, existed, fmt.Errorf("prepare cluster: %w", err))
	}
	credentials, err := m.runtime.Kubeconfig(ctx, spec)
	if err != nil {
		return k3d.Spec{}, existed, nil, m.rollback(spec, existed, fmt.Errorf("read private kubeconfig: %w", err))
	}
	if len(credentials) == 0 {
		return k3d.Spec{}, existed, nil, m.rollback(spec, existed, errors.New("empty private kubeconfig"))
	}
	return spec, existed, credentials, nil
}

// rollback uses an independent bounded context so canceled starts cannot
// abandon a cluster created by this attempt.
func (m *clusterManager) rollback(spec k3d.Spec, existed bool, cause error) error {
	if existed {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := m.runtime.Delete(ctx, spec); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback newly created cluster: %w", err))
	}
	return cause
}

func (m *clusterManager) stop(ctx context.Context, spec k3d.Spec) error {
	if err := m.runtime.Stop(ctx, spec); err != nil {
		return fmt.Errorf("stop cluster: %w", err)
	}
	return nil
}

// stopExisting and deleteExisting operate by cluster identity, without
// requiring a live Tilt process or a registered project entry.
func (m *clusterManager) stopExisting(ctx context.Context, name string) error {
	spec, found, err := m.discover(ctx, name)
	if err != nil || !found {
		return err
	}
	return m.stop(ctx, spec)
}

func (m *clusterManager) deleteExisting(ctx context.Context, name string) error {
	spec, found, err := m.discover(ctx, name)
	if err != nil || !found {
		return err
	}
	if err := m.runtime.Delete(ctx, spec); err != nil {
		return fmt.Errorf("delete cluster: %w", err)
	}
	return nil
}
