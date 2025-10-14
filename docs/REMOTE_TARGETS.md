# Remote Kubernetes API Targets

The CLS Controller supports creating resources on remote Kubernetes clusters using kubeconfig stored in Kubernetes secrets. **✅ This feature is fully implemented, tested, and production-ready.**

## Overview

Remote targets allow you to:
- Create resources on different Kubernetes clusters from a single controller
- Manage multi-cluster deployments
- Separate control plane and workload clusters
- Implement hub-and-spoke cluster architectures
- Use different authentication methods (Workload Identity, service account keys, tokens)

**Implementation Status:** ✅ Complete with full authentication support, client caching, and comprehensive error handling.

## Authentication Methods

The controller supports multiple authentication methods for connecting to remote clusters. Choose the method that best fits your security requirements and infrastructure setup.

### 1. OAuth2 Access Token (Recommended for GKE)

Best for: **GKE clusters with Workload Identity**

```yaml
# Kubeconfig with direct OAuth2 token
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://CLUSTER_IP
    insecure-skip-tls-verify: true  # For IP-based access
  name: remote-cluster
users:
- name: token-user
  user:
    token: ya29.a0AQQ_BDS21rdDbOkoh...  # OAuth2 access token
```

**Pros:** Temporary credentials, integrates with Google Cloud IAM
**Cons:** Tokens expire (typically 1 hour), requires token refresh mechanism

### 2. Workload Identity (Recommended for Production)

Best for: **GKE-to-GKE authentication in production**

```yaml
# Kubeconfig using Workload Identity (no credentials stored)
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://CLUSTER_IP
    insecure-skip-tls-verify: true
  name: remote-cluster
users:
- name: workload-identity
  user: {}  # Empty - relies on pod's service account
```

**Setup Requirements:**
1. Enable Workload Identity on both clusters
2. Create Google Service Account with GKE cluster access
3. Bind Kubernetes Service Account to Google Service Account
4. Configure proper IAM roles

**Pros:** No stored credentials, automatic token refresh, enterprise-grade security
**Cons:** Requires proper Workload Identity setup

### 3. Service Account Key File

Best for: **Development and testing environments**

```yaml
# Kubeconfig with service account key execution
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTi...
    server: https://CLUSTER_IP
  name: remote-cluster
users:
- name: service-account-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: gcloud
      args:
      - auth
      - activate-service-account
      - --key-file=/path/to/service-account-key.json
      - --format=value(access_token)
      - &&
      - gcloud
      - auth
      - print-access-token
```

**Pros:** Works everywhere, doesn't require Workload Identity setup
**Cons:** Long-lived credentials, requires key file management

### 4. Standard Kubeconfig Authentication

Best for: **Non-GKE clusters or custom setups**

```yaml
# Standard kubeconfig with client certificates or other auth
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTi...
    server: https://api.cluster.example.com
  name: remote-cluster
users:
- name: cluster-user
  user:
    client-certificate-data: LS0tLS1CRUdJTi...
    client-key-data: LS0tLS1CRUdJTi...
```

**Pros:** Standard Kubernetes authentication, works with any cluster
**Cons:** Certificate management overhead

## Setup

### 1. Prepare the Remote Cluster Kubeconfig

First, you need a kubeconfig file with appropriate permissions for the remote cluster:

```bash
# Example: Create a service account and kubeconfig for the remote cluster
kubectl create serviceaccount cls-remote-controller -n default
kubectl create clusterrolebinding cls-remote-controller \
  --clusterrole=cluster-admin \
  --serviceaccount=default:cls-remote-controller

# Get the service account token and create kubeconfig
# (Note: This is a simplified example - use proper RBAC in production)
```

### 2. Create the Secret with Kubeconfig

Store the kubeconfig in a Kubernetes secret in the same namespace as your ControllerConfig (typically `cls-system`):

```bash
# Create secret from kubeconfig file
kubectl create secret generic remote-cluster-kubeconfig \
  --from-file=kubeconfig=/path/to/remote-kubeconfig.yaml \
  -n cls-system

# Or create secret from literal data
kubectl create secret generic remote-cluster-kubeconfig \
  --from-literal=kubeconfig="$(cat /path/to/remote-kubeconfig.yaml)" \
  -n cls-system
```

### 3. Create ControllerConfig with Remote Target

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: my-remote-controller
  namespace: cls-system
spec:
  name: "my-remote-controller"
  description: "Creates resources on remote clusters"

  # Configure remote target
  target:
    type: "kube-api"
    kubeConfig:
      secretRef:
        name: "remote-cluster-kubeconfig"  # Secret name
        key: "kubeconfig"                  # Key within secret

  resources:
    - name: "remote-resource"
      template: |
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: "{{.cluster.name}}-config"
          namespace: "production"  # Created on remote cluster
        data:
          cluster_id: "{{.cluster.id}}"
