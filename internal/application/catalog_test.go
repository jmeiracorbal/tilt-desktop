package application

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleCatalogRecord(t *testing.T) catalogRecord {
	t.Helper()
	root:=t.TempDir()
	return catalogRecord{ID:projectID(root),Directory:root,ClusterName:projectID(root),Phase:phaseIdle}
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
	if err:=json.Unmarshal(data,&doc);err!=nil||doc.Version!=1||len(doc.Records)!=1{t.Fatalf("catalog %s: %v",data,err)}
	info,err:=os.Stat(path)
	if err!=nil||info.Mode().Perm()!=0600{t.Fatalf("catalog permissions %v %v",info,err)}
	second,err:=openCatalog(path)
	if err!=nil{t.Fatal(err)}
	got:=second.all()
	if len(got)!=1||got[0]!=r{t.Fatalf("restored %+v, want %+v",got,r)}
	if err:=second.remove(r.ID);err!=nil{t.Fatal(err)}
	third,err:=openCatalog(path)
	if err!=nil||len(third.all())!=0{t.Fatalf("removal not durable: %+v %v",third,err)}
	entries,err:=os.ReadDir(filepath.Dir(path))
	if err!=nil{t.Fatal(err)}
	for _,item:=range entries{if strings.HasPrefix(item.Name(),".catalog-"){t.Fatalf("residual temporary catalog %q",item.Name())}}
}

func TestCatalogRejectsCorruptionAndUnsupportedSchema(t *testing.T){
	root:=t.TempDir()
	path:=filepath.Join(root,"catalog.json")
	for _,payload:=range []string{
		"invalid",
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
	if err:=os.Chmod(path,0644);err!=nil{t.Fatal(err)}
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
	if _,err:=store.remove("not-found");!errors.Is(err,ErrNotRegistered){t.Fatalf("unexpected missing entry error: %v",err)}
}
