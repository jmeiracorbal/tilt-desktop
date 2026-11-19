// Package application composes project definitions, dedicated k3d clusters,
// and independent Tilt sessions without introducing a GUI or persistent catalog.
package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
	"github.com/jmeiracorbal/tilt-desktop/internal/project"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

var (
	ErrNotRegistered = errors.New("application is not registered")
	ErrConfirmationRequired = errors.New("application deletion requires confirmation")
)

// Clusters is the lifecycle contract implemented by the k3d client.
type Clusters interface {
	List(context.Context) ([]k3d.Cluster,error)
	Ensure(context.Context,k3d.Spec) error
	Kubeconfig(context.Context,k3d.Spec) ([]byte,error)
	Stop(context.Context,k3d.Spec) error
	Delete(context.Context,k3d.Spec) error
}

// Tilt is the process lifecycle and observation contract.
type Tilt interface {
	Start(context.Context,tilt.Config) error
	Stop(context.Context,string) error
	Status(string) (tilt.State,error)
	Resources(context.Context,string) ([]tilt.Resource,error)
	Logs(context.Context,string,string,int) ([]byte,error)
}

// Ports selects available local TCP ports (not persistent reservations).
type Ports interface { Available() (uint16,error) }

// Registries discovers the host port of an existing managed registry.
type Registries interface { RegistryPort(context.Context,string) (uint16,error) }

// State is a snapshot from this manager, not Kubernetes workload readiness.
type State struct {
	ProjectID string
	Directory string
	ClusterName string
	Registered bool
	Phase string
	Running bool
	TiltPort uint16
	RegistryPort uint16
	ExitErr error
}

type entry struct {
	mu sync.Mutex
	definition project.Definition
	deleted bool
	active bool
	spec k3d.Spec
	config tilt.Config
}

// Manager serializes each project's lifecycle independently. Cross-process
// locking, catalog persistence and shared clusters are outside its scope.
type Manager struct {
	mu sync.RWMutex
	clusters Clusters
	tilt Tilt
	ports Ports
	registries Registries
	apps map[string]*entry
	beforeTiltStart func(k3d.Spec, tilt.Config) error
}

func NewManager(clusters Clusters, tiltManager Tilt, ports Ports, registries Registries) (*Manager,error) {
	if clusters==nil || tiltManager==nil || ports==nil || registries==nil {
		return nil,errors.New("cluster manager, Tilt manager, port allocator and registry inspector are required")
	}
	return &Manager{clusters:clusters,tilt:tiltManager,ports:ports,registries:registries,apps:make(map[string]*entry)},nil
}

// Register validates the project's Tiltfile and derives a deterministic
// cluster identity from the canonical absolute path.
func (m *Manager) Register(directory string) (project.Definition,error) {
	if directory=="" {return project.Definition{},errors.New("project directory is required")}
	root,err:=filepath.Abs(directory)
	if err!=nil{return project.Definition{},fmt.Errorf("resolve project directory: %w",err)}
	root,err=filepath.EvalSymlinks(root)
	if err!=nil{return project.Definition{},fmt.Errorf("canonicalize project directory: %w",err)}
	id:=projectID(root)
	definition,err:=project.NewDefinition(id,root,project.ClusterAssignment{Mode:project.Dedicated,Name:id})
	if err!=nil{return project.Definition{},err}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _,exists:=m.apps[id];!exists {m.apps[id]=&entry{definition:definition}}
	return definition,nil
}

func (m *Manager) lookup(id string) (*entry,error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a,ok:=m.apps[id]
	if !ok{return nil,fmt.Errorf("%w: %s",ErrNotRegistered,id)}
	return a,nil
}

