# Tilt Desktop

Tilt Desktop is a local Kubernetes development-environment manager built in Go. It is designed to manage k3d clusters and Tilt instances without requiring developers to provision local clusters manually.

## Architecture

- **Go core**: project definitions and lifecycle orchestration.
- **Fyne GUI (planned)**: native Go widgets; no React, TypeScript, Electron, or WebView.
- **Tiltfile**: the source of truth for build, deploy, and live-update behavior.
- **Cluster isolation**: dedicated k3d cluster per application by default, with explicit shared-cluster assignments.

## Current implementation

- `internal/project`: validates application IDs, project directories, Tiltfiles, and dedicated/shared cluster assignments.
- `internal/k3d`: discovers clusters, ensures a cluster is created or running, stops and deletes only clusters whose k3d runtime labels match the requested management ID, and reads their kubeconfig.
- A newly created cluster receives a dedicated k3d-managed registry bound to localhost, with a dynamically assigned host port.
- The k3d CLI runs through Go's `os/exec`, without Bash scripts. `NewLocalClient` uses an isolated `KUBECONFIG` under the user's config directory and creation disables k3d's default kubeconfig update.

**Limitations:** the k3d client has been unit-tested with a simulated command runner but has not yet been verified against a running k3d cluster. The application catalog, ownership persistence, cross-process synchronization, shared-cluster usage protection, process supervision for Tilt, and Fyne interface remain unimplemented. Destructive operations must not be surfaced through a UI until user confirmation and shared-cluster usage checks are in place.

## Development

```sh
go test -count=1 ./...
go vet ./...
```

The core currently uses only the Go standard library. Fyne is not a dependency until the interface is introduced.

The architecture and verified implementation changes are recorded in Google Drive at `Proyectos/Opensource/Tilt Desktop`.
