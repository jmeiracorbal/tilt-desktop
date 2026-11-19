package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const catalogVersion = 1

type phase string

const (
	phaseIdle     phase = "idle"
	phaseStarting phase = "starting"
	phaseRunning  phase = "running"
	phaseOrphaned phase = "orphaned"
	phaseUnknown  phase = "unknown"
)

type catalogRecord struct {
	ID           string `json:"id"`
	Directory    string `json:"directory"`
	ClusterName  string `json:"clusterName"`
	RegistryPort uint16 `json:"registryPort,omitempty"`
	TiltPort     uint16 `json:"tiltPort,omitempty"`
	Kubeconfig   string `json:"kubeconfig,omitempty"`
	Phase        phase  `json:"phase"`
}

type catalogFile struct {
	Version int             `json:"version"`
	Records []catalogRecord `json:"records"`
}

// catalogStore persists only application metadata, never kubeconfig contents.
// All writes are complete replacements of a versioned document.
type catalogStore struct {
	mu      sync.Mutex
	path    string
	records map[string]catalogRecord
}

func openCatalog(path string) (*catalogStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute catalog path is required")
	}
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create catalog directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect catalog: %w", err)
	}
	store := &catalogStore{path:path, records:make(map[string]catalogRecord)}
	if errors.Is(err, os.ErrNotExist) {
		return store,nil
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("catalog %q must be a regular file",path)
	}
	if info.Mode().Perm() & 0077 != 0 {
		return nil, fmt.Errorf("catalog %q permits access by other users",path)
	}
	payload,err:=os.ReadFile(path)
	if err!=nil{return nil,fmt.Errorf("read catalog: %w",err)}
	var file catalogFile
	reader:=json.NewDecoder(bytes.NewReader(payload))
	reader.DisallowUnknownFields()
	if err:=reader.Decode(&file);err!=nil{return nil,fmt.Errorf("decode catalog: %w",err)}
	var trailing any
	if err:=reader.Decode(&trailing);!errors.Is(err,io.EOF){
		return nil,errors.New("catalog contains trailing content")
	}
	if file.Version!=catalogVersion{return nil,fmt.Errorf("unsupported catalog version %d",file.Version)}
	for _,record:=range file.Records{
		if err:=validateRecord(record);err!=nil{return nil,err}
		if _,exists:=store.records[record.ID];exists{return nil,fmt.Errorf("duplicate catalog entry %q",record.ID)}
		store.records[record.ID]=record
	}
	return store,nil
}

func validateRecord(record catalogRecord) error {
	if record.ID==""||record.ClusterName!=record.ID||!filepath.IsAbs(record.Directory) {
		return fmt.Errorf("invalid catalog entry %q",record.ID)
	}
	if _,err:=definitionFor(record.ID,record.Directory);err!=nil {
		// The application folder can disappear after registration. Its
		// identity is still checked without requiring the Tiltfile to exist.
		if !strings.HasPrefix(record.ID,"td-")||len(record.ID)!=23 {
			return fmt.Errorf("invalid catalog identity %q",record.ID)
		}
	}
	if record.Phase!=phaseIdle&&record.Phase!=phaseStarting&&record.Phase!=phaseRunning&&record.Phase!=phaseOrphaned&&record.Phase!=phaseUnknown {
		return fmt.Errorf("invalid catalog phase %q",record.Phase)
	}
	if record.Phase!=phaseIdle && (record.TiltPort==0||record.Kubeconfig=="") {
		return fmt.Errorf("incomplete session metadata for %q",record.ID)
	}
	if record.Kubeconfig!=""&&!filepath.IsAbs(record.Kubeconfig){
		return fmt.Errorf("relative kubeconfig path in record %q",record.ID)
	}
	return nil
}

func (s *catalogStore) all() []catalogRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *catalogStore) snapshot() []catalogRecord {
	result:=make([]catalogRecord,0,len(s.records))
	for _,v:=range s.records{result=append(result,v)}
	sort.Slice(result,func(i,j int)bool{return result[i].ID<result[j].ID})
	return result
}

func (s *catalogStore) upsert(record catalogRecord) error {
	if err:=validateRecord(record);err!=nil{return err}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous,exists:=s.records[record.ID]
	s.records[record.ID]=record
	if err:=s.flush();err!=nil{
		if exists{s.records[record.ID]=previous}else{delete(s.records,record.ID)}
		return err
	}
	return nil
}

func (s *catalogStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous,exists:=s.records[id]
	if !exists{return fmt.Errorf("%w: %s",ErrNotRegistered,id)}
	delete(s.records,id)
	if err:=s.flush();err!=nil{s.records[id]=previous;return err}
	return nil
}

func (s *catalogStore) flush() error {
	doc:=catalogFile{Version:catalogVersion,Records:s.snapshot()}
	payload,err:=json.MarshalIndent(doc,"","  ")
	if err!=nil{return fmt.Errorf("encode catalog: %w",err)}
	payload=append(payload,'\n')
	// Use a same-directory atomic rename. Incomplete writes never replace the
	// last valid catalog; the temporary file is cleaned up on every error.
	temp,err:=os.CreateTemp(filepath.Dir(s.path),".catalog-*")
	if err!=nil{return fmt.Errorf("prepare catalog replacement: %w",err)}
	defer os.Remove(temp.Name())
	if err:=temp.Chmod(0600);err!=nil{temp.Close();return err}
	if _,err:=temp.Write(payload);err!=nil{temp.Close();return fmt.Errorf("write catalog: %w",err)}
	if err:=temp.Sync();err!=nil{temp.Close();return fmt.Errorf("sync catalog: %w",err)}
	if err:=temp.Close();err!=nil{return fmt.Errorf("close catalog: %w",err)}
	if err:=os.Rename(temp.Name(),s.path);err!=nil{return fmt.Errorf("replace catalog: %w",err)}
	return nil
}
