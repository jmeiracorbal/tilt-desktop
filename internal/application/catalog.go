package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/jmeiracorbal/tilt-desktop/internal/project"
)

const catalogVersion = 2

type phase string

const (
	phaseIdle     phase = "idle"
	phaseStarting phase = "starting"
	phaseRunning  phase = "running"
	phaseOrphaned phase = "orphaned"
	phaseUnknown  phase = "unknown"
)

type catalogRecord struct {
	ID          string `json:"id"`
	Directory   string `json:"directory"`
	ClusterName string `json:"clusterName"`
	TiltPort    uint16 `json:"tiltPort,omitempty"`
	Kubeconfig  string `json:"kubeconfig,omitempty"`
	Phase       phase  `json:"phase"`
	PID         int64  `json:"pid,omitempty"`
}

type clusterRecord struct {
	Name         string              `json:"name"`
	Mode         project.ClusterMode `json:"mode"`
	RegistryPort uint16              `json:"registryPort,omitempty"`
}

// catalogEntry is a joined view, not a second persisted representation.
type catalogEntry struct {
	catalogRecord
	clusterRecord
}

type catalogFile struct {
	Version  int             `json:"version"`
	Projects []catalogRecord `json:"projects"`
	Clusters []clusterRecord `json:"clusters"`
}

// catalogStore persists only metadata. Each project references exactly one
// cluster; registry ports belong to clusters, and Tilt ports to projects.
type catalogStore struct {
	mu       sync.Mutex
	path     string
	records  map[string]catalogRecord
	clusters map[string]clusterRecord
}

func openCatalog(path string) (*catalogStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute catalog path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create catalog directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect catalog: %w", err)
	}
	store := &catalogStore{
		path: path, records: make(map[string]catalogRecord),
		clusters: make(map[string]clusterRecord),
	}
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("catalog %q must be a regular file", path)
	}
	if err := requirePrivateFile(path, info); err != nil {
		return nil, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	file, migrated, err := decodeCatalog(payload)
	if err != nil {
		return nil, err
	}
	for _, rec := range file.Projects {
		if _, exists := store.records[rec.ID]; exists {
			return nil, fmt.Errorf("duplicate catalog entry %q", rec.ID)
		}
		store.records[rec.ID] = rec
	}
	for _, cluster := range file.Clusters {
		if _, exists := store.clusters[cluster.Name]; exists {
			return nil, fmt.Errorf("duplicate cluster %q", cluster.Name)
		}
		store.clusters[cluster.Name] = cluster
	}
	if err := validateCatalog(store.records, store.clusters); err != nil {
		return nil, err
	}
	if migrated {
		if err := store.flush(); err != nil {
			return nil, fmt.Errorf("migrate catalog: %w", err)
		}
	}
	return store, nil
}

func decodeStrict(payload []byte, target any) error {
	reader := json.NewDecoder(bytes.NewReader(payload))
	reader.DisallowUnknownFields()
	if err := reader.Decode(target); err != nil {
		return fmt.Errorf("decode catalog: %w", err)
	}
	var trailing any
	if err := reader.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("catalog contains trailing content")
	}
	return nil
}

var clusterNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validateRecord(record catalogRecord) error {
	if record.ID == "" || !filepath.IsAbs(record.Directory) {
		return fmt.Errorf("invalid catalog entry %q", record.ID)
	}
	if filepath.Clean(record.Directory) != record.Directory || projectID(record.Directory) != record.ID {
		return fmt.Errorf("invalid catalog identity %q", record.ID)
	}
	switch record.Phase {
	case phaseIdle, phaseStarting, phaseRunning, phaseOrphaned, phaseUnknown:
	default:
		return fmt.Errorf("invalid catalog phase %q", record.Phase)
	}
	if record.Phase != phaseIdle && (record.TiltPort == 0 || record.Kubeconfig == "") {
		return fmt.Errorf("incomplete session metadata for %q", record.ID)
	}
	if record.Kubeconfig != "" && !filepath.IsAbs(record.Kubeconfig) {
		return fmt.Errorf("relative kubeconfig path in record %q", record.ID)
	}
	return nil
}

func validateCatalog(records map[string]catalogRecord, clusters map[string]clusterRecord) error {
	owners := make(map[uint16]string)
	references := make(map[string]int)
	reserve := func(port uint16, owner string) error {
		if port == 0 {
			return nil
		}
		if previous, exists := owners[port]; exists {
			return fmt.Errorf("port %d is assigned to %s and %s", port, previous, owner)
		}
		owners[port] = owner
		return nil
	}
	for _, cluster := range clusters {
		if !clusterNamePattern.MatchString(cluster.Name) {
			return fmt.Errorf("invalid cluster name %q", cluster.Name)
		}
		switch cluster.Mode {
		case project.Dedicated:
		case project.Shared:
			if !strings.HasPrefix(cluster.Name, "td-shared-") {
				return fmt.Errorf("invalid shared cluster identity %q", cluster.Name)
			}
		default:
			return fmt.Errorf("invalid cluster mode %q", cluster.Mode)
		}
		if err := reserve(cluster.RegistryPort, "cluster "+cluster.Name); err != nil {
			return err
		}
	}
	for _, rec := range records {
		if err := validateRecord(rec); err != nil {
			return err
		}
		cluster, exists := clusters[rec.ClusterName]
		if !exists {
			return fmt.Errorf("project %q references missing cluster %q", rec.ID, rec.ClusterName)
		}
		references[cluster.Name]++
		if cluster.Mode == project.Dedicated && cluster.Name != rec.ID {
			return fmt.Errorf("dedicated cluster %q does not belong to project %q", cluster.Name, rec.ID)
		}
		if err := reserve(rec.TiltPort, "project "+rec.ID); err != nil {
			return err
		}
	}
	for _, cluster := range clusters {
		if cluster.Mode == project.Dedicated && references[cluster.Name] != 1 {
			return fmt.Errorf("dedicated cluster %q requires exactly one project", cluster.Name)
		}
	}
	return nil
}

