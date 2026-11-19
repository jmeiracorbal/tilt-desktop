package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/k3d"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

type trace struct{mu sync.Mutex; calls []string}
func (t *trace) add(s string){t.mu.Lock();defer t.mu.Unlock();t.calls=append(t.calls,s)}
func (t *trace) all() []string {t.mu.Lock();defer t.mu.Unlock();return append([]string(nil),t.calls...)}

type fakeCluster struct{
	mu sync.Mutex
	trace *trace
	exists bool
	spec k3d.Spec
	ensureErr,kubeErr,stopErr,deleteErr error
}
func (f *fakeCluster)List(context.Context)([]k3d.Cluster,error){f.trace.add("cluster.list");f.mu.Lock();defer f.mu.Unlock();if f.exists{return []k3d.Cluster{{Name:f.spec.Name}},nil};return nil,nil}
func (f *fakeCluster)Ensure(_ context.Context,s k3d.Spec)error{f.trace.add("cluster.ensure");f.mu.Lock();defer f.mu.Unlock();if f.ensureErr!=nil{return f.ensureErr};f.exists=true;f.spec=s;return nil}
func (f *fakeCluster)Kubeconfig(_ context.Context,_ k3d.Spec)([]byte,error){f.trace.add("cluster.kubeconfig");if f.kubeErr!=nil{return nil,f.kubeErr};return []byte("apiVersion: v1\n# private\n"),nil}
func (f *fakeCluster)Stop(_ context.Context,_ k3d.Spec)error{f.trace.add("cluster.stop");return f.stopErr}
func (f *fakeCluster)Delete(_ context.Context,_ k3d.Spec)error{f.trace.add("cluster.delete");f.mu.Lock();defer f.mu.Unlock();if f.deleteErr!=nil{return f.deleteErr};f.exists=false;return nil}

type fakeTilt struct{
	mu sync.Mutex
	trace *trace
	configs []tilt.Config
	running map[string]bool
	startErr,stopErr error
}
func (f *fakeTilt)Start(_ context.Context,c tilt.Config)error{f.trace.add("tilt.start");if f.startErr!=nil{return f.startErr};f.mu.Lock();defer f.mu.Unlock();f.configs=append(f.configs,c);f.running[c.ProjectID]=true;return nil}
func (f *fakeTilt)Stop(_ context.Context,id string)error{f.trace.add("tilt.stop");if f.stopErr!=nil{return f.stopErr};f.mu.Lock();defer f.mu.Unlock();f.running[id]=false;return nil}
func (f *fakeTilt)Status(id string)(tilt.State,error){f.mu.Lock();defer f.mu.Unlock();if f.running[id] {return tilt.State{Running:true,PID:os.Getpid()},nil};return tilt.State{Running:false},nil}
func (f *fakeTilt)Resources(_ context.Context,_ string)([]tilt.Resource,error){return []tilt.Resource{{Name:"api",RuntimeStatus:"ok"}},nil}
func (f *fakeTilt)Logs(_ context.Context,_,_ string,_ int)([]byte,error){return []byte("started\n"),nil}

type fakePorts struct{mu sync.Mutex; values []uint16; index int; err error}
func (p *fakePorts)Available()(uint16,error){p.mu.Lock();defer p.mu.Unlock();if p.err!=nil{return 0,p.err};if p.index>=len(p.values){return 0,errors.New("no more ports")};v:=p.values[p.index];p.index++;return v,nil}
type fakeRegistries struct {port uint16; err error; calls int}
func (r *fakeRegistries)RegistryPort(_ context.Context,_ string)(uint16,error){r.calls++;return r.port,r.err}

func setup(t *testing.T)(*Manager,*fakeCluster,*fakeTilt,*fakePorts,*fakeRegistries,string){
	t.Helper();t.Setenv("TMPDIR",t.TempDir())
	e:=&trace{}
	c:=&fakeCluster{trace:e}
	tt:=&fakeTilt{trace:e,running:map[string]bool{}}
	p:=&fakePorts{values:[]uint16{5111,10351,10352,10353}}
	reg:=&fakeRegistries{port:5111}
	m,err:=NewManager(c,tt,p,reg);if err!=nil{t.Fatal(err)}
	dir:=t.TempDir()
	if err:=os.WriteFile(filepath.Join(dir,"Tiltfile"),[]byte("# test\n"),0600);err!=nil{t.Fatal(err)}
	return m,c,tt,p,reg,dir
}

