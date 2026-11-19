# Tilt Desktop

Tilt Desktop is a local Kubernetes development-environment manager built in Go. It is designed to manage k3d clusters and Tilt instances without requiring developers to provision local clusters manually.

## Architecture

- **Go core**: project definitions and lifecycle orchestration.
- **Fyne GUI (planned)**: native Go widgets; no React, TypeScript, Electron, or WebView.
- **Tiltfile**: the source of truth for build, deploy, and live-update behavior.
- **Cluster isolation**: dedicated k3d cluster per application by default, with explicit shared-cluster assignments.

## Current implementation

- `internal/project`: validates application IDs, project directories, Tiltfiles, and dedicated/shared cluster assignments.
- `internal/application`: registers a project folder with a deterministic dedicated cluster identity and coordinates cluster creation/reuse, private kubeconfig materialization, Tilt startup, status, resources, logs, stop and confirmed deletion. New registrations do not require users to pre-create a k3d cluster or name one in the Tiltfile.
- `internal/tilt`: starts and supervises independent Tilt processes for project/k3d-cluster pairs with explicit private kubeconfig and bound localhost port; reads UIResource snapshots and bounded log output using the Tilt CLI.
- Tilt child processes are stopped by interrupting the process, not by running `tilt down`. Unexpected process exits are exposed through session status.
- `internal/k3d`: discovers clusters, ensures a cluster is created or running, stops and deletes only clusters whose k3d runtime labels match the requested management ID, and reads their kubeconfig.
- A newly created cluster receives a dedicated registry bound to localhost. The application manager selects available local ports automatically; when a cluster already exists, it recovers the registry's published port from `k3d registry list --output json`. This in-memory allocation is not a cross-process reservation.
- The k3d CLI runs through Go's `os/exec`, without Bash scripts. Ownership is verified against actual Docker container labels because k3d's JSON cluster listing omits user-defined labels. `NewLocalClient` uses an isolated `KUBECONFIG` under the user's config directory; cluster creation disables k3d's default kubeconfig update.

**Real integration verification:** the `integration` GitHub Actions workflow runs a tagged Go integration test on Ubuntu with Docker, k3d, Tilt, and kubectl. It creates a managed cluster and localhost registry, builds and pushes an image through Tilt, verifies a Kubernetes Deployment rollout and API/log queries, then stops Tilt without deleting deployed resources, restarts the cluster, deletes it, and verifies the global kubeconfig was not modified.

**Limitations:** the existing low-level APIs still accept explicit ports and kubeconfig paths. The application manager automates these values, but its catalog is in memory only. Re-registering a folder in a fresh process can find its existing managed cluster, but **does not reconnect to orphaned Tilt processes**. Persistent port assignments, cross-process locks, shared-cluster coordination, process reconciliation, Fyne UI and application packaging remain unimplemented. A successful `Start` indicates a child process launch, not workload readiness. Linux CI is validated; macOS and Windows behavior, including graceful process interrupts, remains unverified. A future UI must obtain explicit confirmation before destructive deletion.

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

Deletion is separate and requires confirmation: `manager.Delete(ctx, project.ID, true)`. The in-memory manager currently supports only dedicated clusters.

## Development

```sh
go test -count=1 ./...
go vet ./...

# Requires a working Docker runtime, k3d, Tilt, and kubectl:
go test -v -tags=integration -count=1 -timeout=15m ./internal/integration
```

The core currently uses only the Go standard library. Fyne is not a dependency until the interface is introduced.

The architecture and verified implementation changes are recorded in Google Drive at `Proyectos/Opensource/Tilt Desktop`.
