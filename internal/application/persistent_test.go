package application

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/project"
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
	t.Cleanup(func(){if err:=m.Close();err!=nil{t.Error(err)}})
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
	if err:=m.Close();err!=nil{t.Fatal(err)}
	reloaded,err:=NewPersistentManager(c,tm,p,r,observer,path)
	if err!=nil{t.Fatal(err)}
	t.Cleanup(func(){if err:=reloaded.Close();err!=nil{t.Error(err)}})
	list:=reloaded.List()
	if len(list)!=1||list[0].Phase!="idle"||list[0].ProjectID!=def.ID {
		t.Fatalf("reloaded records %+v",list)
	}
	if err:=reloaded.Start(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	restarted,err:=reloaded.Status(def.ID)
	if err!=nil||restarted.TiltPort!=state.TiltPort||p.index!=2{
		t.Fatalf("persisted Tilt port was not reused: %+v index=%d err=%v",restarted,p.index,err)
	}
	if err:=reloaded.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	if err:=reloaded.Delete(context.Background(),def.ID,false);!errors.Is(err,ErrConfirmationRequired){t.Fatalf("delete without confirmation: %v",err)}
	if err:=reloaded.Delete(context.Background(),def.ID,true);err!=nil{t.Fatal(err)}
	if err:=reloaded.Close();err!=nil{t.Fatal(err)}
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
	if err:=m.Close();err!=nil{t.Fatal(err)}
	second,err:=NewPersistentManager(c,tm,p,r,observer,path)
	if err!=nil{t.Fatal(err)}
	t.Cleanup(func(){if err:=second.Close();err!=nil{t.Error(err)}})
	state,err:=second.Reconcile(context.Background(),def.ID)
	if err!=nil||state.Phase!="orphaned"||!state.Running {t.Fatalf("unrecognized orphan: %+v %v",state,err)}
	if err:=second.Start(context.Background(),def.ID);!errors.Is(err,ErrUnverifiedSession){t.Fatalf("duplicate Tilt permitted: %v",err)}
	if err:=second.Stop(context.Background(),def.ID);!errors.Is(err,ErrUnverifiedSession){t.Fatalf("orphan stop permitted: %v",err)}
	if err:=second.Delete(context.Background(),def.ID,true);!errors.Is(err,ErrUnverifiedSession){t.Fatalf("orphan deletion permitted: %v",err)}
	if !c.exists {t.Fatal("orphan caused cluster deletion")}
	// Simulate the original desktop disappearing without stopping its child.
	if err:=tm.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	if err:=os.Remove(rec.Kubeconfig);err!=nil{t.Fatal(err)}
}

