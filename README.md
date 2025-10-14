# CLS Controller

A generalized, configurable Kubernetes controller that creates resources based on cluster events using the CLS Backend API.

## Overview

The CLS Controller is a template-driven, event-based controller that can create any Kubernetes resource in response to cluster lifecycle events. It supports:

- **Template-driven resource creation**: Uses Go templates to create Jobs, Custom Resources, or any Kubernetes resource
- **Event-driven execution**: Responds to cluster created/updated/deleted/reconcile events via Pub/Sub
- **Multi-tenant organization support**: Automatically extracts organization context from events for dynamic multi-tenancy
- **Multiple deployment targets**: Local Kubernetes, remote clusters, or Maestro API
- **Precondition filtering**: Only processes events that match specified conditions
- **Two update strategies**: In-place updates for mutable resources, versioned creation for immutable resources
- **Rich status reporting**: Two-layer status reporting with controller conditions and raw resource status

## Architecture

- **ControllerConfig CRD**: Defines what resources to create and how to manage them
- **Template Engine**: Renders Kubernetes resources using cluster data
- **Client Manager**: Manages connections to different target types (local K8s, remote K8s, Maestro)
- **Status Reporter**: Reports detailed status back to CLS Backend

## Quick Start

1. **View available commands**:
   ```bash
   make help
   ```

2. **Build and deploy everything**:
   ```bash
   make all
   ```

3. **Or step by step**:
   ```bash
   # Build the container image
   make build

   # Push to GCR
   make push

   # Deploy to GKE
   make deploy

   # Check status
   make status
   ```

4. **View logs**:
   ```bash
   make logs
   # or follow logs
   make logs-follow
   ```

5. **Clean up**:
   ```bash
   make cleanup
   ```

## Examples

See `config/examples/` for complete controller configurations including:
- GCP environment validation
- DNS sub-zone management
- HyperShift cluster creation via Maestro

## Development

Built with:
- Go 1.21+
- Kubernetes controller-runtime
- CLS Controller SDK

See [DESIGN.md](DESIGN.md) for detailed architecture and implementation plan.
