package application

import (
	"context"
	"fmt"
	"sort"

	"github.com/jmeiracorbal/tilt-desktop/internal/environment"
	"github.com/jmeiracorbal/tilt-desktop/internal/project"
)

// SharedClusterState reports catalog assignments, not runtime readiness.
// Unresolved includes sessions that may still be running after a restart.
type SharedClusterState struct {
	Name string
	RegistryPort uint16
	Projects int
	Unresolved int
}

func (m *PersistentManager) ListSharedClusters() ([]SharedClusterState,error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return nil,ErrManagerClosed}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	result:=make([]SharedClusterState,0)
	for _,cluster:=range m.store.clusters{
		if cluster.Mode!=project.Shared{continue}
		state:=SharedClusterState{Name:cluster.Name,RegistryPort:cluster.RegistryPort}
		for _,rec:=range m.store.records{
			if rec.ClusterName!=cluster.Name{continue}
			state.Projects++
			if rec.Phase!=phaseIdle{state.Unresolved++}
		}
		result=append(result,state)
	}
	sort.Slice(result,func(i,j int)bool{return result[i].Name<result[j].Name})
	return result,nil
}

// StopSharedCluster is explicit; a project's Stop never stops a shared
// cluster. All associated sessions must be verified idle before stopping it.
func (m *PersistentManager) StopSharedCluster(ctx context.Context,name string)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return ErrManagerClosed}
	cluster,ok:=m.store.cluster(name)
	if !ok||cluster.Mode!=project.Shared{return ErrClusterNotRegistered}
	for _,rec:=range m.store.all(){
		if rec.ClusterName!=name{continue}
		if err:=m.reconcile(ctx,&rec);err!=nil{return err}
		if rec.Phase!=phaseIdle{
			return fmt.Errorf("%w: project %s is %s",ErrClusterInUse,rec.ID,rec.Phase)
		}
	}
	if err:=m.requirements.Require(ctx,environment.ManageCluster);err!=nil{return err}
	return m.core.clusters.stopExisting(ctx,name)
}

// DeleteSharedCluster requires separate confirmation and zero project
// assignments. Runtime ownership is checked by the k3d client before deletion.
func (m *PersistentManager) DeleteSharedCluster(ctx context.Context,name string,confirmed bool)error{
	if !confirmed{return ErrConfirmationRequired}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed{return ErrManagerClosed}
	cluster,ok:=m.store.cluster(name)
	if !ok||cluster.Mode!=project.Shared{return ErrClusterNotRegistered}
	for _,rec:=range m.store.all(){
		if rec.ClusterName==name{return fmt.Errorf("%w: project %s is still assigned",ErrClusterInUse,rec.ID)}
	}
	if err:=m.requirements.Require(ctx,environment.ManageCluster);err!=nil{return err}
	if err:=m.core.clusters.deleteExisting(ctx,name);err!=nil{return err}
	return m.store.removeSharedCluster(name)
}
