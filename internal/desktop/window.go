// Package desktop presents the application lifecycle using Fyne widgets.
// It contains no k3d or Tilt process implementation.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jmeiracorbal/tilt-desktop/internal/application"
	"github.com/jmeiracorbal/tilt-desktop/internal/project"
	"github.com/jmeiracorbal/tilt-desktop/internal/tilt"
)

// Backend describes the Go application operations used by the window.
type Backend interface {
	Register(string) (project.Definition, error)
	RegisterShared(string, string) (project.Definition, error)
	ListSharedClusters() ([]application.SharedClusterState, error)
	StopSharedCluster(context.Context, string) error
	DeleteSharedCluster(context.Context, string, bool) error
	List() []application.State
	Status(string) (application.State, error)
	ReconcileAll(context.Context) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Delete(context.Context, string, bool) error
	Resources(context.Context, string) ([]tilt.Resource, error)
	Logs(context.Context, string, string, int) ([]byte, error)
}

type view struct {
	window fyne.Window
	backend Backend

	items []application.State
	resources []tilt.Resource
	selected string
	busy bool

	projects *widget.List
	resourceList *widget.List
	logs *widget.TextGrid
	title *widget.Label
	metadata *widget.Label
	notice *widget.Label
	add *widget.Button
	shared *widget.Button
	environment *widget.Button
	start *widget.Button
	stop *widget.Button
	delete *widget.Button
	refresh *widget.Button
}