func TestRecoverDefinitelyExitedProcess(t *testing.T){
	m,_,_,_,_,observer,dir,_:=persistentFixture(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	port,err:=(NetPorts{}).Available();if err!=nil{t.Fatal(err)}
	tmp,err:=os.CreateTemp("","tilt-desktop-kubeconfig-*");if err!=nil{t.Fatal(err)}
	path:=tmp.Name();if err:=tmp.Close();err!=nil{t.Fatal(err)}
	rec:=catalogEntry{
		catalogRecord:catalogRecord{ID:def.ID,Directory:def.Directory,ClusterName:def.Cluster.Name,
			TiltPort:port,Kubeconfig:path,PID:999999999,Phase:phaseRunning},
		clusterRecord:clusterRecord{Name:def.Cluster.Name,Mode:project.Dedicated,RegistryPort:5111},
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
	rec:=catalogEntry{
		catalogRecord:catalogRecord{ID:def.ID,Directory:def.Directory,ClusterName:def.Cluster.Name,
			TiltPort:10351,Kubeconfig:f.Name(),PID:int64(os.Getpid()),Phase:phaseRunning},
		clusterRecord:clusterRecord{Name:def.Cluster.Name,Mode:project.Dedicated,RegistryPort:5111},
	}
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


func TestPersistedTiltPortConflictStopsBeforeProcessLaunch(t *testing.T){
	m,c,tm,p,r,observer,dir,path:=persistentFixture(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	listener,err:=net.Listen("tcp4","127.0.0.1:0")
	if err!=nil{t.Fatal(err)}
	defer listener.Close()
	taken:=uint16(listener.Addr().(*net.TCPAddr).Port)
	rec,err:=m.record(def.ID);if err!=nil{t.Fatal(err)}
	rec.TiltPort=taken
	if err:=m.store.upsert(rec);err!=nil{t.Fatal(err)}
	if err:=m.Close();err!=nil{t.Fatal(err)}
	restored,err:=NewPersistentManager(c,tm,p,r,observer,path)
	if err!=nil{t.Fatal(err)}
	t.Cleanup(func(){if err:=restored.Close();err!=nil{t.Error(err)}})
	err=restored.Start(context.Background(),def.ID)
	if err==nil||!strings.Contains(err.Error(),"unavailable"){
		t.Fatalf("accepted port occupied by another service: %v",err)
	}
	if len(tm.configs)!=0{t.Fatal("Tilt launched despite occupied port")}
	if c.exists{t.Fatal("new cluster was not rolled back")}
}

func TestPersistentManagerExclusiveOwnership(t *testing.T) {
	m,c,tm,p,r,observer,dir,path:=persistentFixture(t)
	def,err:=m.Register(dir)
	if err!=nil{t.Fatal(err)}
	other,err:=NewPersistentManager(c,tm,p,r,observer,path)
	if other!=nil{_ = other.Close()}
	if !errors.Is(err,ErrInstanceRunning){t.Fatalf("expected exclusive ownership, got %v",err)}
	if err:=m.Close();err!=nil{t.Fatal(err)}
	if _,err:=m.Register(dir);!errors.Is(err,ErrManagerClosed){t.Fatalf("closed manager register: %v",err)}
	if err:=m.Start(context.Background(),def.ID);!errors.Is(err,ErrManagerClosed){t.Fatalf("closed manager start: %v",err)}
	other,err=NewPersistentManager(c,tm,p,r,observer,path)
	if err!=nil{t.Fatal(err)}
	defer other.Close()
	if got:=other.List();len(got)!=1||got[0].ProjectID!=def.ID{t.Fatalf("catalog not preserved: %+v",got)}
}

func TestPersistentPortConflictRejectsRegistryBeforeCreation(t *testing.T) {
	m,c,tm,p,_,_,dir,_:=persistentFixture(t)
	first,err:=m.Register(dir)
	if err!=nil{t.Fatal(err)}
	secondDir:=t.TempDir()
	if err:=os.WriteFile(filepath.Join(secondDir,"Tiltfile"),[]byte("# test\n"),0600);err!=nil{t.Fatal(err)}
	second,err:=m.Register(secondDir)
	if err!=nil{t.Fatal(err)}
	rec,err:=m.record(first.ID)
	if err!=nil{t.Fatal(err)}
	rec.TiltPort=p.values[0]
	if err:=m.store.upsert(rec);err!=nil{t.Fatal(err)}
	if err:=m.Start(context.Background(),second.ID);err==nil||!strings.Contains(err.Error(),"assigned to project"){t.Fatalf("expected reserved port conflict, got %v",err)}
	if c.exists||len(tm.configs)!=0{t.Fatal("conflict created external resources")}
}

func TestPersistentConcurrentRegistrationSurvivesReopen(t *testing.T) {
	m,c,tm,ports,registries,observer,_,path:=persistentFixture(t)
	const count=12
	dirs:=make([]string,count)
	for n:=range dirs{
		dirs[n]=t.TempDir()
		if err:=os.WriteFile(filepath.Join(dirs[n],"Tiltfile"),[]byte("# concurrent registration\n"),0600);err!=nil{t.Fatal(err)}
	}
	var wg sync.WaitGroup
	errs:=make(chan error,count*2)
	for _,dir:=range dirs{
		for n:=0;n<2;n++{
			wg.Add(1)
			go func(directory string){
				defer wg.Done()
				_,err:=m.Register(directory)
				errs<-err
			}(dir)
		}
	}
	wg.Wait()
	close(errs)
	for err:=range errs{if err!=nil{t.Fatal(err)}}
	if got:=len(m.List());got!=count{t.Fatalf("registered %d projects, want %d",got,count)}
	if err:=m.Close();err!=nil{t.Fatal(err)}
	reopened,err:=NewPersistentManager(c,tm,ports,registries,observer,path)
	if err!=nil{t.Fatal(err)}
	defer reopened.Close()
	if got:=len(reopened.List());got!=count{t.Fatalf("restored %d projects, want %d",got,count)}
}

func TestPersistentConstructorFailureReleasesOwnership(t *testing.T) {
	m,c,tm,ports,registries,observer,_,path:=persistentFixture(t)
	if err:=m.Close();err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(path,[]byte("invalid catalog"),0600);err!=nil{t.Fatal(err)}
	if err:=protectPrivateFile(path);err!=nil{t.Fatal(err)}
	failed,err:=NewPersistentManager(c,tm,ports,registries,observer,path)
	if failed!=nil{_ = failed.Close();t.Fatal("opened corrupt catalog")}
	if err==nil||errors.Is(err,ErrInstanceRunning){t.Fatalf("unexpected constructor error: %v",err)}
	if err:=os.Remove(path);err!=nil{t.Fatal(err)}
	recovered,err:=NewPersistentManager(c,tm,ports,registries,observer,path)
	if err!=nil{t.Fatalf("constructor leaked ownership: %v",err)}
	defer recovered.Close()
}