func (m *Manager) existingSpec(ctx context.Context,name string) (k3d.Spec,bool,error) {
	all,err:=m.clusters.List(ctx)
	if err!=nil{return k3d.Spec{},false,fmt.Errorf("discover k3d clusters: %w",err)}
	for _,c:=range all {
		if c.Name!=name {continue}
		port,err:=m.registries.RegistryPort(ctx,name)
		if err!=nil{return k3d.Spec{},true,fmt.Errorf("discover registry port for %q: %w",name,err)}
		if port==0{return k3d.Spec{},true,fmt.Errorf("registry for %q has no published port",name)}
		return k3d.Spec{Name:name,ManagementID:name,RegistryPort:port},true,nil
	}
	return k3d.Spec{},false,nil
}

func (m *Manager) clusterSpec(ctx context.Context,a *entry) (k3d.Spec,bool,error) {
	name:=a.definition.Cluster.Name
	spec,exists,err:=m.existingSpec(ctx,name)
	if err!=nil||exists{return spec,exists,err}
	port,err:=m.ports.Available()
	if err!=nil{return k3d.Spec{},false,fmt.Errorf("allocate registry port: %w",err)}
	if port==0{return k3d.Spec{},false,errors.New("port allocator returned zero registry port")}
	return k3d.Spec{Name:name,ManagementID:name,RegistryPort:port},false,nil
}

// Start creates or resumes a dedicated cluster, writes its credentials to a
// private temporary file, then starts Tilt. When a newly created cluster
// cannot be started it is rolled back; a preexisting one is never deleted.
func (m *Manager) Start(ctx context.Context,id string) error {
	a,err:=m.lookup(id)
	if err!=nil{return err}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted{return fmt.Errorf("%w: %s",ErrNotRegistered,id)}
	if err:=ctx.Err();err!=nil{return err}
	if a.active {
		state,err:=m.tilt.Status(id)
		if err!=nil{return fmt.Errorf("read Tilt status: %w",err)}
		if state.Running{return nil}
		if err:=os.Remove(a.config.Kubeconfig);err!=nil&&!errors.Is(err,os.ErrNotExist){
			return fmt.Errorf("remove credentials from exited Tilt session: %w",err)
		}
		a.active=false
	}
	spec,existed,err:=m.clusterSpec(ctx,a)
	if err!=nil{return err}
	if err:=m.clusters.Ensure(ctx,spec);err!=nil{
		return m.rollback(spec,existed,fmt.Errorf("prepare cluster: %w",err))
	}
	credentials,err:=m.clusters.Kubeconfig(ctx,spec)
	if err!=nil{return m.rollback(spec,existed,fmt.Errorf("read private kubeconfig: %w",err))}
	if len(credentials)==0{return m.rollback(spec,existed,errors.New("empty private kubeconfig"))}
	file,err:=os.CreateTemp("","tilt-desktop-kubeconfig-*")
	if err!=nil{return m.rollback(spec,existed,fmt.Errorf("create private kubeconfig: %w",err))}
	path:=file.Name()
	if _,err:=file.Write(credentials);err!=nil {
		_ = file.Close()
		_ = os.Remove(path)
		return m.rollback(spec,existed,fmt.Errorf("write private kubeconfig: %w",err))
	}
	if err:=file.Close();err!=nil {
		_ = os.Remove(path)
		return m.rollback(spec,existed,fmt.Errorf("close private kubeconfig: %w",err))
	}
	port:=a.config.Port
	if port!=0 {
		if !loopbackPortFree(port){
			_ = os.Remove(path)
			return m.rollback(spec,existed,fmt.Errorf("previously assigned Tilt port %d is unavailable",port))
		}
	}else{
		var err error
		port,err=m.ports.Available()
		if err!=nil || port==0 {
			_ = os.Remove(path)
			if err==nil {err=errors.New("port allocator returned zero Tilt port")}
			return m.rollback(spec,existed,fmt.Errorf("allocate Tilt HTTP port: %w",err))
		}
	}
	config:=tilt.Config{ProjectID:id,Directory:a.definition.Directory,Kubeconfig:path,ClusterName:spec.Name,Port:port}
	if m.beforeTiltStart!=nil {
		if err:=m.beforeTiltStart(spec,config);err!=nil {
			_ = os.Remove(path)
			return m.rollback(spec,existed,fmt.Errorf("record Tilt startup: %w",err))
		}
	}
	if err:=m.tilt.Start(ctx,config);err!=nil {
		removeErr:=os.Remove(path)
		return errors.Join(m.rollback(spec,existed,fmt.Errorf("start Tilt: %w",err)),removeErr)
	}
	a.spec,a.config,a.active=spec,config,true
	return nil
}

