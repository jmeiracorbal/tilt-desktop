# Tilt Desktop

Tilt Desktop is a local development environment manager for Kubernetes projects. It will orchestrate k3d clusters and Tilt instances so that users can work with applications without manually managing local clusters.

## Architecture

- **Go core**: application definitions, lifecycle, reconciliation, and integrations with Tilt and k3d.
- **Fyne GUI**: a native Go interface, kept separate from the core. No frontend JavaScript, TypeScript, or WebView.
- **Clusters**: one dedicated k3d cluster per application by default; explicit shared-cluster assignment when needed.
- **Tiltfiles**: project-owned Tiltfiles remain the source of truth for development workflows.

## Current status

The initial Go core only validates application definitions and their cluster assignments. Cluster provisioning, Tilt process management, persistence, and the Fyne UI are **not implemented yet**.

## Development

```sh
go test ./...
```

Architecture decisions are maintained in Google Drive under `Proyectos/Opensource/Tilt Desktop`.