func TestRegisterStableAndValidated(t *testing.T){
	m,_,_,_,_,dir:=setup(t)
	first,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	second,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	if first!=second{t.Fatalf("registration changed: %+v %+v",first,second)}
	if first.Cluster.Name!=first.ID||first.Cluster.Mode!="dedicated"||!strings.HasPrefix(first.ID,"td-"){t.Fatalf("unexpected cluster: %+v",first)}
	if _,err:=m.Register(filepath.Join(dir,"missing"));err==nil{t.Fatal("accepted missing folder")}
	if _,err:=m.Register(t.TempDir());err==nil{t.Fatal("accepted missing Tiltfile")}
	if _,err:=m.Status("missing");!errors.Is(err,ErrNotRegistered){t.Fatalf("status: %v",err)}
}

func TestStartStopDeleteAndCredentialIsolation(t *testing.T){
	m,c,tt,p,_,dir:=setup(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	state,err:=m.Status(def.ID);if err!=nil||state.Running||!state.Registered{t.Fatalf("registered: %+v %v",state,err)}
	ctx:=context.Background()
	if err:=m.Start(ctx,def.ID);err!=nil{t.Fatal(err)}
	if err:=m.Start(ctx,def.ID);err!=nil{t.Fatal(err)}
	state,err=m.Status(def.ID)
	if err!=nil||!state.Running||state.TiltPort!=10351||state.RegistryPort!=5111{t.Fatalf("running: %+v %v",state,err)}
	if p.index!=2||len(tt.configs)!=1{t.Fatalf("duplicate start: ports=%d configs=%d",p.index,len(tt.configs))}
	if tt.configs[0].Directory!=def.Directory||tt.configs[0].ClusterName!=def.Cluster.Name{t.Fatalf("Tilt config %+v",tt.configs[0])}
	path:=tt.configs[0].Kubeconfig
	stat,err:=os.Stat(path);if err!=nil{t.Fatal(err)}
	if stat.Mode().Perm()!=0600{t.Fatalf("kubeconfig permissions: %o",stat.Mode().Perm())}
	data,err:=os.ReadFile(path);if err!=nil||!strings.Contains(string(data),"private"){t.Fatalf("kubeconfig read: %v",err)}
	res,err:=m.Resources(ctx,def.ID)
	if err!=nil||!reflect.DeepEqual(res,[]tilt.Resource{{Name:"api",RuntimeStatus:"ok"}}){t.Fatalf("resources: %+v %v",res,err)}
	logs,err:=m.Logs(ctx,def.ID,"",20);if err!=nil||string(logs)!="started\n"{t.Fatalf("logs: %q %v",logs,err)}
	if err:=m.Stop(ctx,def.ID);err!=nil{t.Fatal(err)}
	if err:=m.Stop(ctx,def.ID);err!=nil{t.Fatal(err)}
	if _,err:=os.Stat(path);!errors.Is(err,os.ErrNotExist){t.Fatalf("credentials not removed: %v",err)}
	if !c.exists{t.Fatal("Stop removed the cluster")}
	if err:=m.Delete(ctx,def.ID,false);!errors.Is(err,ErrConfirmationRequired){t.Fatalf("confirmation: %v",err)}
	if err:=m.Delete(ctx,def.ID,true);err!=nil{t.Fatal(err)}
	if c.exists{t.Fatal("cluster not deleted")}
	if _,err:=m.Status(def.ID);!errors.Is(err,ErrNotRegistered){t.Fatalf("deleted status: %v",err)}
	want:=[]string{"cluster.list","cluster.ensure","cluster.kubeconfig","tilt.start","tilt.stop","cluster.stop","cluster.list","cluster.delete"}
	if got:=c.trace.all();!reflect.DeepEqual(got,want){t.Fatalf("order got %v want %v",got,want)}
}

func TestExistingClusterRegistryDiscoveryAndRestart(t *testing.T){
	m,c,tt,p,reg,dir:=setup(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	c.exists=true;c.spec=k3d.Spec{Name:def.ID,ManagementID:def.ID,RegistryPort:6123};reg.port=6123
	if err:=m.Start(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	if c.spec.RegistryPort!=6123||p.index!=1||reg.calls!=1{t.Fatalf("registry was changed: %+v, %d, %d",c.spec,p.index,reg.calls)}
	if err:=m.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
	other,err:=NewManager(c,tt,p,reg);if err!=nil{t.Fatal(err)}
	second,err:=other.Register(dir);if err!=nil||second.ID!=def.ID{t.Fatalf("identity mismatch: %+v %v",second,err)}
	if err:=other.Start(context.Background(),second.ID);err!=nil{t.Fatal(err)}
	if c.spec.RegistryPort!=6123||p.index!=2{t.Fatalf("registry rediscovered incorrectly: %+v index %d",c.spec,p.index)}
	if err:=other.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
}

func TestStartFailureDoesNotDeleteExistingCluster(t *testing.T){
	for _,existing:=range []bool{false,true}{
		t.Run(map[bool]string{false:"new",true:"existing"}[existing],func(t *testing.T){
			m,c,tt,_,reg,dir:=setup(t)
			def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
			c.exists=existing;c.spec=k3d.Spec{Name:def.ID,ManagementID:def.ID,RegistryPort:5111};reg.port=5111
			tt.startErr=errors.New("cannot launch Tilt")
			err=m.Start(context.Background(),def.ID)
			if err==nil||!strings.Contains(err.Error(),"cannot launch Tilt"){t.Fatalf("missing start error: %v",err)}
			if c.exists!=existing{t.Fatalf("cluster existence: %t, want %t",c.exists,existing)}
			items,err:=os.ReadDir(os.TempDir());if err!=nil{t.Fatal(err)}
			for _,item:=range items{if strings.HasPrefix(item.Name(),"tilt-desktop-kubeconfig-"){t.Fatalf("leaked kubeconfig %s",item.Name())}}
		})
	}
}

func TestRollbackReportsCleanupErrors(t *testing.T){
	m,c,_,_,_,dir:=setup(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	c.kubeErr=errors.New("cannot fetch credentials")
	c.deleteErr=errors.New("cleanup failed")
	err=m.Start(context.Background(),def.ID)
	if err==nil||!strings.Contains(err.Error(),"cannot fetch credentials")||!strings.Contains(err.Error(),"cleanup failed"){t.Fatalf("combined failure: %v",err)}
}

func TestDeleteUnstartedDoesNotAllocatePort(t *testing.T){
	m,c,_,p,_,dir:=setup(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	if err:=m.Delete(context.Background(),def.ID,false);!errors.Is(err,ErrConfirmationRequired){t.Fatal(err)}
	if err:=m.Delete(context.Background(),def.ID,true);err!=nil{t.Fatal(err)}
	if p.index!=0||c.exists{t.Fatal("unstarted project created resources")}
}

func TestConcurrentStartIsIdempotent(t *testing.T){
	m,_,tt,_,_,dir:=setup(t)
	def,err:=m.Register(dir);if err!=nil{t.Fatal(err)}
	var wg sync.WaitGroup
	errs:=make(chan error,12)
	for i:=0;i<12;i++{wg.Add(1);go func(){defer wg.Done();errs<-m.Start(context.Background(),def.ID)}()}
	wg.Wait();close(errs)
	for err:=range errs{if err!=nil{t.Fatal(err)}}
	if len(tt.configs)!=1{t.Fatalf("started %d processes",len(tt.configs))}
	if err:=m.Stop(context.Background(),def.ID);err!=nil{t.Fatal(err)}
}

func TestDependenciesAndNetPorts(t *testing.T){
	if _,err:=NewManager(nil,nil,nil,nil);err==nil{t.Fatal("accepted nil dependencies")}
	port,err:=(NetPorts{}).Available()
	if err!=nil||port==0{t.Fatalf("port=%d error=%v",port,err)}
}


func TestDifferentFoldersGetIndependentClusterNamesAndPorts(t *testing.T) {
	m,c,tiltManager,ports,_,firstDir:=setup(t)
	secondDir:=t.TempDir()
	if err:=os.WriteFile(filepath.Join(secondDir,"Tiltfile"),[]byte("# second\n"),0600);err!=nil{t.Fatal(err)}
	first,err:=m.Register(firstDir);if err!=nil{t.Fatal(err)}
	second,err:=m.Register(secondDir);if err!=nil{t.Fatal(err)}
	if first.ID==second.ID || first.Cluster.Name==second.Cluster.Name{t.Fatal("two project folders received the same dedicated cluster")}
	if err:=m.Start(context.Background(),first.ID);err!=nil{t.Fatal(err)}
	if err:=m.Start(context.Background(),second.ID);err!=nil{t.Fatal(err)}
	if len(tiltManager.configs)!=2{t.Fatalf("expected two Tilt processes, got %d",len(tiltManager.configs))}
	one,two:=tiltManager.configs[0],tiltManager.configs[1]
	if one.ClusterName==two.ClusterName || one.Port==two.Port || one.Kubeconfig==two.Kubeconfig{
		t.Fatalf("project session isolation failed: %+v %+v",one,two)
	}
	if ports.index!=4 || c.spec.Name!=second.Cluster.Name{t.Fatalf("unexpected cluster or port assignment: %+v, index=%d",c.spec,ports.index)}
	if err:=m.Stop(context.Background(),first.ID);err!=nil{t.Fatal(err)}
	if err:=m.Stop(context.Background(),second.ID);err!=nil{t.Fatal(err)}
}