// rollback uses an independent bounded context so canceled starts cannot
// abandon a cluster that this attempt created.
func (m *Manager) rollback(spec k3d.Spec,existed bool,cause error) error {
	if existed{return cause}
	ctx,cancel:=context.WithTimeout(context.Background(),cleanupTimeout)
	defer cancel()
	if err:=m.clusters.Delete(ctx,spec);err!=nil {
		return errors.Join(cause,fmt.Errorf("rollback newly created cluster: %w",err))
	}
	return cause
}

// Stop interrupts Tilt, then stops the dedicated cluster. It does not run
// tilt down or delete the cluster's deployed workloads and persistent data.
func (m *Manager) Stop(ctx context.Context,id string) error {
	a,err:=m.lookup(id)
	if err!=nil{return err}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted{return fmt.Errorf("%w: %s",ErrNotRegistered,id)}
	return m.stop(ctx,a)
}

func (m *Manager) stop(ctx context.Context,a *entry) error {
	if !a.active{return nil}
	if err:=m.tilt.Stop(ctx,a.definition.ID);err!=nil{return fmt.Errorf("stop Tilt: %w",err)}
	if err:=m.clusters.Stop(ctx,a.spec);err!=nil{return fmt.Errorf("stop cluster: %w",err)}
	if err:=os.Remove(a.config.Kubeconfig);err!=nil&&!errors.Is(err,os.ErrNotExist) {
		return fmt.Errorf("remove private kubeconfig: %w",err)
	}
	a.active=false
	return nil
}

// Delete destroys the dedicated cluster only with explicit confirmation.
// It never adopts or destroys a shared cluster.
func (m *Manager) Delete(ctx context.Context,id string,confirmed bool) error {
	if !confirmed{return ErrConfirmationRequired}
	a,err:=m.lookup(id)
	if err!=nil{return err}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted{return fmt.Errorf("%w: %s",ErrNotRegistered,id)}
	if err:=m.stop(ctx,a);err!=nil{return err}
	spec,exists,err:=m.existingSpec(ctx,a.definition.Cluster.Name)
	if err!=nil{return err}
	if exists {
		if err:=m.clusters.Delete(ctx,spec);err!=nil{return fmt.Errorf("delete cluster: %w",err)}
	}
	a.deleted=true
	m.mu.Lock()
	delete(m.apps,id)
	m.mu.Unlock()
	return nil
}

// Status reports the current in-memory session and its last Tilt exit error.
func (m *Manager) Status(id string) (State,error) {
	a,err:=m.lookup(id)
	if err!=nil{return State{},err}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted{return State{},fmt.Errorf("%w: %s",ErrNotRegistered,id)}
	result:=State{ProjectID:id,Directory:a.definition.Directory,ClusterName:a.definition.Cluster.Name,Registered:true}
	if !a.active{return result,nil}
	state,err:=m.tilt.Status(id)
	if err!=nil{return State{},fmt.Errorf("read Tilt status: %w",err)}
	result.Running,result.ExitErr=state.Running,state.ExitErr
	result.TiltPort,result.RegistryPort=a.config.Port,a.spec.RegistryPort
	return result,nil
}

// Resources and Logs return observations scoped to the registered project.
func (m *Manager) Resources(ctx context.Context,id string) ([]tilt.Resource,error) {
	if _,err:=m.lookup(id);err!=nil{return nil,err}
	return m.tilt.Resources(ctx,id)
}
func (m *Manager) Logs(ctx context.Context,id,resource string,tail int) ([]byte,error) {
	if _,err:=m.lookup(id);err!=nil{return nil,err}
	return m.tilt.Logs(ctx,id,resource,tail)
}
