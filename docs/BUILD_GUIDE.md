# CLS Controller Build Guide

This document provides the complete, tested build process for the cls-controller application using podman.

## Prerequisites

- Go 1.21+ installed
- Podman installed and configured
- GCR authentication configured
- Registry auth file at `/Users/asegundo/.config/containers/auth.json`
- cls-controller-sdk in parent directory (`../cls-controller-sdk`)

## Quick Build Commands (Working Process)

### Option 1: Using Makefile (Recommended)

```bash
# Build and push in one command
cd cls-controller
make build push

# Or use the full development workflow
make dev
```

### Option 2: Manual Build Process

#### 1. Authenticate with Google Container Registry

```bash
# IMPORTANT: Authenticate podman with GCR first
gcloud auth print-access-token | /opt/podman/bin/podman login -u oauth2accesstoken --password-stdin gcr.io
```

#### 2. Build Container Image with Podman

```bash
# Build from parent directory (required for SDK dependency)
cd /Users/asegundo/git/gcp

# Build for linux/amd64 (required for GKE)
REGISTRY_AUTH_FILE=/Users/asegundo/.config/containers/auth.json \
  /opt/podman/bin/podman build \
  --platform linux/amd64 \
  -f cls-controller/Dockerfile.build \
  -t gcr.io/apahim-dev-1/cls-controller:org-events-$(date +%Y%m%d%H%M%S) \
  .
```

#### 3. Push to Google Container Registry

```bash
# Push to GCR (after authentication)
REGISTRY_AUTH_FILE=/Users/asegundo/.config/containers/auth.json \
  /opt/podman/bin/podman push \
  gcr.io/apahim-dev-1/cls-controller:org-events-$(date +%Y%m%d%H%M%S)
```

#### 4. Update Kubernetes Deployment

```bash
# Update deployment with new image
kubectl set image deployment/cls-controller \
  cls-controller=gcr.io/apahim-dev-1/cls-controller:org-events-$(date +%Y%m%d%H%M%S) \
  -n cls-system
```

## Build Process Details

### Container Build Stages

The build uses a multi-stage Dockerfile (`Dockerfile.build`):

1. **Builder Stage** (`golang:1.21-alpine`):
   - Installs build dependencies (git, ca-certificates, tzdata)
   - Copies cls-controller-sdk dependency from parent directory
   - Copies cls-controller source code
   - Downloads Go dependencies with `go mod download`
   - Runs `go mod tidy` to ensure clean module state
   - Compiles the binary with static linking: `CGO_ENABLED=0 GOOS=linux`
   - Produces optimized binary at `/workspace/cls-controller/controller`

2. **Runtime Stage** (`scratch`):
   - Minimal runtime environment with ca-certificates and timezone data
   - Non-root user (UID 65534)
   - Copies only the static binary
   - No shell or runtime dependencies

### Key Build Parameters

- **Platform**: `linux/amd64` (required for GKE nodes)
- **Registry Auth**: Uses service account credentials via REGISTRY_AUTH_FILE
- **Static Binary**: `CGO_ENABLED=0` for scratch container compatibility
- **Optimized**: `-ldflags="-w -s"` strips debug info and symbol table
- **Build Context**: Must run from parent directory to include cls-controller-sdk

### Directory Structure Requirements

```
/Users/asegundo/git/gcp/
├── cls-controller/          # Controller source code
│   ├── cmd/controller/main.go
│   ├── internal/
│   ├── Dockerfile.build     # Multi-stage build file
│   └── go.mod
├── cls-controller-sdk/      # Required dependency
│   ├── api_client.go
│   ├── types.go
│   └── go.mod
└── [build from here]        # ← podman build context
```

## Latest Changes (Organization from Events)

### Architecture Update (2025-10-13)

The cls-controller has been updated to extract organization context from Pub/Sub events instead of using hard-coded configuration:

**Key Changes:**
- ✅ **Event-Driven Organization**: Organization comes from `event.OrganizationDomain` field
- ✅ **Multi-Tenant API Support**: Uses organization-scoped cls-backend endpoints
- ✅ **No Configuration Required**: No need to set `ORGANIZATION_DOMAIN` environment variables
- ✅ **Backward Compatibility**: Maintains existing API client interfaces

**Event Structure:**
```json
{
  "type": "cluster.created",
  "cluster_id": "cluster-uuid",
  "organization_domain": "redhat.com",
  "generation": 1
}
```

**API Flow:**
```
Event → Extract org → cls-backend API call using organization context
cluster.created (org: redhat.com) → GET /organizations/redhat-com/clusters/{id}
```

## Common Issues and Solutions

### Issue 1: Build Context Error

**Error**: `COPY cls-controller-sdk/ cls-controller-sdk/` fails with "no such file or directory"

**Root Cause**: Building from wrong directory - SDK must be in parent directory

**Solution**: Always build from parent directory:
```bash
# WRONG - building from cls-controller directory
cd cls-controller
podman build -f Dockerfile.build .

# CORRECT - building from parent directory
cd /Users/asegundo/git/gcp
podman build -f cls-controller/Dockerfile.build .
```

### Issue 2: GCR Push Authentication Failures

**Error**: `unauthorized: authentication failed` when pushing to GCR

**Root Cause**: Podman needs active authentication with Google Container Registry

**Solution**: Always authenticate before building/pushing:
```bash
# Step 1: Authenticate podman with GCR using gcloud token
gcloud auth print-access-token | /opt/podman/bin/podman login -u oauth2accesstoken --password-stdin gcr.io

# Step 2: Verify login succeeded (should show "Login Succeeded!")

# Step 3: Then build and push normally
```

### Issue 3: Platform Compatibility

**Error**: Images fail to run on GKE (arm64 vs amd64)

