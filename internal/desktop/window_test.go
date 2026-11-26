package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/jmeiracorbal/tilt-desktop/internal/application"
	"github.com/jmeiracorbal/tilt-desktop/internal/project"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

var _ Backend = (*application.PersistentManager)(nil)

type stubBackend struct { entries []application.State }

func (s *stubBackend) Register(string) (project.Definition,error) { return project.Definition{},nil }
func (s *stubBackend) RegisterShared(string,string)(project.Definition,error){return project.Definition{},nil}
func (s *stubBackend) ListSharedClusters()([]application.SharedClusterState,error){return nil,nil}
func (s *stubBackend) StopSharedCluster(context.Context,string)error{return nil}
func (s *stubBackend) DeleteSharedCluster(context.Context,string,bool)error{return nil}
func (s *stubBackend) List() []application.State {return append([]application.State(nil),s.entries...)}
func (s *stubBackend) Status(id string)(application.State,error){
	for _,entry:=range s.entries {if entry.ProjectID==id{return entry,nil}}
	return application.State{},errors.New("not registered")
}
func (s *stubBackend) ReconcileAll(context.Context)error{return nil}
func (s *stubBackend) Start(context.Context,string)error{return nil}
func (s *stubBackend) Stop(context.Context,string)error{return nil}
func (s *stubBackend) Delete(context.Context,string,bool)error{return nil}
func (s *stubBackend) Resources(context.Context,string)([]tilt.Resource,error){return nil,nil}
func (s *stubBackend) Logs(context.Context,string,string,int)([]byte,error){return nil,nil}

func TestHeadlessWindowBuildsWithPersistedProjects(t *testing.T) {
	app:=test.NewApp()
	defer app.Quit()
	m:=&stubBackend{entries:[]application.State{{
		ProjectID:"td-abc",Directory:"/tmp/my-app",ClusterName:"td-abc",Registered:true,Phase:"idle",
	}}}
	window,err:=NewWindow(app,m)
	if err!=nil {t.Fatal(err)}
	if window.Content()==nil {t.Fatal("window has no content")}
	if window.Title()!="Tilt Desktop" {t.Fatalf("window title: %s",window.Title())}
}

func TestWindowRequiresDependencies(t *testing.T) {
	if _,err:=NewWindow(nil,nil);err==nil{t.Fatal("accepted missing app and backend")}
	app:=test.NewApp()
	defer app.Quit()
	if _,err:=NewWindow(app,nil);err==nil{t.Fatal("accepted missing backend")}
}

func TestActionsFailClosedForUnverifiedSessions(t *testing.T){
	for _,phase:=range []string{"orphaned","unknown","starting"} {
		state:=application.State{Phase:phase,Running:true}
		if canStart(state)||canStop(state)||canDelete(state) {
			t.Fatalf("unsafe %s session can be modified",phase)
		}
	}
	idle:=application.State{Phase:"idle"}
	if !canStart(idle)||canStop(idle)||!canDelete(idle){t.Fatal("invalid actions for stopped application")}
	active:=application.State{Phase:"running",Running:true}
	if canStart(active)||!canStop(active)||!canDelete(active){t.Fatal("invalid actions for active application")}
}

func TestProjectAndResourceFormatting(t *testing.T){
	s:=formatProject(application.State{ProjectID:"td-abc",Directory:"/projects/example",Phase:"running"})
	if !strings.Contains(s,"example")||!strings.Contains(s,"running"){t.Fatalf("unexpected project label %q",s)}
	resource:=formatResource(tilt.Resource{Name:"api",RuntimeStatus:"ok",UpdateStatus:"pending"})
	for _,want:=range []string{"api","ok","pending"} {
		if !strings.Contains(resource,want){t.Fatalf("resource format %q missing %q",resource,want)}
	}
}

func TestSharedClusterActionAvailability(t *testing.T) {
	for _,tc:=range []struct{
		name string
		state application.SharedClusterState
		stop,remove bool
	}{
		{"empty",application.SharedClusterState{},true,true},
		{"idle members",application.SharedClusterState{Projects:2},true,false},
		{"unresolved member",application.SharedClusterState{Projects:2,Unresolved:1},false,false},
	}{
		t.Run(tc.name,func(t *testing.T){
			if canStopShared(tc.state)!=tc.stop||canDeleteShared(tc.state)!=tc.remove{
				t.Fatalf("unexpected actions for %+v",tc.state)
			}
		})
	}
}

func TestProjectDeletionPromptDistinguishesClusterOwnership(t *testing.T) {
	state:=application.State{Directory:"/projects/example",ClusterMode:project.Shared}
	title,message:=projectDeletionPrompt(state)
	if title!="Remove shared project"||!strings.Contains(message,"retained")||strings.Contains(message,"permanently removes"){
		t.Fatalf("unsafe shared deletion prompt: %s %s",title,message)
	}
	state.ClusterMode=project.Dedicated
	title,message=projectDeletionPrompt(state)
	if title!="Delete project and cluster"||!strings.Contains(message,"permanently removes the cluster"){
		t.Fatalf("missing dedicated deletion warning: %s %s",title,message)
	}
}