```

## Configuration Reference

### Target Configuration

```yaml
target:
  type: "kube-api"           # Required: Must be "kube-api" for remote clusters
  kubeConfig:                # Required for remote clusters
    secretRef:
      name: "secret-name"    # Required: Name of secret containing kubeconfig
      key: "kubeconfig"      # Required: Key within secret containing kubeconfig data
```

### Secret Requirements

The secret must:
- Be in the same namespace as the ControllerConfig
- Contain a valid kubeconfig in the specified key
- Have the kubeconfig formatted as standard YAML

### Resource Namespace Behavior

When using remote targets:
- **Secret location**: Read from ControllerConfig's namespace (e.g., `cls-system`)
- **Resource creation**: Uses `metadata.namespace` from the rendered template
- **Target cluster**: Resources created on the remote cluster specified in the kubeconfig

## Examples

### Multi-Cluster Application Deployment

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: multi-cluster-app
  namespace: cls-system
spec:
  name: "multi-cluster-app"

  target:
    type: "kube-api"
    kubeConfig:
      secretRef:
        name: "workload-cluster-kubeconfig"
        key: "kubeconfig"

  preconditions:
    - field: "spec.environment"
      operator: "eq"
      value: "production"

  resources:
    - name: "app-namespace"
      template: |
        apiVersion: v1
        kind: Namespace
        metadata:
          name: "{{.cluster.name}}"

    - name: "app-deployment"
      template: |
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: "{{.cluster.name}}-app"
          namespace: "{{.cluster.name}}"
        spec:
          replicas: 3
          selector:
            matchLabels:
              app: "{{.cluster.name}}-app"
          template:
            metadata:
              labels:
                app: "{{.cluster.name}}-app"
            spec:
              containers:
              - name: app
                image: "{{.cluster.spec.app_image}}"
```

### Cross-Cluster Service Mesh Setup

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: service-mesh-remote
  namespace: cls-system
spec:
  name: "service-mesh-remote"

  target:
    type: "kube-api"
    kubeConfig:
      secretRef:
        name: "mesh-cluster-kubeconfig"
        key: "kubeconfig"

  resources:
    - name: "mesh-config"
      template: |
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: "cluster-{{.cluster.id}}-mesh-config"
          namespace: "istio-system"
        data:
          cluster_name: "{{.cluster.name}}"
          cluster_network: "{{.cluster.spec.network_id}}"

    - name: "mesh-secret"
      template: |
        apiVersion: v1
        kind: Secret
        metadata:
          name: "cluster-{{.cluster.id}}-certs"
          namespace: "istio-system"
        type: Opaque
        data:
          root-cert.pem: "{{.cluster.spec.root_cert | base64encode}}"
```

## Security Considerations

### RBAC for Remote Clusters

Create minimal RBAC for the remote cluster service account:

```yaml
# On the remote cluster
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cls-controller-remote
rules:
- apiGroups: [""]
  resources: ["namespaces", "configmaps", "secrets"]
  verbs: ["get", "list", "create", "update", "patch", "delete"]
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list", "create", "update", "patch", "delete"]
# Add other resources as needed by your templates

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cls-controller-remote
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cls-controller-remote
subjects:
- kind: ServiceAccount
  name: cls-remote-controller
  namespace: default
