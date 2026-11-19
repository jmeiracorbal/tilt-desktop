package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

type fakeObserver struct {
	session tilt.Session
	err error
	count int
}
func (o *fakeObserver) Inspect(_ context.Context,_ tilt.Config)(tilt.Session,error) {
	o.count++
	return o.session,o.err
}

func persistentFixture(t *testing.T)(*PersistentManager,*fakeCluster,*fakeTilt,*fakePorts,*fakeRegistries,*fakeObserver,string,string) {
	t.Helper()
	_,c,tm,p,r,dir:=setup(t)
	observer:=&fakeObserver{err:errors.New("Tilt session endpoint unavailable")}
	path:=filepath.Join(t.TempDir(),"state","catalog.json")
	m,err:=NewPersistentManager(c,tm,p,r,observer,path)
	if err!=nil{t.Fatal(err)}
	return m,c,tm,p,r,observer,dir,path
}

func TestPersistentRegistrationAndStopSurviveRestart(t *testing.T){
	m,c,tm,p,r,observer,dir,path:=persistentFixture(t)
	def,err:=m.Register(dir)
	if err!=nil{t.Fatal(err)}
	if got:=m.List();len(got)!=1||got[0].ProjectID!=def.ID||got[0].Phase!="idle"{
		t.Fatalf("persisted registration %+v",got)
	}
	if err:=m.Start(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	state,err:=m.Status(def.ID)
	if err!=nil||!state.Running||state.Phase!="running"||state.TiltPort!=10351{
		t.Fatalf("running state %+v %v",state,err)
	}
	if err:=m.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	reloaded,err:=NewPersistentManager(c,tm,p,r,observer,path)
	if err!=nil{t.Fatal(err)}
	list:=reloaded.List()
	if len(list)!=1||list[0].Phase!="idle"||list[0].ProjectID!=def.ID {
		t.Fatalf("reloaded records %+v",list)
	}
	if err:=reloaded.Start(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	if err:=reloaded.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	if err:=reloaded.Delete(context.Background(),def.ID,false);!errors.Is(err,ErrConfirmationRequired){t.Fatalf("delete without confirmation: %v",err)}
	if err:=reloaded.Delete(context.Background(),def.ID,true);err!=nil{t.Fatal(err)}
	last,err:=openCatalog(path)
	if err!=nil||len(last.all())!=0{t.Fatalf("catalog deletion not durable: %+v %v",last,err)}
}

func TestPersistentOrphanCannotBeStartedOrDeleted(t *testing.T){
	m,c,tm,p,r,observer,dir,path:=persistentFixture(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	if err:=m.Start(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	rec,err:=m.record(def.ID);if err!=nil{t.Fatal(err)}
	if rec.PID!=int64(os.Getpid())||rec.TiltPort==0||rec.Kubeconfig=="" {
		t.Fatalf("process identity was not journaled: %+v",rec)
	}
	observer.session=tilt.Session{PID:rec.PID,StartTime:"2026-11-19T03:00:00Z",TiltfilePath:filepath.Join(dir,"Tiltfile")}
	observer.err=nil
	second,err:=NewPersistentManager(c,tm,p,r,observer,path)
	if err!=nil{t.Fatal(err)}
	state,err:=second.Reconcile(context.Background(),def.ID)
	if err!=nil||state.Phase!="orphaned"||!state.Running {t.Fatalf("unrecognized orphan: %+v %v",state,err)}
	if err:=second.Start(context.Background(),def.ID);!errors.Is(err,ErrUnverifiedSession){t.Fatalf("duplicate Tilt permitted: %v",err)}
	if err:=second.Stop(context.Background(),def.ID);!errors.Is(err,ErrUnverifiedSession){t.Fatalf("orphan stop permitted: %v",err)}
	if err:=second.Delete(context.Background(),def.ID,true);!errors.Is(err,ErrUnverifiedSession){t.Fatalf("orphan deletion permitted: %v",err)}
	if !c.exists {t.Fatal("orphan caused cluster deletion")}
	if err:=m.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
}

func TestRecoverDefinitelyExitedProcess(t *testing.T){
	m,_,_,_,_,observer,dir,_:=persistentFixture(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	port,err:=(NetPorts{}).Available();if err!=nil{t.Fatal(err)}
	tmp,err:=os.CreateTemp("","tilt-desktop-kubeconfig-*");if err!=nil{t.Fatal(err)}
	path:=tmp.Name();if err:=tmp.Close();err!=nil{t.Fatal(err)}
	rec:=catalogRecord{
		ID:def.ID,Directory:def.Directory,ClusterName:def.Cluster.Name,
		RegistryPort:5111,TiltPort:port,Kubeconfig:path,PID:999999999,Phase:phaseRunning,
	}
	if err:=m.store.upsert(rec);err!=nil{t.Fatal(err)}
	state,err:=m.Reconcile(context.Background(),def.ID)
	if err!=nil||state.Phase!="idle"||state.Running {t.Fatalf("stale session not cleared %+v %v",state,err)}
	if _,err:=os.Stat(path);!errors.Is(err,os.ErrNotExist){t.Fatalf("stale credentials present: %v",err)}
	if observer.count!=1{t.Fatalf("inspection count %d",observer.count)}
}

func TestReconcileLeavesUnknownProcessUntouched(t *testing.T){
	m,_,_,_,_,observer,dir,_:=persistentFixture(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	f,err:=os.CreateTemp("","tilt-desktop-kubeconfig-*");if err!=nil{t.Fatal(err)}
	defer os.Remove(f.Name());f.Close()
	rec:=catalogRecord{ID:def.ID,Directory:def.Directory,ClusterName:def.Cluster.Name,
		RegistryPort:5111,TiltPort:10351,Kubeconfig:f.Name(),PID:int64(os.Getpid()),Phase:phaseRunning}
	if err:=m.store.upsert(rec);err!=nil{t.Fatal(err)}
	state,err:=m.Reconcile(context.Background(),def.ID)
	if err!=nil||state.Phase!="unknown"{t.Fatalf("expected unknown state: %+v %v",state,err)}
	if err:=m.Start(context.Background(),def.ID);!errors.Is(err,ErrUnverifiedSession){t.Fatalf("unsafe restart: %v",err)}
	if _,err:=os.Stat(f.Name());err!=nil{t.Fatalf("removed credentials of potentially active session: %v",err)}
	if observer.count<1{t.Fatal("missing inspection")}
}

func TestPersistentCatalogRefusesUntrustedPath(t *testing.T){
	_,c,tm,p,r,observer,_,_:=persistentFixture(t)
	if _,err:=NewPersistentManager(c,tm,p,r,observer,"relative/catalog.json");err==nil{
		t.Fatal("accepted relative catalog")
	}
}

func TestPersistentRegistrationRejectsMissingTiltfile(t *testing.T){
	m,_,_,_,_,_,_,_:=persistentFixture(t)
	without:=t.TempDir()
	if _,err:=m.Register(without);err==nil||!strings.Contains(err.Error(),"Tiltfile"){t.Fatalf("accepted missing Tiltfile: %v",err)}
	if len(m.List())!=0{t.Fatal("invalid project added to catalog")}
}
