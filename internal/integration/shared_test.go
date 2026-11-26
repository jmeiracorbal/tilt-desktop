//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmeiracorbal/tilt-desktop/internal/application"
	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
	"github.com/jmeiracorbal/tilt-desktop/internal/project"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

func TestSharedClusterLifecycle(t *testing.T) {
	for _, binary := range []string{"docker", "k3d", "tilt", "kubectl"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatalf("%s required: %v", binary, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	global := filepath.Join(root, "global-kubeconfig")
	const marker = "global context must remain unchanged\n"
	if err := os.WriteFile(global, []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", global)
	runner := k3d.ExecRunner{Binary: "k3d", KubeconfigPath: filepath.Join(root, "runner-kubeconfig")}
	client, err := k3d.NewClient(runner, k3d.DockerInspector{Binary: "docker"})
	if err != nil { t.Fatal(err) }
	supervisor, err := tilt.NewManager(tilt.ExecLauncher{Binary: "tilt"})
	if err != nil { t.Fatal(err) }
	catalog := filepath.Join(root, "catalog.json")
	manager, err := application.NewPersistentManager(client, supervisor, application.NetPorts{}, client, supervisor, catalog)
	if err != nil { t.Fatal(err) }
	defer manager.Close()

	// Unique names avoid colliding with any independently managed environment.
	name := fmt.Sprintf("td-shared-ci-%d", time.Now().UnixNano())
	var ids []string
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 90*time.Second)
		defer done()
		for _, id := range ids {
			state, err := supervisor.Status(id)
			if err == nil && state.Running {
				if err := supervisor.Stop(clean, id); err != nil { t.Errorf("cleanup Tilt %s: %v", id, err) }
			}
		}
		all, err := client.List(clean)
		if err != nil { t.Errorf("cleanup inventory: %v", err); return }
		for _, cluster := range all {
			if cluster.Name != name { continue }
			port, err := client.RegistryPort(clean, name)
			if err != nil { t.Errorf("cleanup registry: %v", err); return }
			if err := client.Delete(clean, k3d.Spec{Name:name, ManagementID:name, RegistryPort:port}); err != nil {
				t.Errorf("cleanup owned cluster: %v", err)
			}
		}
	})
	for _, label := range []string{"a", "b"} {
		dir := filepath.Join(root, "project-"+label)
		if err := os.Mkdir(dir, 0700); err != nil { t.Fatal(err) }
		manifest := fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: td-shared-%s\ndata:\n  value: %s\n", label, label)
		if err := os.WriteFile(filepath.Join(dir,"configmap.yaml"), []byte(manifest), 0600); err != nil { t.Fatal(err) }
		if err := os.WriteFile(filepath.Join(dir,"Tiltfile"), []byte("k8s_yaml('configmap.yaml')\n"), 0600); err != nil { t.Fatal(err) }
		def, err := manager.RegisterShared(dir, name)
		if err != nil { t.Fatal(err) }
		ids = append(ids, def.ID)
		if def.Cluster.Mode != project.Shared || def.Cluster.Name != name { t.Fatalf("assignment: %+v",def.Cluster) }
	}
	for _, id := range ids {
		if err := manager.Start(ctx,id); err != nil { t.Fatalf("start %s: %v",id,err) }
	}
	a, err := manager.Status(ids[0])
	if err != nil { t.Fatal(err) }
	b, err := manager.Status(ids[1])
	if err != nil { t.Fatal(err) }
	if !a.Running || !b.Running || a.ClusterName != name || b.ClusterName != name ||
		a.RegistryPort == 0 || a.RegistryPort != b.RegistryPort || a.TiltPort == 0 ||
		b.TiltPort == 0 || a.TiltPort == b.TiltPort {
		t.Fatalf("unexpected shared state: %+v %+v",a,b)
	}
	creds, err := client.Kubeconfig(ctx,k3d.Spec{Name:name,ManagementID:name,RegistryPort:a.RegistryPort})
	if err != nil { t.Fatal(err) }
	config := filepath.Join(root,"external-kubeconfig")
	if err := os.WriteFile(config,creds,0600); err != nil { t.Fatal(err) }
	for _, label := range []string{"a","b"} {
		if err := await(ctx,readyTimeout,func(ctx context.Context) error {
			_, err := kubectl(ctx,config,name,"get","configmap","td-shared-"+label)
			return err
		}); err != nil { t.Fatalf("project %s not deployed: %v",label,err) }
	}
	if err := manager.StopSharedCluster(ctx,name); !errors.Is(err,application.ErrClusterInUse) { t.Fatalf("active stop: %v",err) }
	if err := manager.DeleteSharedCluster(ctx,name,true); !errors.Is(err,application.ErrClusterInUse) { t.Fatalf("assigned deletion: %v",err) }
	if err := manager.Stop(ctx,ids[0]); err != nil { t.Fatal(err) }
	if err := manager.Delete(ctx,ids[0],true); err != nil { t.Fatal(err) }
	b, err = manager.Status(ids[1])
	if err != nil || !b.Running { t.Fatalf("second session interrupted: %+v %v",b,err) }
	if _, err := manager.Resources(ctx,ids[1]); err != nil { t.Fatalf("second Tilt API interrupted: %v",err) }
	if _, err := kubectl(ctx,config,name,"get","configmap","td-shared-b"); err != nil { t.Fatalf("shared Kubernetes interrupted: %v",err) }
	if err := manager.Stop(ctx,ids[1]); err != nil { t.Fatal(err) }
	if err := manager.StopSharedCluster(ctx,name); err != nil { t.Fatal(err) }
	if err := manager.Close(); err != nil { t.Fatal(err) }

	reopened, err := application.NewPersistentManager(client,supervisor,application.NetPorts{},client,supervisor,catalog)
	if err != nil { t.Fatal(err) }
	defer reopened.Close()
	restored, err := reopened.Status(ids[1])
	if err != nil || restored.ClusterMode != project.Shared || restored.RegistryPort != b.RegistryPort || restored.TiltPort != b.TiltPort {
		t.Fatalf("shared assignment not restored: %+v %v",restored,err)
	}
	if err := reopened.Start(ctx,ids[1]); err != nil { t.Fatalf("restart member: %v",err) }
	if err := await(ctx,readyTimeout,func(ctx context.Context) error {
		_, err := kubectl(ctx,config,name,"get","configmap","td-shared-b")
		return err
	}); err != nil { t.Fatalf("shared workload unavailable after restart: %v",err) }
	if err := reopened.Delete(ctx,ids[1],true); err != nil { t.Fatal(err) }
	clusters, err := reopened.ListSharedClusters()
	if err != nil || len(clusters)!=1 || clusters[0].Projects!=0 { t.Fatalf("shared cluster not retained: %+v %v",clusters,err) }
	if err := reopened.DeleteSharedCluster(ctx,name,false); !errors.Is(err,application.ErrConfirmationRequired) { t.Fatalf("missing confirmation: %v",err) }
	if err := reopened.DeleteSharedCluster(ctx,name,true); err != nil { t.Fatal(err) }
	all,err := client.List(ctx)
	if err != nil { t.Fatal(err) }
	for _,cluster := range all { if cluster.Name==name { t.Fatal("shared cluster still exists") } }
	if err := verifyGlobal(global,marker); err != nil { t.Fatal(err) }
}
