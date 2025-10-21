# CLAUDE.md - cls-controller Project Guide

## Project Overview
The `cls-controller` is a configurable Kubernetes controller that creates resources in response to cluster events. It supports multiple deployment targets (local K8s, remote clusters, Maestro API) with template-driven resource creation and automatic token refresh for Workload Identity.

## Key Architecture Components
- **Entry point**: `cmd/controller/` - main application
- **Core logic**: `internal/controller/` - controller implementation
- **Client management**: `internal/client/manager.go` - handles K8s clients with OAuth2 transport
- **Template engine**: `internal/template/` - renders Kubernetes resources
- **SDK**: `internal/sdk/` - backend API interactions
- **CRDs**: `internal/crd/` - ControllerConfig custom resource definitions

## Critical Issues Solved

### 1. Workload Identity Token Refresh Issue ✅
**Problem**: Controller failed after ~1 hour with "server has asked for the client to provide credentials"
**Root Cause**: Using static tokens that expire, cached clients reused expired tokens
**Solution**: Implemented OAuth2 transport with automatic token refresh

**Key Changes in `internal/client/manager.go:308-388`**:
```go
// OLD (problematic):
token, err := tokenSource.Token()
config.BearerToken = token.AccessToken

// NEW (fixed):
transport := &oauth2.Transport{
    Source: oauth2.ReuseTokenSource(nil, tokenSource),
    Base:   baseTransport,
}
config.Transport = transport
```

### 2. TLS Transport Conflict Issue ✅
**Problem**: "using a custom transport with TLS certificate options or the insecure flag is not allowed"
**Root Cause**: Can't set both custom `Transport` and `TLSClientConfig` in REST config
**Solution**: Move TLS configuration into the transport layer

**Key Changes**:
```go
// Configure TLS in transport (not REST config)
baseTransport := http.DefaultTransport.(*http.Transport).Clone()
baseTransport.TLSClientConfig = &tls.Config{
    RootCAs: caCertPool, // or InsecureSkipVerify: true
}

// Use transport for both OAuth2 and TLS
transport := &oauth2.Transport{
    Source: oauth2.ReuseTokenSource(nil, tokenSource),
    Base:   baseTransport,
}
```

### 3. CA Certificate Mismatch Issue ✅
**Problem**: "x509: certificate signed by unknown authority"
**Root Cause**: Using CA certificate from wrong cluster
**Solution**: Extract CA certificate directly from target cluster endpoint

## Workload Identity Secret Management

### Current Working Secret Configuration
- **Secret name**: `remote-gke-cluster`
- **Namespace**: `cls-system`
- **Required keys**: `endpoint`, `ca-cert`
- **Controller expects**: CA cert under `ca-cert` key (exact name)

### Quick Secret Creation Commands

#### For Known Workload Identity Endpoint:
```bash
ENDPOINT="https://gke-b4a8996f0f7e4bd5a1271fc6e39bf045ff6e-231185119761.us-central1-a.gke.goog"
kubectl create secret generic remote-gke-cluster -n cls-system \
  --from-literal=endpoint="$ENDPOINT" \
  --from-literal=ca-cert="$(echo | openssl s_client -servername $(echo $ENDPOINT | cut -d'/' -f3) -connect $(echo $ENDPOINT | cut -d'/' -f3):443 -showcerts 2>/dev/null | awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/ {if(++count==2) print; if(count>2) print}')" \
  --dry-run=client -o yaml | kubectl apply -f -
```

#### For Standard GKE Clusters (from kubectl config):
```bash
CLUSTER_NAME="gke_PROJECT_REGION_CLUSTER"
kubectl create secret generic remote-gke-cluster -n cls-system \
  --from-literal=endpoint="$(kubectl config view --raw -o jsonpath="{.clusters[?(@.name==\"$CLUSTER_NAME\")].cluster.server}")" \
  --from-literal=ca-cert="$(kubectl config view --raw -o jsonpath="{.clusters[?(@.name==\"$CLUSTER_NAME\")].cluster.certificate-authority-data}" | base64 -d)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

#### Verification Commands:
```bash
# Check secret contents
kubectl get secret remote-gke-cluster -n cls-system -o jsonpath='{.data.endpoint}' | base64 -d
kubectl get secret remote-gke-cluster -n cls-system -o jsonpath='{.data.ca-cert}' | base64 -d | openssl x509 -subject -noout

# Verify CA cert length (should be >2000 characters)
kubectl get secret remote-gke-cluster -n cls-system -o jsonpath='{.data.ca-cert}' | wc -c
```

## Build and Deployment

### Building the Project:
```bash
# Local build test
go build ./internal/client
go test ./internal/client -v
go build ./...

# Container build (takes time due to dependencies)
make build
# Creates: gcr.io/apahim-dev-1/cls-controller:TIMESTAMP
```

### Running Tests:
```bash
# Client package tests (most relevant for auth issues)
go test ./internal/client -v

# All tests
go test ./... -v
```

## Debugging Guide

### Common Error Patterns:

#### 1. Token Expiry (Fixed):
```
"error":"failed to create resource: the server has asked for the client to provide credentials"
```
**Check**: OAuth2 transport implementation in `manager.go:310-350`

#### 2. TLS Certificate Issues:
```
"tls: failed to verify certificate: x509: certificate signed by unknown authority"
```
**Check**:
- Secret has correct `ca-cert` key
- CA certificate matches the endpoint
- TLS config is in transport, not REST config

#### 3. CA Certificate Missing:
```
"No CA certificate provided, using insecure TLS connection in transport"
```
**Check**:
- Secret exists in correct namespace
- `ca-cert` key is not empty
- Base64 encoding is correct

### Log Messages to Expect:

#### Success Messages:
```
"Successfully created Workload Identity client with automatic token refresh"
"Using provided CA certificate for TLS verification in transport"
"Workload Identity client created with OAuth2 transport - tokens will auto-refresh"
```

#### Debug Information:
- Token refresh happens automatically every ~55 minutes
- Client caching works by secret key: `wi-{secret-name}`
- TLS verification uses CA certificate from secret

## File Locations

### Key Files:
- `internal/client/manager.go` - Main client management and OAuth2 transport
- `internal/controller/resource_manager.go` - Resource creation logic
- `deployments/` - Kubernetes manifests
- `config/` - Example configurations

### Important Functions:
- `createWorkloadIdentityClient()` - OAuth2 transport setup
- `getWorkloadIdentityClient()` - Client caching and retrieval
- `getClusterInfoFromSecret()` - Secret reading logic

## Dependencies
- `golang.org/x/oauth2` - OAuth2 transport for token refresh
- `k8s.io/client-go` - Kubernetes client
- `sigs.k8s.io/controller-runtime` - Controller framework

## Testing Strategy
1. **Unit tests**: All client package tests pass
2. **Integration**: Build completes successfully
3. **Runtime**: Logs show successful OAuth2 transport creation
4. **Long-term**: No authentication failures after 1+ hours

## Future Considerations
- Monitor token refresh events in logs
- Consider adding metrics for authentication failures
- Document additional auth methods if needed
- Test with different GKE cluster configurations

---
**Last Updated**: October 2025
**Key Achievement**: Workload Identity with automatic token refresh + secure TLS