// NewWindow creates the initial native desktop UI. Every potentially blocking
// backend operation runs outside Fyne's event goroutine.
func NewWindow(app fyne.App, backend Backend) (fyne.Window, error) {
	if app == nil || backend == nil {
		return nil, errors.New("Fyne application and backend are required")
	}
	v := &view{window: app.NewWindow("Tilt Desktop"), backend: backend}
	v.window.Resize(fyne.NewSize(1100, 740))

	v.title = widget.NewLabelWithStyle("Select a project", fyne.TextAlignLeading, fyne.TextStyle{Bold:true})
	v.metadata = widget.NewLabel("Register an existing folder containing a Tiltfile.")
	v.metadata.Wrapping = fyne.TextWrapWord
	v.notice = widget.NewLabel("Ready")
	v.notice.Wrapping = fyne.TextWrapWord
	v.logs = widget.NewTextGridFromString("Select a running project to view logs.")
	v.resourceList = widget.NewList(
		func() int { return len(v.resources) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(index widget.ListItemID, object fyne.CanvasObject) {
			if index < 0 || index >= len(v.resources) { return }
			object.(*widget.Label).SetText(formatResource(v.resources[index]))
		},
	)
	v.projects = widget.NewList(
		func() int { return len(v.items) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(index widget.ListItemID, object fyne.CanvasObject) {
			if index < 0 || index >= len(v.items) { return }
			object.(*widget.Label).SetText(formatProject(v.items[index]))
		},
	)
	v.projects.OnSelected = func(index widget.ListItemID) {
		if index < 0 || index >= len(v.items) { return }
		v.selected = v.items[index].ProjectID
		v.showSelection()
	}
	v.projects.OnUnselected = func(_ widget.ListItemID) {
		// The selected ID is retained while the list is refreshed.
	}

	v.add = widget.NewButton("Add project", v.chooseFolder)
	v.shared = widget.NewButton("Shared clusters", v.showSharedClusters)
	v.environment = widget.NewButton("Environment", v.showEnvironment)
	v.refresh = widget.NewButton("Refresh", v.refreshAll)
	v.start = widget.NewButton("Start", func() {
		id := v.selected
		if id == "" { return }
		v.execute("Starting project", func(ctx context.Context) error { return v.backend.Start(ctx, id) }, nil)
	})
	v.stop = widget.NewButton("Stop", func() {
		id := v.selected
		if id == "" { return }
		v.execute("Stopping project", func(ctx context.Context) error { return v.backend.Stop(ctx, id) }, nil)
	})
	v.delete = widget.NewButton("Delete", v.confirmDelete)

	heading := container.NewBorder(nil, nil, nil, container.NewHBox(v.add, v.refresh),
		widget.NewLabelWithStyle("Tilt Desktop", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	sidebar := container.NewBorder(heading, container.NewVBox(v.shared, v.environment), nil, nil, v.projects)

	actions := container.NewHBox(v.start, v.stop, v.delete)
	details := container.NewVBox(v.title, v.metadata, actions)
	tabs := container.NewAppTabs(
		container.NewTabItem("Resources", v.resourceList),
		container.NewTabItem("Logs", container.NewScroll(v.logs)),
	)
	mainContent := container.NewBorder(details, nil, nil, nil, tabs)
	split := container.NewHSplit(sidebar, mainContent)
	split.Offset = 0.30

	v.window.SetContent(container.NewBorder(nil, container.NewPadded(v.notice), nil, nil, split))
	v.window.SetCloseIntercept(v.confirmClose)
	v.syncCatalog()
	v.refreshAll()
	return v.window, nil
}

func (v *view) chooseFolder() {
	if v.busy { return }
	dialog.ShowFolderOpen(func(directory fyne.ListableURI, err error) {
		if err != nil { dialog.ShowError(err, v.window); return }
		if directory == nil { return }
		if v.busy { return }
		v.chooseCluster(directory.Path())
	}, v.window)
}

func (v *view) confirmDelete() {
	if v.busy || v.selected == "" { return }
	state, ok := v.selectedState()
	if !ok || !canDelete(state) { return }
	id := state.ProjectID
	title, message := projectDeletionPrompt(state)
	dialog.ShowConfirm(title, message, func(confirmed bool) {
		if !confirmed { return }
		v.execute("Deleting project", func(ctx context.Context) error {
			return v.backend.Delete(ctx, id, true)
		}, nil)
	}, v.window)
}

func (v *view) refreshAll() {
	v.execute("Refreshing project state", func(ctx context.Context) error {
		return v.backend.ReconcileAll(ctx)
	}, nil)
}

func (v *view) execute(label string, operation func(context.Context) error, onSuccess func()) {
	if v.busy { return }
	v.busy = true
	v.notice.SetText(label + "...")
	v.updateButtons()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := operation(ctx)
		fyne.Do(func() {
			v.busy = false
			v.syncCatalog()
			if err != nil {
				v.notice.SetText(label + " failed: " + err.Error())
				dialog.ShowError(err, v.window)
			} else {
				v.notice.SetText(label + " completed.")
			}
			v.updateButtons()
			if err == nil && onSuccess != nil { onSuccess() }
		})
	}()
}

// syncCatalog is called only from the Fyne event goroutine. It reads a
// snapshot from the backend and never invokes slow network operations.
func (v *view) syncCatalog() {
	states := v.backend.List()
	for i := range states {
		current, err := v.backend.Status(states[i].ProjectID)
		if err == nil { states[i] = current }
	}
	v.items = states
	v.projects.Refresh()
	index := -1
	for i := range states {
		if states[i].ProjectID == v.selected {
			index = i
			break
		}
	}
	if index == -1 {
		if len(states) > 0 {
			index = 0
			v.selected = states[0].ProjectID
		} else {
			v.selected = ""
		}
	}
	if index != -1 { v.projects.Select(index) }
	v.showSelection()
}

func (v *view) selectedState() (application.State, bool) {
	for _, state := range v.items {
		if state.ProjectID == v.selected { return state, true }
	}
	return application.State{}, false
}

func (v *view) showSelection() {
	state, ok := v.selectedState()
	v.resources = nil
	v.resourceList.Refresh()
	v.logs.SetText("Select a running project to view logs.")
	if !ok {
		v.title.SetText("Select a project")
		v.metadata.SetText("Register an existing folder containing a Tiltfile.")
		v.updateButtons()
		return
	}
	v.title.SetText(filepath.Base(state.Directory))
	v.metadata.SetText(fmt.Sprintf(
		"%s\nCluster: %s (%s)\nSession: %s\nTilt port: %d    Registry port: %d",
		state.Directory, state.ClusterName, state.ClusterMode, state.Phase, state.TiltPort, state.RegistryPort,
	))
	v.updateButtons()
	if state.Running && state.Phase == "running" { v.loadObservations(state.ProjectID) }
	if state.Phase == "orphaned" || state.Phase == "unknown" || state.Phase == "starting" {
		v.logs.SetText("Previous Tilt process requires reconciliation. Destructive actions are disabled.")
	}
}

func (v *view) loadObservations(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resources, resourceErr := v.backend.Resources(ctx, id)
		logs, logsErr := v.backend.Logs(ctx, id, "", 200)
		fyne.Do(func() {
			if v.selected != id { return }
			if resourceErr == nil {
				v.resources = resources
				v.resourceList.Refresh()
			}
			switch {
			case logsErr == nil:
				v.logs.SetText(string(logs))
			default:
				v.logs.SetText("Logs unavailable: " + logsErr.Error())
			}
			if resourceErr != nil {
				v.notice.SetText("Resources unavailable: " + resourceErr.Error())
			}
		})
	}()
}

func (v *view) updateButtons() {
	state, ok := v.selectedState()
	setEnabled(v.add, !v.busy)
	setEnabled(v.shared, !v.busy)
	setEnabled(v.environment, !v.busy)
	setEnabled(v.refresh, !v.busy)
	setEnabled(v.start, !v.busy && ok && canStart(state))
	setEnabled(v.stop, !v.busy && ok && canStop(state))
	setEnabled(v.delete, !v.busy && ok && canDelete(state))
}

func setEnabled(button *widget.Button, enabled bool) {
	if enabled { button.Enable() } else { button.Disable() }
}

func (v *view) confirmClose() {
	if v.busy {
		dialog.ShowInformation("Operation in progress", "Finish the active operation before closing Tilt Desktop.", v.window)
		return
	}
	var running []string
	for _, entry := range v.items {
		if entry.Phase == "running" && entry.Running { running = append(running, entry.ProjectID) }
	}
	if len(running) == 0 {
		v.closeWindow()
		return
	}
	dialog.ShowConfirm("Stop applications and quit",
		"Stop managed Tilt processes and their dedicated clusters before closing?\n\nShared clusters stay running. Deployed workloads remain in their clusters.",
		func(confirmed bool) {
			if !confirmed { return }
			v.execute("Stopping applications", func(ctx context.Context) error {
				var failures []error
				for _, id := range running {
					if err := v.backend.Stop(ctx, id); err != nil {
						failures = append(failures, fmt.Errorf("%s: %w", id, err))
					}
				}
				return errors.Join(failures...)
			}, v.closeWindow)
		}, v.window)
}

func (v *view) closeWindow() {
	v.window.SetCloseIntercept(nil)
	v.window.Close()
}

func formatProject(s application.State) string {
	name := filepath.Base(s.Directory)
	if name == "." || name == string(filepath.Separator) { name = s.ProjectID }
	phase := strings.TrimSpace(s.Phase)
	if phase == "" { phase = "idle" }
	return name + "  [" + phase + "]"
}

func formatResource(r tilt.Resource) string {
	return fmt.Sprintf("%s   runtime: %s   update: %s", r.Name, r.RuntimeStatus, r.UpdateStatus)
}

func canStart(state application.State) bool { return state.Phase == "idle" }
func canStop(state application.State) bool { return state.Phase == "running" && state.Running }
func canDelete(state application.State) bool {
	return state.Phase == "idle" || (state.Phase == "running" && state.Running)
}