**Solution**: Always specify `--platform linux/amd64` for GKE compatibility

### Issue 4: Go Module Issues

**Error**: `go mod download` or `go mod tidy` fails during build

**Root Cause**: Module dependency issues or network problems

**Solution**: Test locally first:
```bash
cd cls-controller
go mod download
go mod tidy
go build -o controller cmd/controller/main.go
```

### Issue 5: Missing SDK Dependency

**Error**: Build fails with import errors for `github.com/apahim/controller-sdk`

**Root Cause**: cls-controller-sdk not in expected location

**Solution**: Ensure SDK is in parent directory:
```bash
ls ../cls-controller-sdk/  # Should show SDK files
```

## Build Environment

### Dockerfile Analysis

```dockerfile
# Stage 1: Builder (golang:1.21-alpine)
FROM golang:1.21-alpine AS builder
RUN apk add --no-cache git ca-certificates tzdata
WORKDIR /workspace

# Copy dependencies first (SDK from parent directory)
COPY cls-controller-sdk/ cls-controller-sdk/
COPY cls-controller/ cls-controller/

# Set working directory to cls-controller
WORKDIR /workspace/cls-controller

# Download dependencies and ensure clean state
RUN go mod download
RUN go mod tidy

# Build the controller
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o controller \
    cmd/controller/main.go

# Stage 2: Runtime (scratch)
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /workspace/cls-controller/controller /controller
USER 65534:65534
ENTRYPOINT ["/controller"]
```

### Build Output Verification

Successful build output shows:
```
[1/2] STEP 9/9: RUN CGO_ENABLED=0 GOOS=linux...
--> 4ff3784995ca
[2/2] STEP 6/6: ENTRYPOINT ["/controller"]
[2/2] COMMIT gcr.io/apahim-dev-1/cls-controller:org-events-20251013211216
--> 609cc4ccaf47
Successfully tagged gcr.io/apahim-dev-1/cls-controller:org-events-20251013211216
```

## Image Tag Strategy

### Recommended Tagging Convention

- **Development**: `org-events-YYYYMMDD-HHMMSS` (e.g., `org-events-20251013211216`)
- **Feature**: `FEATURE-YYYYMMDD` (e.g., `multi-tenant-20251013`)
- **Testing**: `test-FEATURE-YYYYMMDD` (e.g., `test-versioned-20251013`)
- **Production**: `v1.x.x` or `latest` (only for verified releases)

### Example Tags Used

- `org-events-20251013211216` - Latest organization-from-events implementation
- `newgenerationonly-20251013135800` - Version management improvements
- `complete-implementation` - Feature complete version

## Performance Considerations

### Build Time Optimization

- **Go Module Cache**: `go mod download` step is cached between builds
- **Multi-stage Build**: Only final binary copied to runtime image
- **Static Binary**: No runtime dependencies required
- **Layer Caching**: Docker layers cached for repeated builds

### Image Size

- **Builder Image**: ~1.5GB (golang:1.21-alpine with dependencies)
- **Runtime Image**: ~42MB (scratch + binary + certificates + timezone data)
- **Final Size**: Optimized for fast deployment and low resource usage

## Troubleshooting Checklist

Before building, verify:

1. **Working Directory**: `pwd` should be `/Users/asegundo/git/gcp` (parent directory)
2. **SDK Dependency**: `ls ../cls-controller-sdk/` shows SDK files
3. **Code Compilation**: `cd cls-controller && go build ./cmd/controller/main.go` succeeds locally
4. **Module State**: `go mod tidy` runs without errors
5. **Authentication**: `podman login gcr.io` status is valid

### Quick Local Test

```bash
# Test Go compilation locally first
cd cls-controller
go build -o controller cmd/controller/main.go

# Test binary execution (should show help or version)
./controller --help

# If successful, proceed with container build
# If failed, fix Go errors first before containerizing
```

## Deployment Integration

### Makefile Integration

The project includes a Makefile with podman build support:

```bash
# Build using Makefile (handles authentication and context automatically)
make build PROJECT_ID=apahim-dev-1

# Or build and deploy in one command
make dev PROJECT_ID=apahim-dev-1
```

### Update Kubernetes Manifests

After successful build, update deployment files:

```yaml
# deployments/deployment.yaml
spec:
  template:
    spec:
      containers:
      - name: cls-controller
        image: gcr.io/apahim-dev-1/cls-controller:org-events-20251013211216  # UPDATE TAG
```

### Rolling Update

```bash
# Apply updated manifests
kubectl apply -f deployments/deployment.yaml -n cls-system

# Monitor rollout
kubectl rollout status deployment/cls-controller -n cls-system

# Verify new pods
kubectl get pods -n cls-system -l app.kubernetes.io/name=cls-controller
```

## Integration with cls-backend

### API Compatibility

The cls-controller integrates with cls-backend's organization-scoped APIs:

- **Cluster Fetch**: `GET /organizations/{org_id}/clusters/{cluster_id}`
- **Status Report**: `PUT /organizations/{org_id}/clusters/{cluster_id}/status`
- **Organization Mapping**: `redhat.com` → `redhat-com`

### Event Flow

```
1. Pub/Sub Event → Controller receives event with organization_domain
2. Organization Extraction → event.OrganizationDomain (e.g., "redhat.com")
3. API Call → GET /organizations/redhat-com/clusters/{id}
4. Resource Processing → Create/update Kubernetes resources
5. Status Report → PUT /organizations/redhat-com/clusters/{id}/status
```

---

**Last Updated**: 2025-10-13
**Verified Working**: ✅ Build process tested and documented
**Latest Image**: `gcr.io/apahim-dev-1/cls-controller:org-events-20251013211216`
**Next Build**: Use this exact procedure for consistent results