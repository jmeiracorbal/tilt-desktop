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
	Records []struct {
		catalogRecord
		RegistryPort uint16 `json:"registryPort,omitempty"`
	} `json:"records"`
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
			file.Projects = append(file.Projects, rec.catalogRecord)
			file.Clusters = append(file.Clusters, clusterRecord{
				Name: rec.ClusterName, Mode: project.Dedicated, RegistryPort: rec.RegistryPort,
			})
		}
		return file, true, nil
	default:
		return catalogFile{}, false, fmt.Errorf("unsupported catalog version %d", header.Version)
	}
}
