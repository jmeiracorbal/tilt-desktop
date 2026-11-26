package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jmeiracorbal/tilt-desktop/internal/application"
	"github.com/jmeiracorbal/tilt-desktop/internal/project"
)

const (
	dedicatedChoice = "Dedicated cluster"
	existingChoice = "Existing shared cluster"
	newChoice = "New shared cluster"
)

func (v *view) chooseCluster(path string) {
	clusters, err := v.backend.ListSharedClusters()
	if err != nil {
		dialog.ShowError(err, v.window)
		return
	}
	names := make([]string, 0, len(clusters))
	for _, cluster := range clusters {
		names = append(names, cluster.Name)
	}
	modes := []string{dedicatedChoice, newChoice}
	if len(names) > 0 {
		modes = []string{dedicatedChoice, existingChoice, newChoice}
	}
	mode := widget.NewRadioGroup(modes, nil)
	existing := widget.NewSelect(names, nil)
	if len(names) > 0 {
		existing.SetSelected(names[0])
	}
	name := widget.NewEntry()
	name.SetPlaceHolder("td-shared-development")
	existingField := container.NewVBox(widget.NewLabel("Managed shared cluster"), existing)
	newField := container.NewVBox(widget.NewLabel("Cluster name (td-shared-...)"), name)
	note := widget.NewLabel("Dedicated clusters isolate projects. Shared clusters use a common Kubernetes cluster and registry; namespaces must be managed by the projects.")
	note.Wrapping = fyne.TextWrapWord
	mode.OnChanged = func(choice string) {
		existingField.Hide()
		newField.Hide()
		switch choice {
		case existingChoice:
			existingField.Show()
		case newChoice:
			newField.Show()
		}
	}
	mode.SetSelected(dedicatedChoice)
	content := container.NewVBox(widget.NewLabel(filepath.Base(path)), mode, existingField, newField, note)
	var picker dialog.Dialog
	submit := widget.NewButton("Register", func() {
		if v.busy {
			return
		}
		choice := mode.Selected
		clusterName := ""
		switch choice {
		case dedicatedChoice:
		case existingChoice:
			clusterName = existing.Selected
		case newChoice:
			clusterName = strings.TrimSpace(name.Text)
		default:
			dialog.ShowError(fmt.Errorf("select a cluster mode"), v.window)
			return
		}
		if choice != dedicatedChoice && clusterName == "" {
			dialog.ShowError(fmt.Errorf("enter or select a shared cluster name"), v.window)
			return
		}
		if choice == newChoice {
			for _, current := range names {
				if current == clusterName {
					dialog.ShowError(fmt.Errorf("cluster %q already exists; select it instead", clusterName), v.window)
					return
				}
			}
		}
		picker.Hide()
		v.execute("Registering project", func(_ context.Context) error {
			if choice == dedicatedChoice {
				_, err := v.backend.Register(path)
				return err
			}
			_, err := v.backend.RegisterShared(path, clusterName)
			return err
		}, nil)
	})
	picker = dialog.NewCustom("Choose cluster", "Cancel", container.NewVBox(content, submit), v.window)
	picker.Resize(fyne.NewSize(520, 340))
	picker.Show()
}

func projectDeletionPrompt(state application.State) (string, string) {
	name := filepath.Base(state.Directory)
	if state.ClusterMode == project.Shared {
		return "Remove shared project", fmt.Sprintf(
			"Remove %q from Tilt Desktop?\n\nIts managed Tilt process will stop. The shared cluster, Kubernetes workloads and source directory are retained.",
			name,
		)
	}
	return "Delete project and cluster", fmt.Sprintf(
		"Delete %q and its dedicated k3d cluster?\n\nThis permanently removes the cluster and its Kubernetes resources. The project directory is not deleted.",
		name,
	)
}

func canStopShared(cluster application.SharedClusterState) bool {
	return cluster.Unresolved == 0
}

func canDeleteShared(cluster application.SharedClusterState) bool {
	return cluster.Projects == 0 && cluster.Unresolved == 0
}

func (v *view) showSharedClusters() {
	if v.busy {
		return
	}
	clusters, err := v.backend.ListSharedClusters()
	if err != nil {
		dialog.ShowError(err, v.window)
		return
	}
	if len(clusters) == 0 {
		dialog.ShowInformation("Shared clusters", "No shared clusters registered. Add a project and choose a new shared cluster.", v.window)
		return
	}
	names := make([]string, 0, len(clusters))
	for _, cluster := range clusters {
		names = append(names, cluster.Name)
	}
	selector := widget.NewSelect(names, nil)
	details := widget.NewLabel("")
	details.Wrapping = fyne.TextWrapWord
	var panel dialog.Dialog
	stop := widget.NewButton("Stop cluster", func() {
		name := selector.Selected
		panel.Hide()
		v.execute("Stopping shared cluster", func(ctx context.Context) error {
			return v.backend.StopSharedCluster(ctx, name)
		}, v.showSharedClusters)
	})
	remove := widget.NewButton("Delete cluster", func() {
		name := selector.Selected
		panel.Hide()
		dialog.ShowConfirm("Delete shared cluster", fmt.Sprintf(
			"Permanently delete shared cluster %q and its Kubernetes resources?\n\nNo projects may remain assigned.",
			name,
		), func(confirmed bool) {
			if !confirmed {
				v.showSharedClusters()
				return
			}
			v.execute("Deleting shared cluster", func(ctx context.Context) error {
				return v.backend.DeleteSharedCluster(ctx, name, true)
			}, v.showSharedClusters)
		}, v.window)
	})
	selector.OnChanged = func(name string) {
		for _, cluster := range clusters {
			if cluster.Name != name {
				continue
			}
			details.SetText(fmt.Sprintf("Registry port: %d\nAssigned projects: %d\nSessions not confirmed idle: %d", cluster.RegistryPort, cluster.Projects, cluster.Unresolved))
			setEnabled(stop, canStopShared(cluster))
			setEnabled(remove, canDeleteShared(cluster))
			return
		}
		stop.Disable()
		remove.Disable()
	}
	selector.SetSelected(names[0])
	note := widget.NewLabel("Stop requires all sessions to be idle. Delete requires zero assigned projects and confirmation. Start a member project to start the cluster.")
	note.Wrapping = fyne.TextWrapWord
	panel = dialog.NewCustom("Shared clusters", "Close", container.NewVBox(selector, details, note, container.NewHBox(stop, remove)), v.window)
	panel.Resize(fyne.NewSize(500, 280))
	panel.Show()
}
