package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
	"github.com/jmeiracorbal/tilt-desktop/internal/project"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

var ErrUnverifiedSession=errors.New("previous Tilt session cannot be safely managed")
var ErrManagerClosed=errors.New("persistent manager is closed")

// SessionObserver inspects an existing Tilt process without adopting it.
type SessionObserver interface {
	Inspect(context.Context,tilt.Config)(tilt.Session,error)
}

// PersistentManager preserves the project catalog and startup journal.
// Running Tilt processes remain owned by the manager that spawned them.
type PersistentManager struct {
	mu sync.Mutex
	core *Manager
	store *catalogStore
	observer SessionObserver
	lock io.Closer
	closed bool
}

// NewPersistentManager restores registered projects and their last known
// sessions. Reconcile must validate previous sessions before they are reused.
func NewPersistentManager(clusters Clusters,tiltManager Tilt,ports Ports,registries Registries,observer SessionObserver,path string)(*PersistentManager,error){
	if observer==nil{return nil,errors.New("Tilt session observer is required")}
	if !filepath.IsAbs(path){return nil,errors.New("absolute catalog path is required")}
	lock,err:=acquireInstance(filepath.Dir(path))
	if err!=nil{return nil,err}
	store,err:=openCatalog(path)
	if err!=nil{return nil,errors.Join(err,lock.Close())}
	core,err:=NewManager(clusters,tiltManager,ports,registries)
	if err!=nil{return nil,errors.Join(err,lock.Close())}
	for _,rec:=range store.all(){
		core.apps[rec.ID]=&entry{config:tilt.Config{Port:rec.TiltPort},definition:project.Definition{
			ID:rec.ID,Directory:rec.Directory,Tiltfile:filepath.Join(rec.Directory,"Tiltfile"),
			Cluster:project.ClusterAssignment{Mode:rec.Mode,Name:rec.ClusterName},
		}}
	}
	manager:=&PersistentManager{core:core,store:store,observer:observer,lock:lock}
	core.checkRegistryPort=store.checkRegistryPort
	core.checkTiltPort=store.checkTiltPort
	core.sharedRegistryPort=func(name string)(uint16,error){
		cluster,ok:=store.cluster(name)
		if !ok||cluster.Mode!=project.Shared{return 0,ErrClusterNotRegistered}
		return cluster.RegistryPort,nil
	}
	core.validateSharedStart=func(id string,spec k3d.Spec,existed bool)error{
		if !existed{
			for _,rec:=range store.all(){
				if rec.ClusterName==spec.Name&&rec.ID!=id&&rec.Phase!=phaseIdle{
					return fmt.Errorf("%w: project %s has an unresolved session",ErrClusterInUse,rec.ID)
				}
			}
		}
		return nil
	}
	core.beforeTiltStart=func(spec k3d.Spec, config tilt.Config)error{
		rec,err:=manager.record(config.ProjectID)
		if err!=nil{return err}
		if rec.ClusterName!=spec.Name||rec.Directory!=config.Directory{return errors.New("session does not match catalog assignment")}
		rec.RegistryPort=spec.RegistryPort
		rec.TiltPort=config.Port
		rec.Kubeconfig=config.Kubeconfig
		rec.Phase=phaseStarting
		rec.PID=0
		return store.upsert(rec)
	}
	return manager,nil
}

// NewLocalManager uses the user's private config directory for the catalog.
// It does not load the user's kubeconfig or change their active kubectl context.
func NewLocalManager() (*PersistentManager,error){
	clusterClient,err:=k3d.NewLocalClient()
	if err!=nil{return nil,err}
	tiltManager,err:=tilt.NewManager(tilt.ExecLauncher{Binary:"tilt"})
	if err!=nil{return nil,err}
	root,err:=localStateDirectory()
	if err!=nil{return nil,err}
	path:=filepath.Join(root,"catalog.json")
	return NewPersistentManager(clusterClient,tiltManager,NetPorts{},clusterClient,tiltManager,path)
}

// Close relinquishes the catalog without adopting or terminating child processes.
// The caller must stop owned sessions first for a graceful shutdown.
func (m *PersistentManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return nil}
	m.closed=true
	return m.lock.Close()
}

func (m *PersistentManager) record(id string)(catalogEntry,error){
	if m.closed{return catalogEntry{},ErrManagerClosed}
	for _,rec:=range m.store.all(){
		if rec.ID==id{return rec,nil}
	}
	return catalogEntry{},fmt.Errorf("%w: %s",ErrNotRegistered,id)
}

// Register validates the folder and makes a durable catalog entry. Existing
// records are kept intact, including any unresolved prior process identity.
func (m *PersistentManager) Register(directory string)(project.Definition,error){
	return m.register(directory,"")
}

// RegisterShared assigns a project to a managed shared cluster by its full
// name. A new cluster is recorded immediately and provisioned on first Start.
func (m *PersistentManager) RegisterShared(directory,name string)(project.Definition,error){
	if !validSharedName(name){return project.Definition{},fmt.Errorf("invalid shared cluster name %q",name)}
	return m.register(directory,name)
}

