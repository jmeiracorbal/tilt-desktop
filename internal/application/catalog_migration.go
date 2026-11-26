package application

import (
	"encoding/json"
	"fmt"

	"github.com/jmeiracorbal/tilt-desktop/internal/project"
)

// Version 1 stored registry assignments in each project. Conversion is
// performed once, under the manager's exclusive lock, before any operations.
type catalogV1 struct {
	Version int `json:"version"`
	Records []catalogV1Record `json:"records"`
}

// The original schema is frozen independently of future catalogRecord changes.
type catalogV1Record struct {
	ID           string `json:"id"`
	Directory    string `json:"directory"`
	ClusterName  string `json:"clusterName"`
	RegistryPort uint16 `json:"registryPort,omitempty"`
	TiltPort     uint16 `json:"tiltPort,omitempty"`
	Kubeconfig   string `json:"kubeconfig,omitempty"`
	Phase        phase  `json:"phase"`
	PID          int64  `json:"pid,omitempty"`
}

func decodeCatalog(payload []byte) (catalogFile, bool, error) {
	var header struct { Version int `json:"version"` }
	if err := json.Unmarshal(payload, &header); err != nil {
		return catalogFile{}, false, fmt.Errorf("decode catalog version: %w", err)
	}
	switch header.Version {
	case catalogVersion:
		var file catalogFile
		if err := decodeStrict(payload, &file); err != nil { return catalogFile{}, false, err }
		if file.Projects == nil || file.Clusters == nil {
			return catalogFile{}, false, fmt.Errorf("catalog requires projects and clusters arrays")
		}
		return file, false, nil
	case 1:
		var old catalogV1
		if err := decodeStrict(payload, &old); err != nil { return catalogFile{}, false, err }
		if old.Records == nil { return catalogFile{}, false, fmt.Errorf("catalog v1 requires records array") }
		file := catalogFile{
			Version: catalogVersion, Projects: make([]catalogRecord, 0, len(old.Records)),
			Clusters: make([]clusterRecord, 0, len(old.Records)),
		}
		for _, rec := range old.Records {
			file.Projects = append(file.Projects, catalogRecord{
				ID: rec.ID, Directory: rec.Directory, ClusterName: rec.ClusterName,
				TiltPort: rec.TiltPort, Kubeconfig: rec.Kubeconfig, Phase: rec.Phase, PID: rec.PID,
			})
			file.Clusters = append(file.Clusters, clusterRecord{
				Name: rec.ClusterName, Mode: project.Dedicated, RegistryPort: rec.RegistryPort,
			})
		}
		return file, true, nil
	default:
		return catalogFile{}, false, fmt.Errorf("unsupported catalog version %d", header.Version)
	}
}