```

### Secret Management

- Store kubeconfig secrets securely
- Rotate kubeconfig credentials regularly
- Use namespace isolation for different environments
- Consider using external secret management systems

### Network Security

- Ensure controller can reach remote cluster API servers
- Use TLS/SSL for all connections
- Consider VPN or private networking for sensitive environments

## Troubleshooting

### Common Issues and Solutions

#### 1. Secret not found
```
Error: failed to get secret cls-system/remote-cluster-kubeconfig
```
**Solutions:**
- Verify secret exists in correct namespace: `kubectl get secret -n cls-system`
- Check secret name spelling in ControllerConfig
- Ensure controller has RBAC permissions to read secrets

#### 2. Invalid kubeconfig
```
Error: invalid kubeconfig in secret cls-system/remote-cluster-kubeconfig key kubeconfig
```
**Solutions:**
- Validate kubeconfig syntax: `kubectl --kubeconfig=./kubeconfig get nodes`
- Check YAML formatting and indentation
- Verify base64 encoding if storing as data vs stringData
- Test kubeconfig outside the controller first

#### 3. SSL Certificate validation errors
```
Error: x509: certificate signed by unknown authority
```
**Solutions:**
- Use `insecure-skip-tls-verify: true` for IP-based cluster access
- Ensure controller image has proper CA certificates (use distroless base image)
- For DNS-based access, verify certificate chain is complete

#### 4. Permission denied on remote cluster
```
Error: failed to create resource: forbidden
Permission 'container.googleapis.com/clusters.connect' denied
```
**Solutions:**
- Check RBAC permissions on remote cluster
- For GKE: Ensure service account has `container.clusterAdmin` role
- Verify IAM bindings: `gcloud projects get-iam-policy PROJECT_ID`
- Use direct cluster IP endpoint if permission issues persist

#### 5. Authentication as system:anonymous
```
Error: User "system:anonymous" cannot create resource
```
**Solutions:**
- Verify authentication method is working
- Check that OAuth2 tokens are not expired
- For Workload Identity: verify service account binding
- Test authentication manually: `kubectl --kubeconfig=./kubeconfig auth whoami`

#### 6. Network connectivity issues
```
Error: failed to create remote client: connection refused
Error: dial tcp: lookup cluster.example.com: no such host
```
**Solutions:**
- Check network connectivity to remote cluster
- Verify API server endpoint is accessible from controller pod
- For private clusters: ensure controller can reach private IP
- Test connectivity: `telnet CLUSTER_IP 443`

#### 7. DNS endpoint issues
```
Error: failed to resolve cluster DNS name
```
**Solutions:**
- Enable DNS endpoint on GKE cluster: `gcloud container clusters update CLUSTER --enable-private-endpoint-subnetwork`
- Use direct IP addresses if DNS resolution fails
- Configure CoreDNS for cross-cluster name resolution

#### 8. Token expiration (OAuth2 method)
```
Error: token has expired
```
**Solutions:**
- Implement token refresh mechanism
- Use Workload Identity for automatic token refresh
- Monitor token expiration and refresh proactively
- Consider using service account keys for long-running workloads

#### 9. Resource creation failures
```
Error: failed to create resource: namespace does not exist
```
**Solutions:**
- Ensure target namespaces exist on remote cluster
- Create namespace as part of resource template
- Check resource dependencies and creation order
- Verify resource YAML is valid for target Kubernetes version

#### 10. Client caching issues
```
Error: connection reset by peer after long idle
```
**Solutions:**
- Restart controller to refresh cached clients
- Implement client health checking and cache invalidation
- Configure appropriate client timeouts
- Monitor client connection health

### Debugging

Enable debug logging to troubleshoot remote target issues:

```bash
# Check controller logs
kubectl logs -n cls-system deployment/cls-controller -f

# Look for client manager debug messages
kubectl logs -n cls-system deployment/cls-controller -f | grep "client-manager"
```

Check secret contents:

```bash
# Verify secret exists and has correct key
kubectl get secret remote-cluster-kubeconfig -n cls-system -o yaml

# Decode and validate kubeconfig
kubectl get secret remote-cluster-kubeconfig -n cls-system -o jsonpath='{.data.kubeconfig}' | base64 -d | kubectl --kubeconfig=/dev/stdin get nodes
```

## Performance Considerations

### Client Caching

The controller caches remote Kubernetes clients to improve performance:
- Clients are cached by secret reference (`secretName/keyName`)
- Cache persists for the lifetime of the controller process
- No automatic cache invalidation (restart controller to refresh)

### Resource Creation

- Each remote target requires a separate API call
- Multiple resources to the same remote cluster reuse the cached client
- Consider batching operations for better performance

## Production Deployment Best Practices

### Container Image Requirements

Use distroless base images to ensure proper SSL certificate validation:

```dockerfile
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /workspace/cls-controller/controller /controller
```

**Why:** Remote cluster connections require proper CA certificate bundles for SSL verification.

### Workload Identity Setup for GKE

1. **Create Google Service Account:**
```bash
gcloud iam service-accounts create cls-controller-remote-access \
  --display-name="CLS Controller Remote Access"
```

2. **Grant cluster access permissions:**
```bash
gcloud projects add-iam-policy-binding PROJECT_ID \
  --member="serviceAccount:cls-controller-remote-access@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/container.clusterAdmin"
```

3. **Bind to Kubernetes Service Account:**
```bash
gcloud iam service-accounts add-iam-policy-binding \
  cls-controller-remote-access@PROJECT_ID.iam.gserviceaccount.com \
  --role="roles/iam.workloadIdentityUser" \
  --member="serviceAccount:cls-system:cls-controller"
```

4. **Annotate Kubernetes Service Account:**
```bash
kubectl annotate serviceaccount cls-controller \
  -n cls-system \
  iam.gke.io/gcp-service-account=cls-controller-remote-access@PROJECT_ID.iam.gserviceaccount.com
```

### DNS vs IP-based Access

#### DNS Endpoint (Recommended)
- Enable DNS endpoints: `gcloud container clusters update CLUSTER --enable-dns-endpoint`
- Use cluster FQDN in kubeconfig: `https://cluster-dns-name.googleapis.com`
- Better certificate validation and security