func (s *catalogStore) all() []catalogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]catalogEntry, 0, len(s.records))
	for _, rec := range s.records {
		result = append(result, catalogEntry{rec, s.clusters[rec.ClusterName]})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// The registry can be reused by members of the same cluster; no Tilt port
// can overlap a registry, and each project has its own Tilt port.
func (s *catalogStore) checkRegistryPort(name string, port uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkPort("", name, port)
}

func (s *catalogStore) checkTiltPort(id string, port uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkPort(id, "", port)
}

func (s *catalogStore) checkPort(projectID, clusterName string, port uint16) error {
	if port == 0 {
		return nil
	}
	for _, rec := range s.records {
		if rec.ID != projectID && rec.TiltPort == port {
			return fmt.Errorf("port %d is assigned to project %s", port, rec.ID)
		}
	}
	for _, cluster := range s.clusters {
		if cluster.Name != clusterName && cluster.RegistryPort == port {
			return fmt.Errorf("port %d is assigned to cluster %s", port, cluster.Name)
		}
	}
	return nil
}

// upsert commits the project and its cluster together. A failed validation or
// write leaves both in-memory maps and the on-disk catalog unchanged.
func (s *catalogStore) upsert(entry catalogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, cluster := entry.catalogRecord, entry.clusterRecord
	if rec.ClusterName != cluster.Name {
		return errors.New("project and cluster assignment do not match")
	}
	oldRecord, hadRecord := s.records[rec.ID]
	oldCluster, hadCluster := s.clusters[cluster.Name]
	if hadRecord && oldRecord.ClusterName != rec.ClusterName {
		return fmt.Errorf("project %q already has a cluster assignment", rec.ID)
	}
	if hadCluster && oldCluster.Mode != cluster.Mode {
		return fmt.Errorf("cluster %q already has a different mode", cluster.Name)
	}
	if hadCluster && cluster.Mode == project.Shared && oldCluster.RegistryPort != 0 && oldCluster.RegistryPort != cluster.RegistryPort {
		return fmt.Errorf("shared cluster %q already has registry port %d", cluster.Name, oldCluster.RegistryPort)
	}
	s.records[rec.ID], s.clusters[cluster.Name] = rec, cluster
	err := validateCatalog(s.records, s.clusters)
	if err == nil {
		err = s.flush()
	}
	if err != nil {
		if hadRecord { s.records[rec.ID] = oldRecord } else { delete(s.records, rec.ID) }
		if hadCluster { s.clusters[cluster.Name] = oldCluster } else { delete(s.clusters, cluster.Name) }
	}
	return err
}

func (s *catalogStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, exists := s.records[id]
	if !exists {
		return fmt.Errorf("%w: %s", ErrNotRegistered, id)
	}
	cluster := s.clusters[rec.ClusterName]
	delete(s.records, id)
	if cluster.Mode == project.Dedicated {
		delete(s.clusters, cluster.Name)
	}
	if err := s.flush(); err != nil {
		s.records[id] = rec
		s.clusters[cluster.Name] = cluster
		return err
	}
	return nil
}

func (s *catalogStore) snapshot() catalogFile {
	doc := catalogFile{
		Version: catalogVersion, Projects: make([]catalogRecord, 0, len(s.records)),
		Clusters: make([]clusterRecord, 0, len(s.clusters)),
	}
	for _, rec := range s.records { doc.Projects = append(doc.Projects, rec) }
	for _, cluster := range s.clusters { doc.Clusters = append(doc.Clusters, cluster) }
	sort.Slice(doc.Projects, func(i, j int) bool { return doc.Projects[i].ID < doc.Projects[j].ID })
	sort.Slice(doc.Clusters, func(i, j int) bool { return doc.Clusters[i].Name < doc.Clusters[j].Name })
	return doc
}

func (s *catalogStore) flush() error {
	doc:=s.snapshot()
	payload,err:=json.MarshalIndent(doc,"","  ")
	if err!=nil{return fmt.Errorf("encode catalog: %w",err)}
	payload=append(payload,'\n')
	// Use a same-directory atomic rename. Incomplete writes never replace the
	// last valid catalog; the temporary file is cleaned up on every error.
	temp,err:=os.CreateTemp(filepath.Dir(s.path),".catalog-*")
	if err!=nil{return fmt.Errorf("prepare catalog replacement: %w",err)}
	defer os.Remove(temp.Name())
	if err:=protectPrivateFile(temp.Name());err!=nil{temp.Close();return err}
	if _,err:=temp.Write(payload);err!=nil{temp.Close();return fmt.Errorf("write catalog: %w",err)}
	if err:=temp.Sync();err!=nil{temp.Close();return fmt.Errorf("sync catalog: %w",err)}
	if err:=temp.Close();err!=nil{return fmt.Errorf("close catalog: %w",err)}
	if err:=os.Rename(temp.Name(),s.path);err!=nil{return fmt.Errorf("replace catalog: %w",err)}
	return nil
}
