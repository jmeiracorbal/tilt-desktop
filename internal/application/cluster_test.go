package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
)

func TestClusterLifecycleIndependentOfProject(t *testing.T) {
	ctx := context.Background()
	events := &trace{}
	runtime := &fakeCluster{trace: events}
	ports := &fakePorts{values: []uint16{5111}}
	registries := &fakeRegistries{port: 5111}
	manager := &clusterManager{runtime: runtime, registries: registries, ports: ports}
	const name = "td-shared-development"
	validate := func(spec k3d.Spec) error {
		if spec.Name != name || spec.ManagementID != name || spec.RegistryPort != 5111 {
			t.Fatalf("unexpected cluster identity: %+v", spec)
		}
		return nil
	}
	spec, existed, credentials, err := manager.prepare(ctx, name, validate)
	if err != nil || existed || len(credentials) == 0 {
		t.Fatalf("prepare new cluster: %+v %t %v", spec, existed, err)
	}
	reused, existed, _, err := manager.prepare(ctx, name, validate)
	if err != nil || !existed || reused != spec || ports.index != 1 {
		t.Fatalf("reuse cluster: %+v %t %v", reused, existed, err)
	}
	if err := manager.stopExisting(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := manager.deleteExisting(ctx, name); err != nil {
		t.Fatal(err)
	}
	if runtime.exists {
		t.Fatal("cluster still exists")
	}
	want := []string{
		"cluster.list", "cluster.ensure", "cluster.kubeconfig",
		"cluster.list", "cluster.ensure", "cluster.kubeconfig",
		"cluster.list", "cluster.stop", "cluster.list", "cluster.delete",
	}
	if got := events.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle order: got %v, want %v", got, want)
	}
}

func TestClusterAssignmentRejectedBeforeRuntimeMutation(t *testing.T) {
	events := &trace{}
	runtime := &fakeCluster{trace: events}
	manager := &clusterManager{
		runtime: runtime, registries: &fakeRegistries{},
		ports: &fakePorts{values: []uint16{5111}},
	}
	conflict := errors.New("assignment conflicts with existing cluster")
	_, _, _, err := manager.prepare(context.Background(), "development", func(k3d.Spec) error {
		return conflict
	})
	if !errors.Is(err, conflict) {
		t.Fatalf("expected assignment conflict, got %v", err)
	}
	if got := events.all(); !reflect.DeepEqual(got, []string{"cluster.list"}) {
		t.Fatalf("runtime changed after rejection: %v", got)
	}
}