#### IP-based Access (Fallback)
- Use cluster IP directly: `https://34.61.142.124`
- Add `insecure-skip-tls-verify: true` to cluster config
- Required when DNS endpoints are not available

### Secret Management

Create secrets using `stringData` for better readability:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: remote-kubeconfig
  namespace: cls-system
type: Opaque
stringData:
  config: |
    apiVersion: v1
    kind: Config
    # ... rest of kubeconfig
```

### Controller Deployment Configuration

Example deployment with proper resource limits and security:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cls-controller-remote-cluster
  namespace: cls-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cls-controller-remote-cluster
  template:
    metadata:
      labels:
        app: cls-controller-remote-cluster
    spec:
      serviceAccountName: cls-controller
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        fsGroup: 65532
      containers:
      - name: controller
        image: gcr.io/PROJECT/cls-controller:latest
        env:
        - name: CONTROLLER_CONFIG_NAME
          value: "remote-cluster-management"
        - name: CONTROLLER_CONFIG_NAMESPACE
          value: "cls-system"
        - name: ORGANIZATION_DOMAIN
          value: "redhat.com"
        resources:
          limits:
            cpu: 500m
            memory: 512Mi
          requests:
            cpu: 100m
            memory: 128Mi
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: true
          capabilities:
            drop: ["ALL"]
```

## Real-World Examples

### Example 1: Multi-Cluster Namespace Management

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: remote-cluster-management
  namespace: cls-system
spec:
  name: "remote-cluster-management"
  description: "Manages resources on remote clusters"

  target:
    type: "kube-api"
    kubeConfig:
      secretRef:
        name: "shard01"
        key: "config"

  resources:
    - name: "remote-namespace"
      template: |
        apiVersion: v1
        kind: Namespace
        metadata:
          name: "cluster-{{.cluster.id}}"
          labels:
            managed-by: cls-controller
            cluster-id: "{{.cluster.id}}"

    - name: "cluster-config"
      template: |
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: "cluster-config"
          namespace: "cluster-{{.cluster.id}}"
        data:
          cluster_id: "{{.cluster.id}}"
          cluster_name: "{{.cluster.name}}"
          organization: "{{.cluster.organization_domain}}"

    - name: "monitoring-deployment"
      template: |
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: "cluster-monitoring"
          namespace: "cluster-{{.cluster.id}}"
        spec:
          replicas: 1
          selector:
            matchLabels:
              app: cluster-monitoring
          template:
            metadata:
              labels:
                app: cluster-monitoring
            spec:
              containers:
              - name: monitor
                image: "gcr.io/PROJECT/cluster-monitor:latest"
                env:
                - name: CLUSTER_ID
                  value: "{{.cluster.id}}"

  statusConditions:
    - name: "Applied"
      status: "True"
      reason: "RemoteResourcesCreated"
      message: "Created remote cluster resources successfully"
```

### Example 2: Cross-Cluster Service Mesh Setup

Useful for configuring service mesh components across multiple clusters:

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: service-mesh-remote
  namespace: cls-system
spec:
  name: "service-mesh-remote"
  description: "Configure service mesh on remote clusters"

  target:
    type: "kube-api"
    kubeConfig:
      secretRef:
        name: "mesh-cluster-kubeconfig"
        key: "config"

  resources:
    - name: "mesh-namespace"
      template: |
        apiVersion: v1
        kind: Namespace
        metadata:
          name: "istio-system"
          labels:
            istio-injection: disabled

    - name: "cluster-secret"
      template: |
        apiVersion: v1
        kind: Secret
        metadata:
          name: "cluster-{{.cluster.id}}-secret"
          namespace: "istio-system"
        type: Opaque
        data:
          cluster_id: "{{.cluster.id | base64encode}}"
          cluster_endpoint: "{{.cluster.spec.api_endpoint | base64encode}}"
```

## Migration from Local to Remote

To migrate existing ControllerConfigs from local to remote targets:

1. Create kubeconfig secret for the target cluster
2. Update ControllerConfig to add `target` configuration
3. Test with a single resource first
4. Monitor logs for any permission or connectivity issues
5. Gradually migrate all resources

Example migration:

```yaml
# Before (local target)
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: my-controller
spec:
  # No target specified = local cluster
  resources:
    - name: "local-resource"
      template: |
        # ... resource template

# After (remote target)
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: my-controller
spec:
  target:
    type: "kube-api"
    kubeConfig:
      secretRef:
        name: "remote-kubeconfig"
        key: "kubeconfig"
  resources:
    - name: "remote-resource"
      template: |
        # ... same resource template (now created on remote cluster)
```