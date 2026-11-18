package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewDefinition(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Tiltfile"), []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []ClusterMode{Dedicated, Shared} {
		t.Run(string(mode), func(t *testing.T) {
			assignment := ClusterAssignment{Mode: mode, Name: "local-dev"}
			got, err := NewDefinition("my-project", root, assignment)
			if err != nil {
				t.Fatalf("NewDefinition() error = %v", err)
			}
			if got.ID != "my-project" || got.Directory != root || got.Tiltfile != filepath.Join(root, "Tiltfile") || got.Cluster != assignment {
				t.Fatalf("unexpected definition: %+v", got)
			}
		})
	}
}

func TestNewDefinitionRejectsInvalidMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Tiltfile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	valid := ClusterAssignment{Mode: Dedicated, Name: "local-dev"}
	cases := []struct {
		name, id, directory string
		assignment          ClusterAssignment
		want                string
	}{
		{"missing ID", " ", root, valid, "project ID is required"},
		{"missing directory", "app", " ", valid, "project directory is required"},
		{"invalid mode", "app", root, ClusterAssignment{Mode: "other", Name: "local-dev"}, "unsupported cluster mode"},
		{"missing cluster", "app", root, ClusterAssignment{Mode: Dedicated}, "cluster name is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDefinition(tc.id, tc.directory, tc.assignment)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewDefinition() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewDefinitionRejectsMissingTiltfile(t *testing.T) {
	root := t.TempDir()
	_, err := NewDefinition("app", root, ClusterAssignment{Mode: Dedicated, Name: "local-dev"})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NewDefinition() error = %v, want os.ErrNotExist", err)
	}
}

func TestNewDefinitionRejectsFileAsDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "project")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDefinition("app", file, ClusterAssignment{Mode: Dedicated, Name: "local-dev"})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("NewDefinition() error = %v, want not a directory", err)
	}
}

func TestNewDefinitionRejectsDirectoryAsTiltfile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Tiltfile"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := NewDefinition("app", root, ClusterAssignment{Mode: Dedicated, Name: "local-dev"})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("NewDefinition() error = %v, want not a regular file", err)
	}
}
