package application

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/project"
)

func sampleCatalogRecord(t *testing.T) catalogEntry {
	t.Helper()
	root:=t.TempDir()
	return catalogEntry{catalogRecord:catalogRecord{ID:projectID(root),Directory:root,ClusterName:projectID(root),Phase:phaseIdle},clusterRecord:clusterRecord{Name:projectID(root),Mode:project.Dedicated}}
}

func TestCatalogSurvivesReopen(t *testing.T){
	r:=sampleCatalogRecord(t)
	path:=filepath.Join(t.TempDir(),"private","catalog.json")
	store,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	if err:=store.upsert(r);err!=nil{t.Fatal(err)}
	data,err:=os.ReadFile(path)
	if err!=nil{t.Fatal(err)}
	if strings.Contains(string(data),"apiVersion: v1") {t.Fatal("catalog leaked kubeconfig contents")}
	var doc catalogFile
	if err:=json.Unmarshal(data,&doc);err!=nil||doc.Version!=catalogVersion||len(doc.Projects)!=1||len(doc.Clusters)!=1{t.Fatalf("catalog %s: %v",data,err)}
	info,err:=os.Stat(path)
	if err!=nil{t.Fatal(err)}
	if err:=requirePrivateFile(path,info);err!=nil{t.Fatalf("catalog is not private: %v",err)}
	second,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	got:=second.all()
	if len(got)!=1||got[0]!=r{t.Fatalf("restored %+v, want %+v",got,r)}
	if err:=second.remove(r.ID);err!=nil{t.Fatal(err)}
	third,err:=openCatalog(path)
	if err!=nil||len(third.all())!=0||len(third.clusters)!=0{t.Fatalf("removal not durable: %+v %v",third,err)}
	entries,err:=os.ReadDir(filepath.Dir(path))
	if err!=nil{t.Fatal(err)}
	for _,item:=range entries{if strings.HasPrefix(item.Name(),".catalog-"){t.Fatalf("residual temporary catalog %q",item.Name())}}
}

func TestCatalogRejectsCorruptionAndUnsupportedSchema(t *testing.T){
	root:=t.TempDir()
	path:=filepath.Join(root,"catalog.json")
	for _,payload:=range []string{
		"invalid",
		`{"version":3,"projects":[],"clusters":[]}`,
		`{"version":2,"records":[]}`,
		`{"version":1,"records":[{"id":"unknown","directory":"/x","clusterName":"unknown","phase":"idle"}]}`,
		`{"version":1,"records":[],"unexpected":1}`,
		`{"version":1,"records":[]} trailing`,
	}{
		if err:=os.WriteFile(path,[]byte(payload),0600);err!=nil{t.Fatal(err)}
		if _,err:=openCatalog(path);err==nil{t.Fatalf("accepted %q",payload)}
	}
}

