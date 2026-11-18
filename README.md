# Tilt Desktop

Tilt Desktop is a local Kubernetes development-environment manager built in Go. It is designed to manage k3d clusters and Tilt instances without requiring developers to provision local clusters manually.

## Architecture

- **Go core**: project definitions and lifecycle orchestration.
- **Fyne GUI (planned)**: native Go widgets; no React, TypeScript, Electron, or WebView.
- **Tiltfile**: the source of truth for build, deploy, and live-update behavior.
- **Cluster isolation**: dedicated k3d cluster per application by default, with explicit shared-cluster assignments.

## Current implementation

- `internal/project`: validates application IDs, project directories, Tiltfiles, and dedicated/shared cluster assignments.
- `internal/tilt`: starts and supervises independent Tilt processes for project/k3d-cluster pairs with explicit private kubeconfig and bound localhost port; reads UIResource snapshots and bounded log output using the Tilt CLI.
- Tilt child processes are stopped by interrupting the process, not by running `tilt down`. Unexpected process exits are exposed through session status.
- `internal/k3d`: discovers clusters, ensures a cluster is created or running, stops and deletes only clusters whose k3d runtime labels match the requested management ID, and reads their kubeconfig.
- A newly created cluster receives a dedicated k3d-managed registry bound to localhost. The caller must supply an available, nonzero `RegistryPort` through `k3d.Spec` when creating a cluster; automatic persistent port assignment is not implemented yet.
- The k3d CLI runs through Go's `os/exec`, without Bash scripts. Ownership is verified against actual Docker container labels because k3d's JSON cluster listing omits user-defined labels. `NewLocalClient` uses an isolated `KUBECONFIG` under the user's config directory; cluster creation disables k3d's default kubeconfig update.

**Real integration verification:** the `integration` GitHub Actions workflow runs a tagged Go integration test on Ubuntu with Docker, k3d, Tilt, and kubectl. It creates a managed cluster and localhost registry, builds and pushes an image through Tilt, verifies a Kubernetes Deployment rollout and API/log queries, then stops Tilt without deleting deployed resources, restarts the cluster, deletes it, and verifies the global kubeconfig was not modified.

**Limitations:** the full lifecycle is verified on Linux CI only, not macOS or Windows. The caller currently supplies the registry port, private kubeconfig path, and Tilt HTTP port. The Tilt session manager does not recover orphaned processes across application restarts; process startup does not imply workload readiness. Graceful signal handling remains unverified on Windows. The application catalog, ownership persistence, cross-process synchronization, shared-cluster usage protection, and Fyne interface remain unimplemented. Destructive operations must not be surfaced through the future UI until user confirmation and shared-cluster usage checks are in place.

## Development

```sh
go test -count=1 ./...
go vet ./...

# Requires a working Docker runtime, k3d, Tilt, and kubectl:
go test -v -tags=integration -count=1 -timeout=15m ./internal/integration
```

The core currently uses only the Go standard library. Fyne is not a dependency until the interface is introduced.

The architecture and verified implementation changes are recorded in Google Drive at `Proyectos/Opensource/Tilt Desktop`.