func (m *PersistentManager) register(directory,sharedName string)(project.Definition,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return project.Definition{},ErrManagerClosed}
	def,err:=m.core.register(directory,sharedName)
	if err!=nil{return def,err}
	if _,err:=m.record(def.ID);err==nil{return def,nil}
	cluster,exists:=m.store.cluster(def.Cluster.Name)
	if !exists{cluster=clusterRecord{Name:def.Cluster.Name,Mode:def.Cluster.Mode}}
	if err:=m.store.upsert(catalogEntry{catalogRecord:catalogRecord{ID:def.ID,Directory:def.Directory,ClusterName:def.Cluster.Name,Phase:phaseIdle},clusterRecord:cluster});err!=nil{
		m.core.mu.Lock();delete(m.core.apps,def.ID);m.core.mu.Unlock()
		return project.Definition{},fmt.Errorf("persist registered project: %w",err)
	}
	return def,nil
}

// List returns registered projects without requiring Docker or Tilt to be up.
func (m *PersistentManager) List() []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return nil}
	records:=m.store.all()
	result:=make([]State,0,len(records))
	for _,r:=range records{
		result=append(result,State{ProjectID:r.ID,Directory:r.Directory,ClusterName:r.ClusterName,ClusterMode:r.Mode,Registered:true,Phase:string(r.Phase),TiltPort:r.TiltPort,RegistryPort:r.RegistryPort})
	}
	return result
}

// Start refuses an unverified or orphaned session. This prevents accidentally
// spawning a second Tilt process after a GUI crash/restart.
func (m *PersistentManager) Start(ctx context.Context,id string)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	rec,err:=m.record(id)
	if err!=nil{return err}
	if rec.Phase!=phaseIdle{
		if err:=m.reconcile(ctx,&rec);err!=nil{return err}
		if rec.Phase!=phaseIdle{
			// The original manager still supervises a running process.
			a,lookupErr:=m.core.lookup(id)
			if lookupErr==nil&&a.active&&rec.Phase==phaseRunning{return nil}
			return fmt.Errorf("%w: project %s state %s",ErrUnverifiedSession,id,rec.Phase)
		}
	}
	if err:=projectFolderValid(rec);err!=nil{return err}
	if err:=m.core.Start(ctx,id);err!=nil{
		current,readErr:=m.record(id)
		if readErr==nil&&current.Phase==phaseStarting{
			current.Phase=phaseUnknown
			return errors.Join(err,m.store.upsert(current))
		}
		return err
	}
	status,err:=m.core.tilt.Status(id)
	if err!=nil{return err}
	a,err:=m.core.lookup(id)
	if err!=nil{return err}
	a.mu.Lock()
	config,spec:=a.config,a.spec
	a.mu.Unlock()
	rec.RegistryPort=spec.RegistryPort
	rec.TiltPort=config.Port
	rec.Kubeconfig=config.Kubeconfig
	rec.Phase=phaseRunning
	rec.PID=int64(status.PID)
	if err:=m.store.upsert(rec);err!=nil{return fmt.Errorf("record running Tilt session: %w",err)}
	return nil
}

func projectFolderValid(rec catalogEntry)error{
	if _,err:=project.NewDefinition(rec.ID,rec.Directory,project.ClusterAssignment{Mode:rec.Mode,Name:rec.ClusterName});err!=nil{
		return fmt.Errorf("validate registered project: %w",err)
	}
	return nil
}

func (m *PersistentManager) reconcile(ctx context.Context,rec *catalogEntry)error{
	if rec.Phase==phaseIdle{return nil}
	a,err:=m.core.lookup(rec.ID)
	if err!=nil{return err}
	a.mu.Lock()
	active:=a.active
	a.mu.Unlock()
	if active{
		status,err:=m.core.Status(rec.ID)
		if err!=nil{return err}
		if status.Running {
			rec.Phase=phaseRunning
			return m.store.upsert(*rec)
		}
		// The manager spawned this process and observed its termination.
		if err:=removePrivateKubeconfig(rec.Kubeconfig);err!=nil{return err}
		rec.Phase=phaseIdle
		rec.Kubeconfig=""
		rec.PID=0
		return m.store.upsert(*rec)
	}
	config:=tilt.Config{ProjectID:rec.ID,Directory:rec.Directory,ClusterName:rec.ClusterName,
		Kubeconfig:rec.Kubeconfig,Port:rec.TiltPort}
	session,inspectErr:=m.observer.Inspect(ctx,config)
	if inspectErr==nil {
		if rec.PID!=0&&rec.PID!=session.PID{
			rec.Phase=phaseUnknown
			return errors.Join(m.store.upsert(*rec),fmt.Errorf("%w: PID mismatch for %s",ErrUnverifiedSession,rec.ID))
		}
		rec.Phase=phaseOrphaned
		if err:=m.store.upsert(*rec);err!=nil{return err}
		return nil
	}
	if rec.PID==0{
		rec.Phase=phaseUnknown
		if err:=m.store.upsert(*rec);err!=nil{return err}
		return nil
	}
	alive,err:=processAlive(rec.PID)
	if err!=nil{
		rec.Phase=phaseUnknown
		return errors.Join(m.store.upsert(*rec),fmt.Errorf("%w: check PID %d: %v",ErrUnverifiedSession,rec.PID,err))
	}
	if alive {
		rec.Phase=phaseUnknown
		return m.store.upsert(*rec)
	}
	// A missing process ID is not sufficient: the old API port must be free.
	if rec.TiltPort==0||!loopbackPortFree(rec.TiltPort){
		rec.Phase=phaseUnknown
		return m.store.upsert(*rec)
	}
	if err:=removePrivateKubeconfig(rec.Kubeconfig);err!=nil{return err}
	rec.Phase=phaseIdle
	rec.PID=0
	rec.Kubeconfig=""
	return m.store.upsert(*rec)
}

