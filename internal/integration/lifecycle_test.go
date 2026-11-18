//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

const (
	integrationTimeout = 12 * time.Minute
	readyTimeout       = 3 * time.Minute
)

func TestK3dAndTiltLifecycle(t *testing.T) {
	for _, executable := range []string{"docker", "k3d", "kubectl", "tilt"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Fatalf("integration requires %s: %v", executable, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// The test is intentionally isolated from the developer's default kubeconfig.
	root := t.TempDir()
	global := filepath.Join(root, "global.yaml")
	const marker = "do not modify global kubeconfig\n"
	if err := os.WriteFile(global, []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", global)
	private := filepath.Join(root, "k3d-kubeconfig")
	client, err := k3d.NewClient(k3d.ExecRunner{Binary: "k3d", KubeconfigPath: private}, k3d.DockerInspector{Binary: "docker"})
	if err != nil {
		t.Fatal(err)
	}

	var entropy [5]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	name := "tdi-" + hex.EncodeToString(entropy[:])
	spec := k3d.Spec{Name: name, ManagementID: name}
	// Cleanup is independent of the production ownership check: the test
	// generated this unique name and must never leak its ephemeral cluster.
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 90*time.Second)
		defer done()
		clusters, err := client.List(cleanupCtx)
		if err != nil {
			t.Errorf("list clusters for cleanup: %v", err)
			return
		}
		for _, cluster := range clusters {
			if cluster.Name == name {
				if _, err := (k3d.ExecRunner{Binary: "k3d", KubeconfigPath: private}).Run(cleanupCtx, "cluster", "delete", name); err != nil {
					t.Errorf("clean up test-owned cluster %q: %v", name, err)
				}
				return
			}
		}
	})

	t.Logf("ensuring isolated k3d cluster %s", name)
	if err := client.Ensure(ctx, spec); err != nil {
		t.Fatalf("create k3d cluster: %v", err)
	}
	if err := client.Ensure(ctx, spec); err != nil {
		t.Fatalf("idempotent ensure: %v", err)
	}
	kubeconfig, err := client.Kubeconfig(ctx, spec)
	if err != nil {
		t.Fatalf("read k3d kubeconfig: %v", err)
	}
	configPath := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(configPath, kubeconfig, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyGlobal(global, marker); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	const configMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: tilt-desktop-integration
data:
  value: verified
`
	tiltfile := "allow_k8s_contexts('k3d-" + name + "')\nk8s_yaml('configmap.yaml')\n"
	if err := os.WriteFile(filepath.Join(project, "Tiltfile"), []byte(tiltfile), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "configmap.yaml"), []byte(configMap), 0600); err != nil {
		t.Fatal(err)
	}

	manager, err := tilt.NewManager(tilt.ExecLauncher{Binary: "tilt"})
	if err != nil {
		t.Fatal(err)
	}
	config := tilt.Config{ProjectID: "test-app", Directory: project, Kubeconfig: configPath, ClusterName: name, Port: 10497}
	if err := manager.Start(ctx, config); err != nil {
		t.Fatalf("start Tilt: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if err := manager.Stop(stopCtx, config.ProjectID); err != nil {
			t.Errorf("clean up Tilt: %v", err)
		}
	})

	t.Log("waiting for Tilt API and deployed Kubernetes ConfigMap")
	var resources []tilt.Resource
	if err := await(ctx, readyTimeout, func(ctx context.Context) error {
		state, err := manager.Status(config.ProjectID)
		if err != nil {
			return err
		}
		if !state.Running {
			return fmt.Errorf("Tilt exited before resources were ready: %v", state.ExitErr)
		}
		resources, err = manager.Resources(ctx, config.ProjectID)
		if err != nil {
			return err
		}
		if len(resources) == 0 {
			return fmt.Errorf("Tilt API returned no resources")
		}
		return nil
	}); err != nil {
		t.Fatalf("Tilt API never became ready: %v", err)
	}
	t.Logf("Tilt resources: %+v", resources)
	if err := await(ctx, readyTimeout, func(ctx context.Context) error {
		_, err := kubectl(ctx, configPath, name, "get", "configmap", "tilt-desktop-integration", "-o", "name")
		return err
	}); err != nil {
		t.Fatalf("Tilt did not deploy ConfigMap: %v", err)
	}
	if _, err := manager.Logs(ctx, config.ProjectID, "", 100); err != nil {
		t.Fatalf("read Tilt logs: %v", err)
	}

	t.Log("stopping Tilt without deleting deployed Kubernetes resources")
	stopCtx, done := context.WithTimeout(ctx, 30*time.Second)
	if err := manager.Stop(stopCtx, config.ProjectID); err != nil {
		done()
		t.Fatalf("stop Tilt: %v", err)
	}
	done()
	if state, err := manager.Status(config.ProjectID); err != nil || state.Running {
		t.Fatalf("Tilt still running after Stop: state=%+v err=%v", state, err)
	}
	if _, err := kubectl(ctx, configPath, name, "get", "configmap", "tilt-desktop-integration"); err != nil {
		t.Fatalf("deployed resource unexpectedly removed by Tilt Stop: %v", err)
	}
	if err := verifyGlobal(global, marker); err != nil {
		t.Fatal(err)
	}

	t.Log("stopping and restarting the managed k3d cluster")
	if err := client.Stop(ctx, spec); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := client.Ensure(ctx, spec); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}
	if err := verifyGlobal(global, marker); err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(ctx, spec); err != nil {
		t.Fatalf("delete managed k3d cluster: %v", err)
	}
	t.Log("k3d and Tilt integration lifecycle completed")
}

func verifyGlobal(path, want string) error {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(bytes) != want {
		return fmt.Errorf("global kubeconfig changed: %q", bytes)
	}
	return nil
}

func kubectl(ctx context.Context, kubeconfig, cluster string, args ...string) ([]byte, error) {
	all := append([]string{"--kubeconfig", kubeconfig, "--context", "k3d-" + cluster}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", all...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func await(ctx context.Context, limit time.Duration, check func(context.Context) error) error {
	wait, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var last error
	for {
		attempt, done := context.WithTimeout(wait, 8*time.Second)
		last = check(attempt)
		done()
		if last == nil {
			return nil
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("%w (last error: %v)", wait.Err(), last)
		case <-time.After(2*time.Second):
		}
	}
}
