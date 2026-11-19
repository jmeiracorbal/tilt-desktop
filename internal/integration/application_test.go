//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmeiracorbal/tilt-desktop/internal/application"
	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

// Exercises the public application orchestration API using actual Docker,
// k3d, Tilt, and Kubernetes. No cluster or Tilt session is created manually.
func TestApplicationManagerLifecycle(t *testing.T) {
	for _, binary := range []string{"docker","k3d","tilt","kubectl"} {
		if _,err:=exec.LookPath(binary);err!=nil {t.Fatalf("%s required: %v",binary,err)}
	}
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Minute)
	defer cancel()
	root:=t.TempDir()
	t.Setenv("TMPDIR",root)
	global:=filepath.Join(root,"global-kubeconfig")
	const marker="global context must remain unchanged\n"
	if err:=os.WriteFile(global,[]byte(marker),0600);err!=nil{t.Fatal(err)}
	t.Setenv("KUBECONFIG",global)
	client,err:=k3d.NewClient(k3d.ExecRunner{Binary:"k3d",KubeconfigPath:filepath.Join(root,"runner-kubeconfig")},k3d.DockerInspector{Binary:"docker"})
	if err!=nil{t.Fatal(err)}
	tiltManager,err:=tilt.NewManager(tilt.ExecLauncher{Binary:"tilt"})
	if err!=nil{t.Fatal(err)}
	manager,err:=application.NewManager(client,tiltManager,application.NetPorts{},k3d.DockerInspector{Binary:"docker"})
	if err!=nil{t.Fatal(err)}
	projectDir:=filepath.Join(root,"app")
	if err:=os.Mkdir(projectDir,0700);err!=nil{t.Fatal(err)}
	const manifest="apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: tilt-desktop-app-manager\ndata:\n  value: managed\n"
	if err:=os.WriteFile(filepath.Join(projectDir,"configmap.yaml"),[]byte(manifest),0600);err!=nil{t.Fatal(err)}
	// The Tiltfile does not name a user-selected cluster. Its development
	// context is derived from the deterministic folder identity.
	def,err:=manager.Register(projectDir)
	if err==nil{t.Fatal("expected registration to reject a missing Tiltfile")}
	// Prepare the project against its canonical, stable cluster identity.
	// The ID is derived from the path before a user knows the cluster name.
	identifier:=application.ProjectID(projectDir)
	tiltfile:="allow_k8s_contexts('k3d-"+identifier+"')\nk8s_yaml('configmap.yaml')\n"
	if err:=os.WriteFile(filepath.Join(projectDir,"Tiltfile"),[]byte(tiltfile),0600);err!=nil{t.Fatal(err)}
	def,err=manager.Register(projectDir)
	if err!=nil{t.Fatal(err)}
	if def.ID!=identifier {t.Fatalf("deterministic identity mismatch %q %q",def.ID,identifier)}
	t.Cleanup(func(){
		clean,done:=context.WithTimeout(context.Background(),90*time.Second)
		defer done()
		// A uniquely named test cluster may need cleanup if the manager
		// failed before registration or an early startup step failed.
		all,listErr:=client.List(clean)
		if listErr!=nil {t.Errorf("list clusters for cleanup: %v",listErr);return}
		for _,c:=range all {
			if c.Name==def.ID {
				_,deleteErr:=(k3d.ExecRunner{Binary:"k3d",KubeconfigPath:filepath.Join(root,"runner-kubeconfig")}).Run(clean,"cluster","delete",def.ID)
				if deleteErr!=nil{t.Errorf("clean up test cluster: %v",deleteErr)}
			}
		}
	})
	t.Log("starting application without manual k3d setup")
	if err:=manager.Start(ctx,def.ID);err!=nil{t.Fatalf("start application: %v",err)}
	state,err:=manager.Status(def.ID)
	if err!=nil||!state.Running||state.RegistryPort==0||state.TiltPort==0{t.Fatalf("state: %+v %v",state,err)}
	configPath:=filepath.Join(root,"external-kubeconfig")
	saveKubeconfig:=func(){
		t.Helper()
		creds,err:=client.Kubeconfig(ctx,k3d.Spec{Name:def.ID,ManagementID:def.ID,RegistryPort:state.RegistryPort})
		if err!=nil{t.Fatal(err)}
		if err:=os.WriteFile(configPath,creds,0600);err!=nil{t.Fatal(err)}
	}
	saveKubeconfig()
	if err:=await(ctx,readyTimeout,func(ctx context.Context)error{
		_,err:=kubectl(ctx,configPath,def.ID,"get","configmap","tilt-desktop-app-manager")
		return err
	});err!=nil{t.Fatalf("Tilt did not deploy via application manager: %v",err)}
	if err:=await(ctx,readyTimeout,func(ctx context.Context)error{
		resources,err:=manager.Resources(ctx,def.ID)
		if err!=nil{return err}
		if len(resources)==0{return fmt.Errorf("no Tilt resources visible")}
		return nil
	});err!=nil{t.Fatalf("Tilt resources unavailable: %v",err)}
	if _,err:=manager.Logs(ctx,def.ID,"",100);err!=nil{t.Fatalf("Tilt logs: %v",err)}
	if err:=verifyGlobal(global,marker);err!=nil{t.Fatal(err)}
	t.Log("stopping application without deleting cluster or workloads")
	if err:=manager.Stop(ctx,def.ID);err!=nil{t.Fatalf("stop application: %v",err)}
	// A fresh manager has no prior in-memory registration or port assignment.
	// It must rediscover the existing cluster and its real registry port.
	secondTilt,err:=tilt.NewManager(tilt.ExecLauncher{Binary:"tilt"})
	if err!=nil{t.Fatal(err)}
	second,err:=application.NewManager(client,secondTilt,application.NetPorts{},k3d.DockerInspector{Binary:"docker"})
	if err!=nil{t.Fatal(err)}
	reopened,err:=second.Register(projectDir)
	if err!=nil||reopened.ID!=def.ID{t.Fatalf("re-register after restart: %+v %v",reopened,err)}
	t.Log("restarting application and recovering the existing dedicated cluster")
	if err:=second.Start(ctx,def.ID);err!=nil{t.Fatalf("recover existing cluster: %v",err)}
	state,err=second.Status(def.ID)
	if err!=nil||!state.Running{t.Fatalf("restarted status: %+v %v",state,err)}
	saveKubeconfig()
	if err:=await(ctx,readyTimeout,func(ctx context.Context)error{
		_,err:=kubectl(ctx,configPath,def.ID,"get","configmap","tilt-desktop-app-manager")
		return err
	});err!=nil{t.Fatalf("resource absent after restarting cluster: %v",err)}
	if err:=verifyGlobal(global,marker);err!=nil{t.Fatal(err)}
	t.Log("deleting application only after explicit confirmation")
	if err:=second.Delete(ctx,def.ID,false);err!=application.ErrConfirmationRequired{t.Fatalf("confirmation not enforced: %v",err)}
	if err:=second.Delete(ctx,def.ID,true);err!=nil{t.Fatalf("confirmed deletion: %v",err)}
	all,err:=client.List(ctx)
	if err!=nil{t.Fatal(err)}
	for _,c:=range all{if c.Name==def.ID{t.Fatal("cluster still exists after application deletion")}}
	t.Logf("application manager lifecycle succeeded for %s (registry %d, Tilt %d)",def.ID,state.RegistryPort,state.TiltPort)
}
