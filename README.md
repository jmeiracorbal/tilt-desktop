# Tilt Desktop

Tilt Desktop is a local Kubernetes development-environment manager built in Go. It is designed to manage k3d clusters and Tilt instances without requiring developers to provision local clusters manually.

## Architecture

- **Go core**: project definitions and lifecycle orchestration.
- **Fyne GUI**: native Go widgets; no React, TypeScript, Electron, or WebView.
- **Tiltfile**: the source of truth for build, deploy, and live-update behavior.
- **Cluster isolation**: dedicated k3d cluster per application by default, with explicit shared-cluster assignments.

## Current implementation

- `internal/project`: validates application IDs, project directories, Tiltfiles, and dedicated/shared cluster assignments.
- `internal/desktop` and `cmd/tilt-desktop`: native Fyne application with a project catalog, folder picker, explicit start/stop/delete actions, resource and log views, safe-state handling and background lifecycle calls.
- `internal/application`: persists a versioned project catalog under the user's config directory, derives a stable dedicated-cluster identity, and coordinates cluster creation/reuse, private kubeconfig materialization, Tilt startup, status, resources, logs, stop and confirmed deletion. New registrations do not require developers to pre-create a cluster or edit the Tiltfile.
- `internal/tilt`: starts and supervises independent Tilt processes for project/k3d-cluster pairs with explicit private kubeconfig and bound localhost port; reads UIResource snapshots and bounded log output using the Tilt CLI.
- Tilt child processes are stopped by interrupting the process, not by running `tilt down`. Unexpected process exits are exposed through session status.
- `internal/k3d`: discovers clusters, ensures a cluster is created or running, stops and deletes only clusters whose k3d runtime labels match the requested management ID, and reads their kubeconfig.
- A newly created cluster receives a dedicated registry bound to localhost. The application manager selects available local ports automatically; when a cluster already exists, it recovers the registry's published port from `k3d registry list --output json`. This in-memory allocation is not a cross-process reservation.
- The k3d CLI runs through Go's `os/exec`, without Bash scripts. Ownership is verified against actual Docker container labels because k3d's JSON cluster listing omits user-defined labels. `NewLocalClient` uses an isolated `KUBECONFIG` under the user's config directory; cluster creation disables k3d's default kubeconfig update.

**Real integration verification:** the `integration` GitHub Actions workflow runs a tagged Go integration test on Ubuntu with Docker, k3d, Tilt, and kubectl. It creates a managed cluster and localhost registry, builds and pushes an image through Tilt, verifies a Kubernetes Deployment rollout and API/log queries, then stops Tilt without deleting deployed resources, restarts the cluster, deletes it, and verifies the global kubeconfig was not modified.

**Persistence and reconciliation:** `NewLocalManager` loads `tilt-desktop/catalog.json` from the user's configuration directory. Entries are versioned, written via an atomic same-directory replacement and stored with restrictive permissions; kubeconfig **contents** are never stored in the catalog. Tilt startup metadata (port, kubeconfig path and PID) is journaled. A new manager reuses the previously assigned Tilt HTTP port when it is free, and returns an explicit conflict if it is occupied. It can inspect an existing Tilt Session through its local API and compare the Tiltfile and PID. Verified but unmanaged live processes are reported as `orphaned`, and uncertain processes as `unknown`; both block duplicate starts and destructive operations. A previously owned process that is confirmed dead and whose port is free can be marked idle, and stale credentials removed.

**Limitations:** Tilt Desktop does **not** adopt or terminate orphaned processes from a previous application instance automatically. Cross-process locking and atomic host-port reservations are not implemented: running two application managers concurrently is unsupported. The existing low-level APIs still accept explicit ports and kubeconfig paths. The catalog restores registered projects, not live process handles. Shared-cluster coordination, cross-platform GUI validation and packaging remain pending. A successful `Start` indicates process launch, not workload readiness. Linux CI is validated; macOS and Windows operation, including graceful interrupts, remains unverified. The future UI must obtain explicit confirmation before destructive deletion.

## Desktop application

Start the native UI from the repository root:

```sh
go run ./cmd/tilt-desktop
```

The desktop requires Docker, k3d and Tilt installed and available on `PATH` for cluster lifecycle actions. On Linux, compiling the native GUI also requires OpenGL and X11 development libraries. Kubernetes resources are built and deployed through the project's existing Tiltfile.

Use **Add project** to register a directory containing a Tiltfile. Each project receives a dedicated k3d cluster by default. Select a project to inspect its state, resources and recent logs. **Start** creates or resumes its environment; **Stop** stops its managed Tilt process and cluster without deleting deployed resources. **Delete** always requires explicit confirmation and destroys the dedicated cluster, not the source directory.

Long-running operations execute off the GUI thread. **Refresh** reconciles the persisted catalog against observable Tilt sessions. Unverified, orphaned or starting sessions block destructive operations. Closing the window while managed sessions are running asks whether to stop them first; it does not run `tilt down`.

This is the first native desktop increment, tested headlessly and built on Linux CI. It is not yet a packaged macOS, Linux or Windows release.

## Application manager

The Go API can start a project from its existing folder without requiring manual k3d setup:

```go
manager, err := application.NewLocalManager()
if err != nil { return err }

project, err := manager.Register("/absolute/path/to/project")
if err != nil { return err }

if err := manager.Start(ctx, project.ID); err != nil { return err }
state, err := manager.Status(project.ID)
if err != nil { return err }
_ = state

// Stops Tilt and the dedicated cluster without removing its data.
return manager.Stop(ctx, project.ID)
```

Deletion is separate and requires confirmation: `manager.Delete(ctx, project.ID, true)`. The manager currently supports only dedicated clusters. `Reconcile` / `ReconcileAll` must be used to check unresolved sessions before attempting to restart them. A running orphan is never silently adopted.

## Development

```sh
go test -count=1 ./...
go vet ./...

# Requires a working Docker runtime, k3d, Tilt, and kubectl:
go test -v -tags=integration -count=1 -timeout=15m ./internal/integration
```

The application core uses only the Go standard library. Fyne 2.8.2 is a dependency of the UI only.

The architecture and verified implementation changes are recorded in Google Drive at `Proyectos/Opensource/Tilt Desktop`.