func TestCatalogRejectsWorldReadableFileAndSymlink(t *testing.T){
	root:=t.TempDir()
	path:=filepath.Join(root,"catalog.json")
	if err:=os.WriteFile(path,[]byte(`{"version":1,"records":[]}`),0644);err!=nil{t.Fatal(err)}
	if err:=makeInsecureFileForTest(path);err!=nil{t.Fatal(err)}
	if _,err:=openCatalog(path);err==nil{t.Fatal("accepted world-readable catalog")}
	if err:=os.Remove(path);err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(path+".target",[]byte(`{"version":1,"records":[]}`),0600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(path+".target",path);err!=nil{t.Skipf("symlinks unavailable: %v",err)}
	if _,err:=openCatalog(path);err==nil{t.Fatal("accepted symlinked catalog")}
}

func TestCatalogValidation(t *testing.T){
	r:=sampleCatalogRecord(t)
	store,err:=openCatalog(filepath.Join(t.TempDir(),"catalog.json"))
	if err!=nil{t.Fatal(err)}
	if err:=store.upsert(r);err!=nil{t.Fatal(err)}
	r.ID="not-derived"
	if err:=store.upsert(r);err==nil{t.Fatal("accepted changed identity")}
	r=sampleCatalogRecord(t)
	r.Phase=phaseRunning
	if err:=store.upsert(r);err==nil{t.Fatal("accepted running state without process metadata")}
	if err:=store.remove("not-found");!errors.Is(err,ErrNotRegistered){t.Fatalf("unexpected missing entry error: %v",err)}
}

func TestCatalogRejectsDuplicatePortAssignments(t *testing.T) {
	first:=sampleCatalogRecord(t)
	second:=sampleCatalogRecord(t)
	store,err:=openCatalog(filepath.Join(t.TempDir(),"catalog.json"))
	if err!=nil{t.Fatal(err)}
	first.TiltPort=10351
	first.RegistryPort=5111
	if err:=store.upsert(first);err!=nil{t.Fatal(err)}
	second.RegistryPort=first.TiltPort
	if err:=store.upsert(second);err==nil{t.Fatal("accepted cross-role port collision")}
	second.RegistryPort=10352
	second.TiltPort=10352
	if err:=store.upsert(second);err==nil{t.Fatal("accepted same-project port collision")}
	second.TiltPort=10353
	if err:=store.upsert(second);err!=nil{t.Fatal(err)}
	if err:=store.remove(first.ID);err!=nil{t.Fatal(err)}
	second.RegistryPort=first.TiltPort
	if err:=store.upsert(second);err!=nil{t.Fatalf("deleted assignment not released: %v",err)}
}

func TestCatalogRejectsPersistedPortCollision(t *testing.T) {
	first:=sampleCatalogRecord(t)
	second:=sampleCatalogRecord(t)
	first.TiltPort=10351
	second.RegistryPort=10351
	path:=filepath.Join(t.TempDir(),"catalog.json")
	payload,err:=json.Marshal(catalogFile{Version:catalogVersion,Projects:[]catalogRecord{first.catalogRecord,second.catalogRecord},Clusters:[]clusterRecord{first.clusterRecord,second.clusterRecord}})
	if err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(path,payload,0600);err!=nil{t.Fatal(err)}
	if err:=protectPrivateFile(path);err!=nil{t.Fatal(err)}
	if _,err:=openCatalog(path);err==nil||!strings.Contains(err.Error(),"assigned to project"){t.Fatalf("expected persisted port conflict, got %v",err)}
}

func TestCatalogSharedClusterPersistsOnce(t *testing.T) {
	path:=filepath.Join(t.TempDir(),"catalog.json")
	store,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	first,second:=sampleCatalogRecord(t),sampleCatalogRecord(t)
	shared:=clusterRecord{Name:"td-shared-development",Mode:project.Shared,RegistryPort:5111}
	first.ClusterName,first.clusterRecord,first.TiltPort=shared.Name,shared,10351
	second.ClusterName,second.clusterRecord,second.TiltPort=shared.Name,shared,10352
	if err:=store.upsert(first);err!=nil{t.Fatal(err)}
	if err:=store.upsert(second);err!=nil{t.Fatal(err)}
	reopened,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	if len(reopened.clusters)!=1||len(reopened.records)!=2{t.Fatalf("unexpected catalog: %+v",reopened.snapshot())}
	for _,rec:=range reopened.all(){
		if rec.clusterRecord!=shared{t.Fatalf("unexpected shared registry: %+v",rec)}
	}
	if err:=reopened.checkRegistryPort(shared.Name,5111);err!=nil{t.Fatal(err)}
	if err:=reopened.checkTiltPort(second.ID,5111);err==nil{t.Fatal("Tilt reused registry port")}
	if err:=reopened.checkTiltPort(second.ID,first.TiltPort);err==nil{t.Fatal("Tilt reused another project's port")}
	second.TiltPort=first.TiltPort
	if err:=reopened.upsert(second);err==nil{t.Fatal("duplicate Tilt port accepted")}
	second.TiltPort=10352
	second.RegistryPort=5112
	if err:=reopened.upsert(second);err==nil{t.Fatal("shared registry changed by member")}
	if err:=reopened.remove(first.ID);err!=nil{t.Fatal(err)}
	if err:=reopened.remove(second.ID);err!=nil{t.Fatal(err)}
	final,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	if len(final.records)!=0||final.clusters[shared.Name]!=shared{t.Fatalf("project removal deleted shared cluster: %+v",final.snapshot())}
}

func TestCatalogRejectsDanglingAndForeignDedicatedReferences(t *testing.T) {
	first,second:=sampleCatalogRecord(t),sampleCatalogRecord(t)
	records:=map[string]catalogRecord{first.ID:first.catalogRecord}
	clusters:=map[string]clusterRecord{}
	if err:=validateCatalog(records,clusters);err==nil{t.Fatal("accepted missing cluster")}
	clusters[first.Name]=first.clusterRecord
	second.ClusterName=first.Name
	records[second.ID]=second.catalogRecord
	if err:=validateCatalog(records,clusters);err==nil{t.Fatal("accepted second project on dedicated cluster")}
}

func TestCatalogMigratesV1PreservingSession(t *testing.T) {
	entry:=sampleCatalogRecord(t)
	entry.RegistryPort=5111
	entry.TiltPort=10351
	entry.Phase=phaseRunning
	entry.PID=1234
	entry.Kubeconfig=filepath.Join(t.TempDir(),"tilt-desktop-kubeconfig-session")
	path:=filepath.Join(t.TempDir(),"catalog.json")
	legacy:=map[string]any{"version":1,"records":[]any{map[string]any{
		"id":entry.ID,"directory":entry.Directory,"clusterName":entry.ClusterName,
		"registryPort":entry.RegistryPort,"tiltPort":entry.TiltPort,"phase":entry.Phase,
		"pid":entry.PID,"kubeconfig":entry.Kubeconfig,
	}}}
	payload,err:=json.Marshal(legacy)
	if err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(path,payload,0600);err!=nil{t.Fatal(err)}
	if err:=protectPrivateFile(path);err!=nil{t.Fatal(err)}
	migrated,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	if all:=migrated.all();len(all)!=1||all[0]!=entry{t.Fatalf("migration lost metadata: %+v",all)}
	data,err:=os.ReadFile(path)
	if err!=nil{t.Fatal(err)}
	var doc catalogFile
	if err:=decodeStrict(data,&doc);err!=nil{t.Fatal(err)}
	if doc.Version!=catalogVersion||len(doc.Projects)!=1||len(doc.Clusters)!=1{t.Fatalf("invalid migrated catalog: %+v",doc)}
	reopened,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	if all:=reopened.all();len(all)!=1||all[0]!=entry{t.Fatalf("reopen lost metadata: %+v",all)}
}

func TestCatalogInvalidV1IsNotRewritten(t *testing.T) {
	entry:=sampleCatalogRecord(t)
	payload,err:=json.Marshal(map[string]any{"version":1,"records":[]any{
		map[string]any{"id":entry.ID,"directory":entry.Directory,"clusterName":"wrong","phase":"idle"},
	}})
	if err!=nil{t.Fatal(err)}
	path:=filepath.Join(t.TempDir(),"catalog.json")
	if err:=os.WriteFile(path,payload,0600);err!=nil{t.Fatal(err)}
	if err:=protectPrivateFile(path);err!=nil{t.Fatal(err)}
	if _,err:=openCatalog(path);err==nil{t.Fatal("accepted invalid v1")}
	got,err:=os.ReadFile(path)
	if err!=nil||string(got)!=string(payload){t.Fatalf("invalid v1 was rewritten: %s %v",got,err)}
}