func removePrivateKubeconfig(path string)error{
	if path==""{return nil}
	if !strings.HasPrefix(filepath.Base(path),"tilt-desktop-kubeconfig-"){
		return fmt.Errorf("unexpected credential path %q",path)
	}
	if err:=os.Remove(path);err!=nil&&!errors.Is(err,os.ErrNotExist){return fmt.Errorf("remove stale credentials: %w",err)}
	return nil
}

// Reconcile observes the old process. A live session is reported as orphaned:
// this manager will not silently adopt its OS process handle.
func (m *PersistentManager) Reconcile(ctx context.Context,id string)(State,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	rec,err:=m.record(id)
	if err!=nil{return State{},err}
	if err:=m.reconcile(ctx,&rec);err!=nil{return State{},err}
	return m.status(rec)
}

// ReconcileAll is explicit so callers can choose when to perform external
// probes. A failed probe does not reset the session to an unsafe idle state.
func (m *PersistentManager) ReconcileAll(ctx context.Context)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return ErrManagerClosed}
	var errs []error
	for _,rec:=range m.store.all(){
		if err:=m.reconcile(ctx,&rec);err!=nil{errs=append(errs,fmt.Errorf("%s: %w",rec.ID,err))}
	}
	return errors.Join(errs...)
}

func (m *PersistentManager) status(rec catalogEntry)(State,error){
	state:=State{ProjectID:rec.ID,Directory:rec.Directory,ClusterName:rec.ClusterName,ClusterMode:rec.Mode,
		Registered:true,Phase:string(rec.Phase),TiltPort:rec.TiltPort,RegistryPort:rec.RegistryPort}
	a,err:=m.core.lookup(rec.ID)
	if err!=nil{return state,err}
	a.mu.Lock()
	active:=a.active
	a.mu.Unlock()
	if active{
		observed,err:=m.core.Status(rec.ID)
		if err!=nil{return state,err}
		state.Running=observed.Running
		state.ExitErr=observed.ExitErr
	}else if rec.Phase==phaseOrphaned{
		state.Running=true
	}
	return state,nil
}

func (m *PersistentManager) Status(id string)(State,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	rec,err:=m.record(id)
	if err!=nil{return State{},err}
	return m.status(rec)
}

// Stop only acts on sessions owned by this instance. For a restored idle
// session it can stop the existing managed cluster independently.
func (m *PersistentManager) Stop(ctx context.Context,id string)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stop(ctx,id)
}

func (m *PersistentManager) stop(ctx context.Context,id string)error{
	rec,err:=m.record(id)
	if err!=nil{return err}
	if rec.Phase!=phaseIdle{
		if err:=m.reconcile(ctx,&rec);err!=nil{return err}
		if rec.Phase!=phaseIdle{
			a,_:=m.core.lookup(id);a.mu.Lock();active:=a.active;a.mu.Unlock()
			if !active{return fmt.Errorf("%w: %s (%s)",ErrUnverifiedSession,id,rec.Phase)}
		}
	}
	if err:=m.core.Stop(ctx,id);err!=nil{return err}
	if rec.Phase==phaseIdle&&rec.Mode==project.Dedicated{
		// Restored catalog records have no child-process handle; they can
		// still stop a cluster after ownership checks in k3d.
		if err:=m.core.clusters.stopExisting(ctx,rec.ClusterName);err!=nil{return err}
	}
	rec.Phase=phaseIdle
	rec.PID=0
	rec.Kubeconfig=""
	return m.store.upsert(rec)
}

// Delete refuses unresolved sessions, and requires explicit confirmation.
func (m *PersistentManager) Delete(ctx context.Context,id string,confirmed bool)error{
	if !confirmed{return ErrConfirmationRequired}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err:=m.stop(ctx,id);err!=nil{return err}
	if err:=m.core.Delete(ctx,id,true);err!=nil{return err}
	return m.store.remove(id)
}

func (m *PersistentManager) Resources(ctx context.Context,id string)([]tilt.Resource,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return nil,ErrManagerClosed}
	return m.core.Resources(ctx,id)
}
func (m *PersistentManager) Logs(ctx context.Context,id,resource string,tail int)([]byte,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return nil,ErrManagerClosed}
	return m.core.Logs(ctx,id,resource,tail)
}

