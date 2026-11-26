package application

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/environment"
)

func TestPersistentStartRejectsUnavailableEnvironment(t *testing.T) {
	m,c,tm,ports,_,_,dir,_:=persistentFixture(t)
	requirements:=m.requirements.(*fakeRequirements)
	requirements.err=environment.ErrRequirements
	def,err:=m.Register(dir)
	if err!=nil{t.Fatal(err)}
	if len(requirements.calls)!=0||len(m.List())!=1{t.Fatal("catalog access requires runtime")}
	ctx:=context.Background()
	if err:=m.Start(ctx,def.ID);!errors.Is(err,environment.ErrRequirements){t.Fatalf("start: %v",err)}
	if !slices.Equal(requirements.calls,[]environment.Requirement{environment.StartProject}){t.Fatalf("checks: %v",requirements.calls)}
	if len(c.trace.all())!=0||len(tm.configs)!=0||ports.index!=0{t.Fatal("failed preflight mutated runtime")}
	state,err:=m.Status(def.ID)
	if err!=nil||state.Phase!="idle"||state.TiltPort!=0||state.RegistryPort!=0{t.Fatalf("unexpected state: %+v %v",state,err)}
	requirements.err=nil
	if err:=m.Start(ctx,def.ID);err!=nil{t.Fatal(err)}
	if !slices.Equal(requirements.calls,[]environment.Requirement{environment.StartProject,environment.StartProject}){t.Fatalf("preflight not repeated: %v",requirements.calls)}
	if err:=m.Stop(ctx,def.ID);err!=nil{t.Fatal(err)}
}

func TestPersistentDedicatedOperationsRejectUnavailableClusterRuntime(t *testing.T) {
	m,c,tm,_,_,_,dir,_:=persistentFixture(t)
	ctx:=context.Background()
	def,err:=m.Register(dir)
	if err!=nil{t.Fatal(err)}
	if err:=m.Start(ctx,def.ID);err!=nil{t.Fatal(err)}
	requirements:=m.requirements.(*fakeRequirements)
	requirements.calls=nil
	requirements.err=environment.ErrRequirements
	before:=c.trace.all()
	if err:=m.Stop(ctx,def.ID);!errors.Is(err,environment.ErrRequirements){t.Fatalf("stop: %v",err)}
	if err:=m.Delete(ctx,def.ID,true);!errors.Is(err,environment.ErrRequirements){t.Fatalf("delete: %v",err)}
	if !slices.Equal(requirements.calls,[]environment.Requirement{environment.ManageCluster,environment.ManageCluster}){t.Fatalf("checks: %v",requirements.calls)}
	if !slices.Equal(before,c.trace.all())||!tm.running[def.ID]||!c.exists{t.Fatal("failed check changed runtime")}
	requirements.err=nil
	if err:=m.Delete(ctx,def.ID,true);err!=nil{t.Fatal(err)}
}

func TestSharedSessionCanStopWithoutClusterRuntime(t *testing.T) {
	m,c,tm,_,_,_,dir,_:=persistentFixture(t)
	ctx:=context.Background()
	const name="td-shared-development"
	def,err:=m.RegisterShared(dir,name)
	if err!=nil{t.Fatal(err)}
	if err:=m.Start(ctx,def.ID);err!=nil{t.Fatal(err)}
	requirements:=m.requirements.(*fakeRequirements)
	requirements.calls=nil
	requirements.err=environment.ErrRequirements
	if err:=m.Stop(ctx,def.ID);err!=nil{t.Fatal(err)}
	if err:=m.Delete(ctx,def.ID,true);err!=nil{t.Fatal(err)}
	if len(requirements.calls)!=0||tm.running[def.ID]||!c.exists{t.Fatal("project-only lifecycle depends on Docker")}
	before:=c.trace.all()
	if err:=m.StopSharedCluster(ctx,name);!errors.Is(err,environment.ErrRequirements){t.Fatalf("stop cluster: %v",err)}
	if err:=m.DeleteSharedCluster(ctx,name,true);!errors.Is(err,environment.ErrRequirements){t.Fatalf("delete cluster: %v",err)}
	if !slices.Equal(requirements.calls,[]environment.Requirement{environment.ManageCluster,environment.ManageCluster}){t.Fatalf("checks: %v",requirements.calls)}
	if !slices.Equal(before,c.trace.all()){t.Fatal("failed preflight changed cluster")}
	requirements.err=nil
	if err:=m.DeleteSharedCluster(ctx,name,true);err!=nil{t.Fatal(err)}
}

func TestPersistentObservationsRequireOnlyTilt(t *testing.T) {
	m,_,_,_,_,_,dir,_:=persistentFixture(t)
	ctx:=context.Background()
	def,err:=m.Register(dir)
	if err!=nil{t.Fatal(err)}
	requirements:=m.requirements.(*fakeRequirements)
	requirements.err=environment.ErrRequirements
	if _,err:=m.Resources(ctx,def.ID);!errors.Is(err,environment.ErrRequirements){t.Fatalf("resources: %v",err)}
	if _,err:=m.Logs(ctx,def.ID,"",100);!errors.Is(err,environment.ErrRequirements){t.Fatalf("logs: %v",err)}
	if !slices.Equal(requirements.calls,[]environment.Requirement{environment.ObserveTilt,environment.ObserveTilt}){t.Fatalf("checks: %v",requirements.calls)}
}

func TestPersistentManagerRequiresEvaluator(t *testing.T) {
	m,c,tm,ports,registries,observer,_,path:=persistentFixture(t)
	if err:=m.Close();err!=nil{t.Fatal(err)}
	if other,err:=NewPersistentManager(c,tm,ports,registries,observer,nil,path);err==nil{
		other.Close()
		t.Fatal("accepted absent evaluator")
	}
}
