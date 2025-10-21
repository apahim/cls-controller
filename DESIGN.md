# Generalized cls-controller Implementation Plan

## Overview
Create a generalized, configurable controller that can create Kubernetes resources based on cluster events.

### Architecture Principles

**Event-Driven Execution**: The controller uses Pub/Sub for lightweight event notifications (cluster created, updated, deleted, reconcile). Events trigger immediate action execution.

**API-Based Data Transfer**: All cluster specifications and status updates happen via the cls-backend REST API. The controller fetches cluster specs on-demand using organization context from events and reports status back using organization-aware APIs.

**Template-Driven Resources**: The controller uses Go templates to create either Jobs (for direct work) or Custom Resources (to trigger existing operators like CAPG, CAPI, etc.).

**CRD-Based Configuration**: Controller configurations are defined using a simple CRD that gets loaded once at startup.

**Simple and Secure**: Basic container security and K8s RBAC provide sufficient isolation.

### Multi-Tenant Organization Support

**Organization from Events**: The controller extracts organization context dynamically from incoming Pub/Sub cluster events rather than using hard-coded configuration. Each cluster event includes an `organization_domain` field that provides the organization context for API calls.

**Event-Driven Organization Resolution**:
- Cluster events contain `organization_domain` field (e.g., "redhat.com")
- Controller uses this organization context for cls-backend API calls
- No organization configuration required in controller deployment
- Supports multiple organizations automatically

**API Integration**: The controller uses organization-aware cls-backend APIs:
- `GET /organizations/{org_id}/clusters/{cluster_id}` - Fetch cluster specs
- `POST /organizations/{org_id}/clusters/{cluster_id}/status` - Report status
- Organization ID derived from organization domain using kebab-case conversion

This approach enables:
- **Dynamic multi-tenancy**: Same controller handles multiple organizations
- **No hard-coded configuration**: Organization comes from event data
- **Simplified deployment**: No organization-specific environment variables needed
- **Automatic scaling**: New organizations supported without controller changes

## 1. Simple Security Model

### Basic Security Principles
- **Container Security**: All Jobs run with `runAsNonRoot`, `readOnlyRootFilesystem`, and dropped capabilities
- **RBAC**: Controllers have minimal permissions - create Jobs, read cluster specs, write status
- **Secret Management**: Secrets mounted as volumes, never in environment variables
- **Network Isolation**: Jobs run in isolated namespaces with network policies (optional)

### Template Security
Templates use standard Go template syntax with basic functions:
- String manipulation: `join`, `split`, `replace`, `trim`, `lower`, `upper`, `substr`
- Encoding: `toJson`, `fromJson`, `base64encode`, `base64decode`
- Default values: `default`
- Random generation: `randomString <length>` - generates random alphanumeric string (e.g., `{{randomString 4}}` → `abcd`)
- Substring extraction: `substr <start> <length> <string>` - extracts substring (e.g., `{{substr 0 8 .cluster.id}}` → first 8 characters)
- Cluster access: `{{.cluster.id}}`, `{{.cluster.spec.region}}`, etc.

## 2. Simple CRD Configuration

### ControllerConfig CRD
A minimal CRD that defines what Job to run for cluster events.

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: controllerconfigs.cls.redhat.com
spec:
  group: cls.redhat.com
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            required: ["name", "resources"]
            properties:
              name:
                type: string
                description: "Controller name"
              description:
                type: string
                description: "What this controller does"

              # Optional: Configure where resources are created (local kube-api, remote kube-api, or maestro)
              # ✅ IMPLEMENTED: Remote kube-api targets with kubeconfig secrets (production-ready)
              target:
                type: object
                properties:
                  type:
                    type: string
                    enum: ["kube-api", "maestro"]
                    default: "kube-api"
                    description: "Target type for resource creation"
                  kubeConfig:
                    type: object
                    description: "Kubeconfig for remote kube-api (✅ fully implemented with client caching)"
                    properties:
                      secretRef:
                        type: object
                        required: ["name", "key"]
                        properties:
                          name:
                            type: string
                            description: "Secret name containing kubeconfig (supports all authentication methods)"
                          key:
                            type: string
                            description: "Key within secret containing kubeconfig"
                  maestroConfig:
                    type: object
                    description: "Maestro configuration (templatable)"
                    properties:
                      endpoint:
                        type: string
                        description: "Templated maestro gRPC endpoint"
                      consumer:
                        type: string
                        description: "Templated maestro consumer ID"

              # Optional: Only process events that match these conditions
              preconditions:
                type: array
                description: "List of conditions that must all be true"
                items:
                  type: object
                  required: ["field", "operator", "value"]
                  properties:
                    field:
                      type: string
                      description: "Cluster field path (e.g., 'spec.provider', 'status', 'spec.region')"
                    operator:
                      type: string
                      enum: ["eq", "ne", "in", "notin", "exists", "notexists"]
                      description: "Comparison operator"
                    value:
                      description: "Value to compare against (string, array for in/notin)"
                example:
                  - field: "spec.provider"
                    operator: "eq"
                    value: "gcp"
                  - field: "spec.region"
                    operator: "in"
                    value: ["us-east1", "us-west1"]

              # List of Kubernetes resources to create when an event matches
              resources:
                type: array
                description: "List of Kubernetes resources to create"
                items:
                  type: object
                  required: ["name", "template"]
                  properties:
                    name:
                      type: string
                      description: "Unique name for this resource within the controller (e.g., 'rendered-config', 'transport-job')"
                    description:
                      type: string
                      description: "Optional description of what this resource does"
                    template:
                      type: string
                      description: "Kubernetes resource YAML template"
                    resourceManagement:
                      type: object
                      description: "Resource-specific update and cleanup strategies"
                      properties:
                        updateStrategy:
                          type: string
                          enum: ["in_place", "versioned"]
                          default: "in_place"
                          description: "How to handle resource updates"
                        versionInterval:
                          type: string
                          default: "5m"
                          description: "Minimum time between creating new versions (only applies when updateStrategy is 'versioned'). New versions are only created if this interval has elapsed since the last version, unless generation changes"
                        newGenerationOnly:
                          type: boolean
                          default: false
                          description: "When set to true ensures new versioned resources are only created when the cluster generation changes, ignoring the versionInterval time-based constraint"
                        keepVersions:
                          type: integer
                          default: 2
                          description: "Number of completed versions to keep for versioned resources. Older completed versions beyond this count will be deleted automatically"

              # Multiple status conditions to report based on resource status
              statusConditions:
                type: array
                description: "Status conditions to report to cls-backend"
                items:
                  type: object
                  required: ["name", "status", "reason", "message"]
                  properties:
                    name:
                      type: string
                      description: "Condition name (e.g., 'Ready', 'Progressing', 'Available')"
                    status:
                      type: string
                      description: "Template expression for condition status (True/False/Unknown)"
                    reason:
                      type: string
                      description: "Template expression for condition reason"
                    message:
                      type: string
                      description: "Template expression for condition message"


          status:
            type: object
            properties:
              phase:
                type: string
                enum: ["Valid", "Invalid"]
              message:
                type: string
  scope: Namespaced
  names:
    plural: controllerconfigs
    singular: controllerconfig
    kind: ControllerConfig
```

## 3. How It Works

### Simple Event Processing Flow
1. **Event Received**: Controller gets cluster event (created/updated/deleted/reconcile) with organization_domain
2. **Organization Extraction**: Extract organization context from event for multi-tenant API calls
3. **Fetch Cluster Spec**: Get current cluster spec from cls-backend API using organization context (includes generation)
4. **Precondition Check**: Evaluate optional preconditions against cluster spec
5. **Status Reporting**: Always report status using organization-aware APIs, even if preconditions fail
6. **Resource Creation**: Render and create Kubernetes resource from template (if preconditions pass)
7. **Status Check**: Read current resource status
8. **Report Status**: Send current status back to cls-backend with observedGeneration using organization context

### Precondition Failure Handling

When preconditions are not met, the controller still reports status to provide visibility:

```go
func (c *Controller) reportPreconditionFailure(cluster *Cluster) error {
    failedPreconditions := c.getFailedPreconditions(cluster)

    status := &StatusReport{
        ClusterID:           cluster.ID,
        ControllerName:      c.config.Name,
        ObservedGeneration:  cluster.Generation,
        Conditions: []Condition{
            {
                Name:    "Applied",
                Status:  "False",
                Reason:  "PreconditionsNotMet",
                Message: fmt.Sprintf("Preconditions not met: %s", strings.Join(failedPreconditions, ", ")),
            },
        },
        ResourceStatus: nil, // No resource created
        Timestamp:      time.Now(),
    }

    return c.apiClient.ReportStatus(ctx, status)
}
```

#### Example: DNS Sub-Zone Controller Precondition Failure

**Cluster that fails preconditions**:
```json
{
  "id": "cluster-456",
  "name": "development-cluster",
  "organization_domain": "example.com",
  "generation": 10,
  "spec": {
    "provider": "aws",  // Not GCP
    "infrastructure_type": "hypershift"
    // Missing: dns_management_enabled field
  }
}
```

**Status Report**:
```json
{
  "cluster_id": "cluster-456",
  "controller_name": "hypershift-dns-subzone",
  "observed_generation": 10,
  "conditions": [
    {
      "name": "Applied",
      "status": "False",
      "reason": "PreconditionsNotMet",
      "message": "Preconditions not met: spec.provider must equal 'gcp' (got 'aws'), spec.dns_management_enabled must exist"
    }
  ],
  "resource_status": null,
  "timestamp": "2024-01-15T10:30:00Z"
}
```

This approach ensures:
- **Complete visibility**: cls-backend knows the controller evaluated the cluster
- **Clear debugging**: Users understand exactly why the controller didn't act
- **Consistent reporting**: Every controller evaluation results in a status report

### Precondition Examples
```yaml
# Only process GCP clusters
preconditions:
  - field: "spec.provider"
    operator: "eq"
    value: "gcp"

# Only process ready clusters in specific regions
preconditions:
  - field: "status"
    operator: "eq"
    value: "ready"
  - field: "spec.region"
    operator: "in"
    value: ["us-east1", "us-west1"]

# Only process clusters that have monitoring enabled
preconditions:
  - field: "spec.features.monitoring"
    operator: "eq"
    value: true

# Only process clusters where backup field exists
preconditions:
  - field: "spec.backup"
    operator: "exists"

# Multiple providers except Azure
preconditions:
  - field: "spec.provider"
    operator: "notin"
    value: ["azure"]
```

### Supported Operators
Both preconditions and completion detection use the same operators:
- **eq**: Field equals value
- **ne**: Field not equals value
- **in**: Field value is in array
- **notin**: Field value is not in array
- **exists**: Field exists (any value)
- **notexists**: Field does not exist

### Unified Syntax: Preconditions and Completion Detection

**Consistency Achievement**: Both systems now use identical syntax for field evaluation:

**Preconditions** (evaluating cluster data from cls-backend):
```yaml
preconditions:
  - field: "spec.provider"
    operator: "eq"
    value: "gcp"
  - field: "status.assignedConsumer"
    operator: "exists"
```

**Completion Detection** (evaluating resource status from Kubernetes):
```yaml
completionDetection:
  - field: "status.conditions[?(@.type=='Complete')].status"
    operator: "eq"
    value: "True"
  - field: "status.failed"
    operator: "eq"
    value: 0
```

**Benefits of Unified Syntax**:
- **Same operators**: `eq`, `ne`, `in`, `notin`, `exists`, `notexists` work identically
- **Same structure**: Array of field evaluations with field/operator/value pattern
- **Same logic**: All conditions must be true (AND logic)
- **Same flexibility**: Support for nested paths and complex field access
- **No special cases**: Everything is just field evaluation with operators


### Status Condition Examples
```yaml
# Job-based controller
statusConditions:
  - name: "Applied"
    status: "True"
    reason: "JobCreated"
    message: "Job {{.resources.validation-job.metadata.name}} has been created"

  - name: "Available"
    status: "{{$complete := false}}{{$failed := false}}{{range .resources.validation-job.status.conditions}}{{if eq .type \"Complete\"}}{{if eq .status \"True\"}}{{$complete = true}}{{end}}{{end}}{{if eq .type \"Failed\"}}{{if eq .status \"True\"}}{{$failed = true}}{{end}}{{end}}{{end}}{{if $complete}}True{{else if $failed}}False{{else}}Unknown{{end}}"
    reason: "{{$complete := false}}{{$failed := false}}{{range .resources.validation-job.status.conditions}}{{if eq .type \"Complete\"}}{{if eq .status \"True\"}}{{$complete = true}}{{end}}{{end}}{{if eq .type \"Failed\"}}{{if eq .status \"True\"}}{{$failed = true}}{{end}}{{end}}{{end}}{{if $complete}}JobSucceeded{{else if $failed}}JobFailed{{else}}JobRunning{{end}}"
    message: "{{$complete := false}}{{$failed := false}}{{range .resources.validation-job.status.conditions}}{{if eq .type \"Complete\"}}{{if eq .status \"True\"}}{{$complete = true}}{{end}}{{end}}{{if eq .type \"Failed\"}}{{if eq .status \"True\"}}{{$failed = true}}{{end}}{{end}}{{end}}Job {{if $complete}}completed successfully{{else if $failed}}failed{{else}}is running with {{.resources.validation-job.status.active}} active pods{{end}}"

# Config Connector DNS sub-zone controller
statusConditions:
  - name: "Applied"
    status: "True"
    reason: "DNSManagedZoneCreated"
    message: "DNS managed zone has been created"

  - name: "Available"
    status: "{{$ready := \"Unknown\"}}{{if .resources.dns-subzone.status.conditions}}{{range .resources.dns-subzone.status.conditions}}{{if eq .type \"Ready\"}}{{$ready = .status}}{{end}}{{end}}{{end}}{{$ready}}"
    reason: "{{$reason := \"DNSZonePending\"}}{{if .resources.dns-subzone.status.conditions}}{{range .resources.dns-subzone.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}{{$reason = \"DNSZoneReady\"}}{{else}}{{$reason = \"DNSZoneNotReady\"}}{{end}}{{end}}{{end}}{{end}}{{$reason}}"
    message: "{{$message := \"DNS sub-zone is being created\"}}{{if .resources.dns-subzone.status.conditions}}{{range .resources.dns-subzone.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}{{$message = \"DNS sub-zone is ready for delegation\"}}{{else}}{{$message = printf \"DNS sub-zone: %s\" .message}}{{end}}{{end}}{{end}}{{end}}{{$message}}"

  - name: "Healthy"
    status: "{{if .resources.dns-subzone.status.observedGeneration}}{{if eq .resources.dns-subzone.metadata.generation .resources.dns-subzone.status.observedGeneration}}True{{else}}False{{end}}{{else}}Unknown{{end}}"
    reason: "{{if .resources.dns-subzone.status.observedGeneration}}{{if eq .resources.dns-subzone.metadata.generation .resources.dns-subzone.status.observedGeneration}}DNSZoneSynced{{else}}DNSZoneOutOfSync{{end}}{{else}}DNSZoneSyncPending{{end}}"
    message: "DNS sub-zone {{if .resources.dns-subzone.status.observedGeneration}}{{if eq .resources.dns-subzone.metadata.generation .resources.dns-subzone.status.observedGeneration}}is in sync with desired state{{else}}is being updated to match desired state{{end}}{{else}}sync status is unknown{{end}}"

# HyperShift HostedCluster controller
statusConditions:
  - name: "Applied"
    status: "True"
    reason: "HostedClusterCreated"
    message: "HostedCluster resource has been created"

  - name: "Available"
    status: "{{if .resources.hosted-cluster.status.conditions}}Unknown{{else}}Unknown{{end}}"
    reason: "ClusterProvisioning"
    message: "HostedCluster is being provisioned - check resource status for details"
```

### ObservedGeneration Tracking
All status reports must include the `observedGeneration` from the cluster spec to ensure status corresponds to the correct cluster version.

### Available Template Context
Templates have access to:
- `.resources` - Map of all created Kubernetes resources with their current status (e.g., `.resources.dns-subzone`, `.resources.maestro-transport`)
- `.cluster` - The original cluster spec from cls-backend (includes `.generation`, `.organization_domain`)
- `.controller` - Controller metadata (name, type, etc.)
- `.timestamp` - Unix timestamp for unique resource naming (e.g., `1704067200`)
- `randomString <length>` - Function to generate random alphanumeric strings (e.g., `{{randomString 4}}` → `abcd`)

**Note**: The `.resources` map contains the live state of all Kubernetes resources created by the controller, allowing status conditions to access each resource's current status, metadata, and spec fields using the resource name as the key (e.g., `.resources.dns-subzone.status.conditions`).

### Reconciliation-Based Status Reporting

The controller operates on a simple principle: **report what you see right now**. There's no waiting or watching - cls-backend handles scheduling reconciliation events to check progress.

#### Event-Driven Flow
1. **cluster.created**: Create resource, report initial status
2. **cluster.reconcile**: Check resource status, report current state
3. **cluster.reconcile**: (repeat) Check resource status, report current state
4. **cluster.reconcile**: Resource completed → report final success/failure

#### Status Progression Examples

**Successful Path**:
```
Event 1 (created):  Create Job → Report "Applied: True, Available: Unknown"
Event 2 (reconcile): Job running → Report "Applied: True, Available: Unknown"
Event 3 (reconcile): Job completed → Report "Applied: True, Available: True"
```

**Precondition Failure Path**:
```
Event 1 (created):  Preconditions fail → Report "Applied: False, reason: PreconditionsNotMet"
Event 2 (reconcile): Preconditions still fail → Report "Applied: False, reason: PreconditionsNotMet"
Event 3 (updated): Cluster spec fixed, preconditions pass → Create Job → Report "Applied: True, Available: Unknown"
```

**Resource Creation Failure Path**:
```
Event 1 (created):  Template render fails → Report "Applied: False, reason: TemplateRenderFailed"
Event 2 (reconcile): Permission denied → Report "Applied: False, reason: ResourceCreationFailed"
Event 3 (reconcile): Permissions fixed → Create Job → Report "Applied: True, Available: Unknown"
```

**Waiting for Completion Path (Versioned Strategy)**:
```
Event 1 (created):  Create Job gen-42 → Report "Applied: True, Available: Unknown, observedGeneration: 42"
Event 2 (reconcile): Job running → Report "Applied: True, Available: Unknown, observedGeneration: 42"
Event 3 (updated): New generation 43, old job still running → Report "Applied: False, reason: WaitingForCompletion, observedGeneration: 43"
Event 4 (reconcile): Still waiting → Report "Applied: False, reason: WaitingForCompletion, observedGeneration: 43"
Event 5 (reconcile): Old job completed, new job created → Report "Applied: True, Available: Unknown, observedGeneration: 43"
```

The cls-backend scheduler determines how often to send reconcile events based on the resource type and expected completion time.

### Waiting for Completion Status Reporting

When using `waitForCompletion: true` in versioned strategy, the controller provides clear visibility into waiting states:

#### Status Conditions During Waiting

**Key Status Fields**:
- **observedGeneration**: Always set to the NEW generation (the one being processed)
- **Applied**: "False" when waiting, "True" after resource created
- **reason**: Specific reason codes for different waiting states
- **message**: Human-readable explanation of what's happening

#### Common Waiting States

**WaitingForCompletion**: Normal waiting state
```json
{
  "cluster_id": "cluster-123",
  "controller_name": "gcp-environment-validation",
  "observed_generation": 43,
  "conditions": [
    {
      "name": "Applied",
      "status": "False",
      "reason": "WaitingForCompletion",
      "message": "Waiting for previous generation resource 'gcp-validate-cluster-123-gen-42-001' to complete before creating generation 43 resource"
    }
  ],
  "resources": {
    "validation-job": {
      "status": "Running",
      "resource_status": {
        // Status of the OLD generation resource (gen-42) that we're waiting for
        "generation": 1,
        "conditions": [
          {
            "type": "Complete",
            "status": "False"
          }
        ],
        "active": 1,
        "succeeded": 0
      }
    }
  }
}
```

**CompletionTimeout**: Timeout expired, forced cleanup
```json
{
  "conditions": [
    {
      "name": "Applied",
      "status": "False",
      "reason": "CompletionTimeout",
      "message": "Completion timeout (600s) expired, force deleted previous generation resource and created new one"
    }
  ]
}
```

#### Implementation Logic

```go
func (c *Controller) HandleClusterEvent(event *ClusterEvent) error {
    cluster, err := c.apiClient.GetCluster(ctx, event.ClusterID)
    if err != nil {
        return err
    }

    // Check if we're waiting for previous generation to complete
    if c.isWaitingForCompletion(cluster) {
        return c.reportWaitingStatus(cluster)
    }

    // Normal processing...
    resources, err := c.getOrCreateAllResources(cluster)
    // ...
}

func (c *Controller) reportWaitingStatus(cluster *Cluster) error {
    // Get the old generation resource we're waiting for
    oldResource, err := c.findPreviousGenerationResource(cluster)
    if err != nil {
        return err
    }

    status := &StatusReport{
        ClusterID:          cluster.ID,
        ObservedGeneration: cluster.Generation, // NEW generation
        Conditions: []Condition{
            {
                Name:    "Applied",
                Status:  "False",
                Reason:  "WaitingForCompletion",
                Message: fmt.Sprintf("Waiting for previous generation resource '%s' to complete", oldResource.GetName()),
            },
        },
        Resources: map[string]ResourceStatus{
            c.resourceName: {
                Status:         "Running", // Status of OLD resource
                ResourceStatus: oldResource.Object["status"],
            },
        },
    }

    return c.apiClient.ReportStatus(ctx, status)
}
```

This approach ensures:
- **Complete visibility**: Users know exactly why their new generation isn't applied yet
- **Clear observedGeneration**: Always reflects the generation being processed
- **Resource status continuity**: Shows status of the resource we're waiting for
- **Actionable information**: Users can see timeout settings and current wait time

## 3a. Controller Target Configuration

The controller supports multiple target types for resource creation, allowing flexible deployment scenarios from local Kubernetes clusters to remote clusters via Maestro.

### Target Types

#### 1. **Local Kube-API** (Default)
```yaml
# No target configuration needed - defaults to local cluster
spec:
  resources:
    - name: "dns-zone"
      template: |
        apiVersion: dns.cnrm.cloud.google.com/v1beta1
        kind: DNSManagedZone
        # ...
```

#### 2. **Remote Kube-API** ✅ **Production Ready**
```yaml
spec:
  target:
    type: "kube-api"
    kubeConfig:
      secretRef:
        name: "remote-cluster-kubeconfig"  # Supports OAuth2, Workload Identity, service account keys
        key: "kubeconfig"

  resources:
    - name: "remote-resource"
      template: |
        apiVersion: v1
        kind: Namespace
        # ... Creates on remote cluster using cached client connections
```

**Authentication Support:**
- OAuth2 access tokens (GKE with Workload Identity)
- Service account key files
- Direct token authentication
- Standard kubeconfig authentication

#### 3. **Maestro API** (Placement-Driven)
```yaml
spec:
  target:
    type: "maestro"
    maestroConfig:
      endpoint: "{{.cluster.status.maestroEndpoint}}"
      consumer: "{{.cluster.status.assignedConsumer}}"

  preconditions:
    - field: "status.assignedConsumer"
      operator: "exists"
    - field: "status.maestroEndpoint"
      operator: "exists"

  resources:
    - name: "hosted-cluster"
      template: |
        apiVersion: hypershift.openshift.io/v1beta1
        kind: HostedCluster
        # ...
```

### Target Selection Benefits

1. **Unified Interface**: Same `.resources.resource-name.status` regardless of target type
2. **Template-Driven**: Maestro endpoints and consumers come from placement decisions
3. **Precondition-Driven**: Natural dependencies ensure placement data exists
4. **Flexible Deployment**: Mix local, remote, and maestro targets across controllers
5. **Consistent Status**: Status conditions work identically across all target types

### Template Context for Maestro Targets

When using Maestro targets, templates have access to placement decision status:
- `.cluster.status.assignedConsumer` - Consumer ID from placement controller
- `.cluster.status.maestroEndpoint` - Maestro gRPC endpoint from placement controller
- `.cluster.status.hostingCluster` - Hosting cluster identifier (optional)
- `.cluster.status.region` - Target region (optional)

### Client Selection and Management

✅ **Fully Implemented:** The controller automatically selects and manages the appropriate client based on the target configuration, providing a unified interface regardless of the underlying target type.

**Implementation Features:**
- Client caching by secret reference for performance
- Automatic secret reading and kubeconfig parsing
- Support for all major authentication methods
- Error handling and connection management
- Namespace-aware secret reading

#### Client Types and Selection Logic

```go
type ClientManager struct {
    localClient   client.Client       // Default local Kubernetes client
    remoteClients map[string]client.Client  // Cached remote kube clients by secret key
    maestroClients map[string]*maestro.Client  // Cached Maestro clients by endpoint
    secretClient  client.Client       // Client for reading kubeconfig secrets
    logger        *zap.Logger
}

// GetClient returns the appropriate client based on target configuration
func (cm *ClientManager) GetClient(ctx context.Context, target *TargetConfig, cluster *Cluster) (ResourceClient, error) {
    switch target.Type {
    case "kube-api", "":  // Default to kube-api
        return cm.getKubeAPIClient(ctx, target, cluster)
    case "maestro":
        return cm.getMaestroClient(ctx, target, cluster)
    default:
        return nil, fmt.Errorf("unsupported target type: %s", target.Type)
    }
}

// getKubeAPIClient returns local or remote Kubernetes client
func (cm *ClientManager) getKubeAPIClient(ctx context.Context, target *TargetConfig, cluster *Cluster) (client.Client, error) {
    // No kubeConfig specified - use local cluster
    if target.KubeConfig == nil {
        return cm.localClient, nil
    }

    // Remote cluster - get client from kubeconfig secret
    secretKey := fmt.Sprintf("%s/%s", target.KubeConfig.SecretRef.Name, target.KubeConfig.SecretRef.Key)

    // Check cache first
    if client, exists := cm.remoteClients[secretKey]; exists {
        return client, nil
    }

    // Create new remote client from secret
    kubeconfig, err := cm.getKubeConfigFromSecret(ctx, target.KubeConfig.SecretRef)
    if err != nil {
        return nil, fmt.Errorf("failed to get kubeconfig from secret: %w", err)
    }

    remoteClient, err := cm.createRemoteClient(kubeconfig)
    if err != nil {
        return nil, fmt.Errorf("failed to create remote client: %w", err)
    }

    // Cache the client
    cm.remoteClients[secretKey] = remoteClient
    return remoteClient, nil
}

// getMaestroClient returns Maestro gRPC client with templated configuration
func (cm *ClientManager) getMaestroClient(ctx context.Context, target *TargetConfig, cluster *Cluster) (*maestro.Client, error) {
    // Render templated endpoint and consumer
    endpoint, err := cm.renderTemplate(target.MaestroConfig.Endpoint, cluster)
    if err != nil {
        return nil, fmt.Errorf("failed to render maestro endpoint: %w", err)
    }

    consumer, err := cm.renderTemplate(target.MaestroConfig.Consumer, cluster)
    if err != nil {
        return nil, fmt.Errorf("failed to render maestro consumer: %w", err)
    }

    clientKey := fmt.Sprintf("%s/%s", endpoint, consumer)

    // Check cache first
    if client, exists := cm.maestroClients[clientKey]; exists {
        return client, nil
    }

    // Create new Maestro client
    maestroClient, err := maestro.NewClient(&maestro.Config{
        Endpoint: endpoint,
        Consumer: consumer,
        TLS:      cm.getTLSConfig(), // Configure TLS from controller config
    })
    if err != nil {
        return nil, fmt.Errorf("failed to create maestro client: %w", err)
    }

    // Cache the client
    cm.maestroClients[clientKey] = maestroClient
    return maestroClient, nil
}
```

#### Unified Resource Client Interface

The controller abstracts different target types behind a common interface:

```go
type ResourceClient interface {
    Create(ctx context.Context, resource *unstructured.Unstructured) error
    Update(ctx context.Context, resource *unstructured.Unstructured) error
    Get(ctx context.Context, name, namespace string) (*unstructured.Unstructured, error)
    Delete(ctx context.Context, resource *unstructured.Unstructured) error
    List(ctx context.Context, namespace string, labels map[string]string) (*unstructured.UnstructuredList, error)
}

// KubeAPIClient wraps Kubernetes client.Client
type KubeAPIClient struct {
    client.Client
}

// MaestroClient wraps Maestro gRPC client and converts to Kubernetes-like interface
type MaestroClient struct {
    *maestro.Client
    consumer string
}

// Create implements ResourceClient for Maestro
func (mc *MaestroClient) Create(ctx context.Context, resource *unstructured.Unstructured) error {
    manifestWork := &maestro.ManifestWork{
        Consumer:  mc.consumer,
        Resources: []*unstructured.Unstructured{resource},
    }

    _, err := mc.Client.CreateManifestWork(ctx, manifestWork)
    return err
}

// Get implements ResourceClient for Maestro
func (mc *MaestroClient) Get(ctx context.Context, name, namespace string) (*unstructured.Unstructured, error) {
    manifestWork, err := mc.Client.GetManifestWork(ctx, &maestro.GetRequest{
        Consumer:  mc.consumer,
        Name:      name,
        Namespace: namespace,
    })
    if err != nil {
        return nil, err
    }

    // Return the resource with status populated from Maestro
    if len(manifestWork.Resources) > 0 {
        resource := manifestWork.Resources[0]
        // Merge status from ManifestWork into resource
        if manifestWork.Status != nil {
            resource.Object["status"] = manifestWork.Status
        }
        return resource, nil
    }

    return nil, fmt.Errorf("resource not found")
}
```

#### Client Lifecycle Management

```go
// Controller initialization
func (c *Controller) setupClientManager() error {
    cm := &ClientManager{
        localClient:    c.k8sClient,
        remoteClients:  make(map[string]client.Client),
        maestroClients: make(map[string]*maestro.Client),
        secretClient:   c.k8sClient, // Use local client to read secrets
        logger:         c.logger,
    }

    c.clientManager = cm
    return nil
}

// Resource operations use the selected client transparently
func (c *Controller) getOrCreateResource(resourceConfig ResourceConfig, cluster *Cluster) (*unstructured.Unstructured, error) {
    // Get appropriate client based on target config
    resourceClient, err := c.clientManager.GetClient(ctx, c.config.Target, cluster)
    if err != nil {
        return nil, fmt.Errorf("failed to get client: %w", err)
    }

    // Render template
    resource, err := c.renderTemplate(resourceConfig.Template, cluster)
    if err != nil {
        return nil, err
    }

    // Check if resource exists
    existing, err := resourceClient.Get(ctx, resource.GetName(), resource.GetNamespace())
    if err != nil {
        // Resource doesn't exist - create it
        if err := resourceClient.Create(ctx, resource); err != nil {
            return nil, fmt.Errorf("failed to create resource: %w", err)
        }
        return resource, nil
    }

    // Resource exists - update if needed
    if c.needsUpdate(existing, resource) {
        if err := resourceClient.Update(ctx, resource); err != nil {
            return nil, fmt.Errorf("failed to update resource: %w", err)
        }
    }

    return existing, nil
}
```

#### Client Caching and Performance

1. **Local Client**: Single instance shared across all controllers
2. **Remote Clients**: Cached by secret reference (`secretName/keyName`)
3. **Maestro Clients**: Cached by endpoint/consumer combination
4. **Connection Pooling**: Maestro clients use gRPC connection pooling
5. **Secret Watching**: Optional secret watching for remote client cache invalidation

#### Error Handling and Fallbacks

```go
func (cm *ClientManager) getMaestroClient(ctx context.Context, target *TargetConfig, cluster *Cluster) (*maestro.Client, error) {
    client, err := cm.createMaestroClient(ctx, target, cluster)
    if err != nil {
        // Log specific error for debugging
        cm.logger.Error("Failed to create Maestro client",
            zap.String("cluster", cluster.ID),
            zap.String("endpoint", target.MaestroConfig.Endpoint),
            zap.Error(err))

        // Return error - no fallback for Maestro (placement decision required)
        return nil, fmt.Errorf("maestro client creation failed: %w", err)
    }

    return client, nil
}

// Health checking for long-lived connections
func (cm *ClientManager) healthCheckClients(ctx context.Context) {
    // Periodically health check cached Maestro connections
    for endpoint, client := range cm.maestroClients {
        if err := client.Health(ctx); err != nil {
            cm.logger.Warn("Maestro client unhealthy, removing from cache",
                zap.String("endpoint", endpoint),
                zap.Error(err))
            delete(cm.maestroClients, endpoint)
        }
    }
}
```

This client selection system provides:
- **Transparent abstraction**: Same resource operations regardless of target type
- **Efficient caching**: Reuse connections across multiple clusters
- **Template-driven configuration**: Dynamic client configuration based on cluster data
- **Error isolation**: Client failures don't affect other target types
- **Performance optimization**: Connection pooling and caching for scalability

## 3b. Resource Update Strategies

The controller supports two distinct update strategies to handle different types of Kubernetes resources and operational requirements:

### Strategy Overview

| Strategy | Use Case | Resource Types | Naming | Cleanup |
|----------|----------|----------------|---------|---------|
| **in_place** | Default for mutable resources | Deployments, ConfigMaps, CRs | Static name | None needed |
| **versioned** | Immutable resources or audit trail | Jobs, Pods, or any resource | Generation + timestamp | Configurable retention |

### When Each Strategy is Used

#### Automatic Strategy Selection
The controller automatically determines the appropriate strategy based on resource type and per-resource configuration:

```go
func (c *Controller) determineUpdateStrategy(resourceConfig ResourceConfig, resourceKind string) string {
    // Force versioned strategy for immutable resources
    if c.isImmutableResourceKind(resourceKind) {
        return "versioned"
    }

    // Use per-resource configured strategy, defaulting to in_place for mutable resources
    if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.UpdateStrategy != "" {
        return resourceConfig.ResourceManagement.UpdateStrategy
    }
    return "in_place"  // Default for mutable resources
}

func (c *Controller) isImmutableResourceKind(kind string) bool {
    immutableKinds := []string{"Job", "Pod"}
    for _, immutable := range immutableKinds {
        if kind == immutable {
            return true
        }
    }
    return false
}
```

#### Strategy Decision Matrix

**`updateStrategy: "in_place"`** (Default):
- ✅ **Best for**: Config Connector resources, Deployments, ConfigMaps, Secrets, Custom Resources
- ✅ **Benefits**: Efficient, maintains resource history, works with operators
- ✅ **Operation**: Updates existing resource spec when cluster generation changes
- ❌ **Not suitable**: Jobs, Pods (immutable resources)

**`updateStrategy: "versioned"`**:
- ✅ **Best for**: Jobs, validation tasks, audit requirements
- ✅ **Benefits**: Complete audit trail, continuous enforcement, rollback capability
- ✅ **Operation**: Creates new resource for each generation with timestamp suffix
- ❌ **Overhead**: Requires cleanup configuration and resource management

### Configuration Examples

#### Simple In-Place Configuration
```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: cluster-dns-zone
spec:
  resources:
    - name: "dns-subzone"
      description: "DNS sub-zone for cluster delegation"
      resourceManagement:
        updateStrategy: "in_place"  # Config Connector resources are mutable
      template: |
        apiVersion: dns.cnrm.cloud.google.com/v1beta1
        kind: DNSManagedZone
        metadata:
          name: "{{.cluster.name}}-dns-zone"  # Same name always
```

#### Versioned Configuration with Cleanup
```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: gcp-validation
spec:
  resources:
    - name: "validation-job"
      description: "GCP environment validation job"
      resourceManagement:
        updateStrategy: "versioned"    # Required for Jobs (immutable resources)
        versionInterval: "5m"          # Wait 5 minutes between recreating completed resources
        newGenerationOnly: false       # Allow time-based recreation within the same generation
        keepVersions: 3                # Keep 3 completed resources total
      template: |
        apiVersion: batch/v1
        kind: Job
        metadata:
          name: "validate-{{.cluster.id}}-gen-{{.cluster.generation}}-{{.timestamp}}"
```

### Strategy Selection Guidelines

1. **Use `in_place` when**:
   - Working with mutable Kubernetes resources (most CRs, Deployments, etc.)
   - Integrating with existing operators (CAPG, CAPI, HyperShift)
   - Want simple configuration with minimal operational overhead
   - Resource state should be preserved across cluster updates

2. **Use `versioned` when**:
   - Working with immutable resources (Jobs, Pods)
   - Need complete audit trail of all operations
   - Want continuous enforcement/validation
   - Working with stateless operations that benefit from recreation

3. **Required for**:
   - Jobs and Pods (automatically enforced - cannot use `in_place`)

### Resource Lifecycle Management

#### Update Strategies
The controller supports two resource update strategies to handle different use cases:

**`updateStrategy: "in_place"` (Default)**:
- **Mutable Resources**: Deployments, ConfigMaps, Secrets, Custom Resources
- **Behavior**: Update existing resource directly when cluster generation changes
- **Benefits**: Efficient, maintains resource history, simpler logic
- **Naming**: Same resource name always (e.g., `gcp-cluster-{cluster.name}`)

**`updateStrategy: "versioned"`**:
- **All Resources**: Creates new resource per generation with timestamp suffix
- **Required for**: Immutable resources like Jobs (automatically enforced)
- **Benefits**: Audit trail, continuous enforcement, rollback capability
- **Naming**: `resource-{cluster-id}-gen-{generation}-{timestamp}`

#### Resource Strategy Selection
```go
func (c *Controller) determineUpdateStrategy(resourceConfig ResourceConfig, resourceKind string) string {
    // Force versioned strategy for immutable resources
    if c.isImmutableResourceKind(resourceKind) {
        return "versioned"
    }

    // Use per-resource configured strategy, defaulting to in_place for mutable resources
    if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.UpdateStrategy != "" {
        return resourceConfig.ResourceManagement.UpdateStrategy
    }
    return "in_place"  // Default for mutable resources
}
```

**Key Principles for Versioned Strategy**:
1. **One active resource per cluster** at any time
2. **Generation-aware cleanup** - configurable old generation cleanup behavior
3. **Continuous enforcement** - recreate completed resources within same generation
4. **Incremental naming** - `resource-{cluster-id}-gen-{generation}-{timestamp}`

#### Generation Transition Behavior

The controller supports versioned resource management with automatic cleanup:

```yaml
resourceManagement:
  updateStrategy: "versioned"
  versionInterval: "5m"        # Minimum time before recreating completed resources
  newGenerationOnly: false     # Allow time-based recreation within same generation
  keepVersions: 2              # Keep 2 completed versions for audit trail
```

**Transition Strategy:**

The controller uses a single, simplified transition strategy:
   ```go
   func (c *Controller) getOrCreateVersionedResource(cluster *Cluster) (*unstructured.Unstructured, error) {
       // 1. Cleanup old generation resources first
       if err := c.cleanupOldGenerations(cluster.ID, cluster.Generation); err != nil {
           return nil, err
       }

       // 2. Check for existing resource in current generation
       currentGenResource, err := c.findResourceForGeneration(cluster.ID, cluster.Generation)
       if err == nil && currentGenResource != nil {
           if c.isResourceRunning(currentGenResource) {
               // Resource is running - return it
               return currentGenResource, nil
           } else {
               // Resource completed - check version interval before recreating
               shouldRecreate, err := c.shouldRecreateVersionedResource(currentGenResource, resourceConfig)
               if !shouldRecreate {
                   return currentGenResource, nil  // Keep existing completed resource
               }
               // Delete completed resource and recreate
               c.k8sClient.Delete(ctx, currentGenResource)
           }
       }

       // 3. Create new versioned resource
       return c.createVersionedResource(cluster)
   }
   ```

#### Generation Transition Examples

**Scenario 1: GCP Validation Job (versioned strategy)**
```
Gen 42 → Gen 43 Transition:
Time 0:  Job validate-cluster-123-gen-42-001 is running
Time 1:  Cluster generation updates to 43
Time 2:  Controller immediately deletes gen-42 job
Time 3:  Create validate-cluster-123-gen-43-001
         Report: Applied=True, Available=Unknown, observedGeneration=43
```

**Scenario 2: DNS Zone (in_place strategy)**
```
Gen 42 → Gen 43 Transition:
Time 0:  DNSManagedZone cluster-dns-zone exists with gen-42 config
Time 1:  Cluster generation updates to 43
Time 2:  Update DNSManagedZone cluster-dns-zone spec immediately (same resource)
         Report: Applied=True, Available=depends on Config Connector status, observedGeneration=43
```

**Scenario 3: Maestro HostedCluster (in_place strategy)**
```
Gen 42 → Gen 43 Transition:
Time 0:  HostedCluster production-west exists with gen-42 spec
Time 1:  Cluster generation updates to 43
Time 2:  Update HostedCluster production-west spec via Maestro API immediately
         Report: Applied=True, Available=depends on HostedCluster status, observedGeneration=43
```

**Scenario 4: Validation Job with Version Interval**
```
Within Generation 42 (same generation, time-based recreation):
Time 0:   Job validate-cluster-123-gen-42-001 completes successfully
Time 1:   Reconcile event received, job completed less than 5 minutes ago
          Report: Applied=True, Available=True, observedGeneration=42 (keeps existing job)
Time 6:   Reconcile event received, job completed more than 5 minutes ago (versionInterval elapsed)
Time 7:   Delete completed job, create validate-cluster-123-gen-42-002
          Report: Applied=True, Available=Unknown, observedGeneration=42
```

#### Resource Completion Detection

The controller automatically detects completion for common resource types:

**Versioned Strategy Use Cases:**
1. **Immutable Resources**: Jobs, Pods (forced by API)
2. **Audit Trail**: Any resource where you need full operation history
3. **Compliance**: Regulatory requirements for change tracking
4. **Continuous Enforcement**: Resources that should be recreated regularly
5. **Rollback Capability**: Easy rollback to previous generations

#### Built-in Completion Detection

The controller has built-in completion detection for common resource types:

```go
// isResourceRunning checks if a resource is still running (not completed/failed)
func (c *Controller) isResourceRunning(resource *unstructured.Unstructured) bool {
    kind := resource.GetKind()

    switch kind {
    case "Job":
        return c.isJobRunning(resource)
    case "Pod":
        return c.isPodRunning(resource)
    default:
        // For other resources, consider them "running" if they exist
        // This prevents immediate recreation of non-completion-based resources
        return true
    }
}

// isJobRunning checks if a Job is still running
func (c *Controller) isJobRunning(job *unstructured.Unstructured) bool {
    // Check job status conditions for completion or failure
    conditions, found, err := unstructured.NestedSlice(job.Object, "status", "conditions")
    if err != nil || !found {
        return true // No status yet - consider it running
    }

    for _, condition := range conditions {
        conditionMap, ok := condition.(map[string]interface{})
        if !ok { continue }

        conditionType, _ := conditionMap["type"]
        conditionStatus, _ := conditionMap["status"]

        // Check for completion or failure
        if conditionType == "Complete" && conditionStatus == "True" {
            return false // Job completed
        }
        if conditionType == "Failed" && conditionStatus == "True" {
            return false // Job failed
        }
    }
    return true // Still running
}

// isPodRunning checks if a Pod is still running
func (c *Controller) isPodRunning(pod *unstructured.Unstructured) bool {
    phase, found, err := unstructured.NestedString(pod.Object, "status", "phase")
    if err != nil || !found {
        return true // No status yet - consider it running
    }

    // Pod is not running if it's succeeded or failed
    return phase != "Succeeded" && phase != "Failed"
}
```

#### Versioned Strategy Examples

**Example 1: Job with Automatic Completion Detection**
```yaml
resources:
  - name: "validation-job"
    description: "Validation job with automatic completion detection"
    resourceManagement:
      updateStrategy: "versioned"      # Required for Jobs (immutable resources)
      versionInterval: "5m"            # Wait 5 minutes before recreating completed jobs
      newGenerationOnly: false         # Allow time-based recreation within same generation
      keepVersions: 2                  # Keep 2 completed jobs for audit
```

**Example 2: ConfigMap for Audit Trail**
```yaml
resources:
  - name: "cluster-config"
    description: "Versioned cluster configuration for audit trail"
    resourceManagement:
      updateStrategy: "versioned"      # For audit trail
      versionInterval: "0s"            # Immediate recreation (no waiting)
      newGenerationOnly: true          # Only create new versions on generation changes
      keepVersions: 10                 # Keep 10 versions for audit
```

**Example 3: Custom Resource with Versioned Strategy**
```yaml
resources:
  - name: "backup-job"
    description: "Custom backup resource"
    resourceManagement:
      updateStrategy: "versioned"      # For continuous enforcement
      versionInterval: "1h"            # Recreate completed backups every hour
      newGenerationOnly: false         # Allow time-based recreation
      keepVersions: 3                  # Keep 3 completed backup jobs
```

#### Resource Management Logic

**In-Place Strategy**:
```go
func (c *Controller) getOrUpdateResource(cluster *Cluster) (*unstructured.Unstructured, error) {
    resourceName := c.generateResourceName(cluster)

    existing := &unstructured.Unstructured{}
    err := c.k8sClient.Get(ctx, resourceName, existing)

    if err == nil {
        // Resource exists - update it with new cluster spec
        newResource, err := c.renderTemplate(cluster)
        if err != nil {
            return nil, err
        }

        existing.Object["spec"] = newResource.Object["spec"]
        existing.SetLabels(newResource.GetLabels())

        return existing, c.k8sClient.Update(ctx, existing)
    }

    // Create new resource
    return c.createResource(cluster)
}
```

**Versioned Strategy**:
```go
func (c *Controller) getOrCreateVersionedResource(cluster *Cluster) (*unstructured.Unstructured, error) {
    // 1. Cleanup old generation resources first
    if err := c.cleanupOldGenerations(cluster.ID, cluster.Generation); err != nil {
        return nil, err
    }

    // 2. Check for existing resource in current generation
    currentGenResource, err := c.findResourceForGeneration(cluster.ID, cluster.Generation)
    if err != nil {
        return nil, err
    }

    if currentGenResource != nil {
        if c.isResourceRunning(currentGenResource) {
            // Resource is running - report its status
            return currentGenResource, nil
        } else {
            // Resource completed/failed - delete and recreate for enforcement
            if err := c.k8sClient.Delete(ctx, currentGenResource); err != nil {
                return nil, fmt.Errorf("failed to delete completed resource: %w", err)
            }
        }
    }

    // 3. Create new resource with incremental naming
    return c.createVersionedResource(cluster)
}
```

#### Versioned Resource Naming Strategy
Versioned resources use incremental naming to avoid conflicts while maintaining clear generation tracking:

```
validate-cluster-123-gen-42-1704067200  # First resource for generation 42
validate-cluster-123-gen-42-1704067800  # Second resource after first completed
validate-cluster-123-gen-43-1704068400  # First resource for generation 43
```

This provides:
- **Clear generation tracking** via the name
- **Audit trail** of when resources were created
- **No naming conflicts** from rapid recreation
- **Simple cleanup logic** based on generation labels

#### Event Flow Examples

**In-Place Strategy** (e.g., DNSRecordSet):
```
Gen 1 (created)   → Create dns-record-abc → Report status
Gen 1 (reconcile) → Update dns-record-abc → Report status
Gen 2 (updated)   → Update dns-record-abc spec → Report status
Gen 2 (reconcile) → Check dns-record-abc status → Report status
```

**Versioned Strategy** (e.g., Jobs):
```
Gen 1 (created)   → Create resource-gen-1-001 → Report status
Gen 1 (reconcile) → resource-gen-1-001 running → Report status (no action)
Gen 1 (reconcile) → resource-gen-1-001 completed → Delete → Create resource-gen-1-002
Gen 2 (updated)   → Delete resource-gen-1-002 → Create resource-gen-2-001 → Report status
```

### Simple Resource Cleanup (Versioned Strategy Only)

#### Cleanup Philosophy
When using `updateStrategy: "versioned"`, the controller supports simple, predictable cleanup:

1. **Keep N completed resources** - Simple count across all generations
2. **Automatic cleanup of old generations** - Clean slate for each generation transition
3. **Time-based recreation** - Configurable interval for recreating completed resources
4. **Generation-aware recreation** - Control whether to recreate only on generation changes

**Note**: Cleanup policies only apply to versioned strategy. In-place strategy maintains a single resource per cluster, so no cleanup is needed.

#### Simple Cleanup Configuration Examples

**Job with Time-based Recreation**:
```yaml
resourceManagement:
  updateStrategy: "versioned"
  versionInterval: "5m"            # Wait 5 minutes before recreating completed resources
  newGenerationOnly: false         # Allow time-based recreation within same generation
  keepVersions: 3                  # Keep 3 completed resources total
```

**Audit Trail ConfigMap**:
```yaml
resourceManagement:
  updateStrategy: "versioned"
  versionInterval: "0s"            # Immediate recreation (no waiting)
  newGenerationOnly: true          # Only recreate on generation changes
  keepVersions: 10                 # Keep 10 versions for audit
```

#### Simple Cleanup Implementation
```go
func (c *Controller) cleanupCompletedVersions(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, clusterID string, currentGeneration int64, kind, namespace string) error {
    // Get keepVersions setting (default to 2 if not specified)
    keepVersions := 2
    if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.KeepVersions > 0 {
        keepVersions = resourceConfig.ResourceManagement.KeepVersions
    }

    // List all resources for this cluster and generation
    labels := map[string]string{
        "cluster-id":         clusterID,
        "cluster-generation": fmt.Sprintf("%d", currentGeneration),
    }

    resourceList := &unstructured.UnstructuredList{}
    err := resourceClient.List(ctx, namespace, labels, resourceList)
    if err != nil {
        return fmt.Errorf("failed to list resources for version cleanup: %w", err)
    }

    if len(resourceList.Items) <= keepVersions {
        return nil // No cleanup needed
    }

    // Filter to only completed resources and sort by creation time (newest first)
    var completedResources []*unstructured.Unstructured
    for i := range resourceList.Items {
        resource := &resourceList.Items[i]
        if !c.isResourceRunning(resource) {
            completedResources = append(completedResources, resource)
        }
    }

    if len(completedResources) <= keepVersions {
        return nil // No cleanup needed
    }

    // Delete old completed resources beyond keepVersions limit
    resourcesToDelete := completedResources[keepVersions:]
    for _, resource := range resourcesToDelete {
        if err := resourceClient.Delete(ctx, resource); err != nil {
            c.logger.Warn("Failed to delete old completed version", zap.Error(err))
            // Continue cleanup - don't fail on individual deletion errors
        }
    }

    return nil
}
```

### Two-Layer Status Reporting

The controller reports status at two levels:

#### 1. Controller Conditions (Interpreted)
These are the controller's interpretation of what's happening:
- **Applied**: "True" when resource is created, "False" if creation failed
- **Available**: Controller's assessment of resource readiness based on statusConditions
- **Healthy**: Optional condition for more complex health checks

#### 2. Resource Status (Raw)
The complete, unfiltered status from the actual Kubernetes resource's `.status` field:
- **Job**: `.status` with conditions, active/succeeded counts, completion time
- **Config Connector DNSManagedZone**: `.status` with conditions, observedGeneration, and `.observedState` (actual GCP resource state)
- **HostedCluster**: `.status` with version info, kubeconfig, conditions

**Config Connector Note**: The `.status.observedState` field contains the actual state of the GCP resource as reported by Google Cloud APIs, providing real-time visibility into the cloud resource status.

This gives cls-backend both:
- **Structured view**: Via controller conditions for consistent interpretation
- **Raw data**: Via resource status for detailed debugging and custom logic

#### Generation Tracking in Resource Status

The `resource_status` includes both generation fields for complete state visibility:

- **`generation`**: Current resource generation (`.metadata.generation`) - increments when resource spec changes
- **`observed_generation`**: What the resource's controller has processed (`.status.observedGeneration`)

**Generation Scenarios**:
```
generation = observed_generation: Resource controller is up-to-date
generation > observed_generation: Resource controller is processing changes
No observed_generation field: Resource doesn't support generation tracking (e.g., Jobs)
```

This helps cls-backend understand:
1. **Resource freshness**: Is the status based on current or outdated spec?
2. **Controller lag**: Is the resource controller behind on processing changes?
3. **Change detection**: Has the resource been modified since last status report?

### Status Report Structure
Every status report to cls-backend automatically includes:

**Example: Single Resource Controller (DNS Sub-Zone)**
```json
{
  "cluster_id": "cluster-123",
  "controller_name": "hypershift-dns-subzone",
  "observed_generation": 42,  // Always set to .cluster.generation
  "conditions": [
    {
      "name": "Applied",
      "status": "True",
      "reason": "DNSManagedZoneCreated",
      "message": "DNS sub-zone abcd.example.com. for HyperShift cluster production-east has been created"
    },
    {
      "name": "Available",
      "status": "True",
      "reason": "DNSZoneReady",
      "message": "DNS sub-zone abcd.example.com. is ready for HyperShift delegation"
    }
  ],
  "resources": {
    "dns-subzone": {
      "status": "Ready",
      "resource_status": {
        // Raw status from the DNSManagedZone resource (.status field)
        "generation": 1,
        "observed_generation": 1,
        "conditions": [
          {
            "type": "Ready",
            "status": "True",
            "lastTransitionTime": "2024-01-15T10:30:00Z",
            "reason": "UpToDate",
            "message": "The resource is up to date"
          }
        ],
        "observedState": {
          "dnsName": "abcd.example.com.",
          "nameServers": [
            "ns-cloud-c1.googledomains.com.",
            "ns-cloud-c2.googledomains.com."
          ]
        }
      }
    }
  },
  "timestamp": "2024-01-15T10:30:00Z"
}
```

**Example: Multi-Resource Controller (Maestro with ConfigMap + Job)**
```json
{
  "cluster_id": "cluster-456",
  "controller_name": "maestro-hostedcluster",
  "observed_generation": 15,
  "conditions": [
    {
      "name": "Applied",
      "status": "True",
      "reason": "ResourcesCreated",
      "message": "ConfigMap and Maestro transport job created for cluster production-west"
    },
    {
      "name": "Available",
      "status": "True",
      "reason": "MaestroSendSucceeded",
      "message": "Maestro transport completed successfully"
    }
  ],
  "resources": {
    "rendered-hostedcluster": {
      "status": "Ready",
      "resource_status": {
        // Raw status from ConfigMap (.status field) - no .data content included
        "generation": 3,  // ConfigMap updated multiple times
        "resourceVersion": "12847",
        // ConfigMaps don't have complex status, but we include what's available
      }
    },
    "maestro-transport": {
      "status": "Completed",
      "resource_status": {
        // Raw status from Job (.status field)
        "generation": 1,  // Job created once per generation
        "conditions": [
          {
            "type": "Complete",
            "status": "True",
            "lastTransitionTime": "2024-01-15T11:15:00Z"
          }
        ],
        "active": 0,
        "succeeded": 1,
        "completionTime": "2024-01-15T11:14:30Z"
      }
    }
  },
  "timestamp": "2024-01-15T11:15:00Z"
}
```

**Key Points about Resource Status Reporting:**
- **All resources included**: Both ConfigMap and Job status are reported for complete visibility
- **Status fields only**: Raw `.status` field from each Kubernetes resource, no resource content (spec, data, etc.)
- **Operational insight**: Know if ConfigMap creation/update succeeded, Job completed, etc.
- **Debugging separation**: Rendered YAML available via `kubectl get cm hostedcluster-production-west -o yaml`, status reports stay manageable

**Note for Controller Integration**: Another controller (e.g., HyperShift controller) can easily extract the sub-zone FQDN from any condition message using simple regex or string parsing:
- `conditions[?(@.name=="Available")].message` → `"DNS sub-zone abcd.example.com. is ready for HyperShift delegation"`
- Extract FQDN: `abcd.example.com.`

This allows the HyperShift controller to configure the HostedCluster with the correct delegated sub-zone.

#### Resource Status Examples

**Job Status (GCP Validation)**:
```json
"resource_status": {
  "generation": 1,  // Job was created once, never updated
  // No observed_generation - Jobs don't track this since they're immutable
  "conditions": [
    {
      "type": "Complete",
      "status": "True",
      "lastProbeTime": "2024-01-15T10:30:00Z",
      "lastTransitionTime": "2024-01-15T10:29:45Z"
    }
  ],
  "active": 0,
  "succeeded": 1,
  "failed": 0,
  "completionTime": "2024-01-15T10:29:45Z",
  "startTime": "2024-01-15T10:29:30Z"
}
```

**Config Connector DNSManagedZone Status (HyperShift Sub-Zone)**:
```json
"resource_status": {
  "generation": 1,  // from .metadata.generation - DNSManagedZone created once
  "observed_generation": 1,  // from .status.observedGeneration - Config Connector has processed generation 1
  "conditions": [  // from .status.conditions - Config Connector's assessment
    {
      "type": "Ready",
      "status": "True",
      "lastTransitionTime": "2024-01-15T10:30:00Z",
      "reason": "UpToDate",
      "message": "The resource is up to date"
    }
  ],
  "observedState": {  // from .status.observedState - Actual state from Google Cloud DNS
    "dnsName": "abcd.example.com.",  // Actual DNS zone name in GCP
    "description": "Delegated DNS sub-zone for HyperShift cluster production-east",
    "nameServers": [  // Name servers assigned by Google Cloud DNS
      "ns-cloud-c1.googledomains.com.",
      "ns-cloud-c2.googledomains.com.",
      "ns-cloud-c3.googledomains.com.",
      "ns-cloud-c4.googledomains.com."
    ],
    "visibility": "public"  // DNS zone visibility setting
  }
}
```

**HostedCluster Status (HyperShift)**:
```json
"resource_status": {
  "generation": 5,  // HostedCluster updated multiple times (version upgrades, config changes)
  "observed_generation": 4,  // HyperShift controller is one generation behind
  "conditions": [
    {
      "type": "Available",
      "status": "True",
      "lastTransitionTime": "2024-01-15T10:45:00Z"
    }
  ],
  "version": {
    "desired": {
      "image": "quay.io/openshift-release-dev/ocp-release:4.14.0"
    },
    "history": [
      {
        "image": "quay.io/openshift-release-dev/ocp-release:4.14.0",
        "version": "4.14.0"
      }
    ]
  },
  "kubeconfig": {
    "name": "my-cluster-admin-kubeconfig"
  },
  "platform": {
    "gcp": {
      "projectID": "my-project",
      "region": "us-central1"
    }
  }
}
```

### Resource Template Examples

#### Option 1: Simple Job (for direct work)
```yaml
resources:
  - name: "validation-job"
    description: "Validates cluster environment"
    template: |
      apiVersion: batch/v1
      kind: Job
      metadata:
        name: "validate-{{.cluster.id}}"
        namespace: "cls-system"
      spec:
        template:
          spec:
            containers:
            - name: validator
              image: "gcr.io/my-project/gcp-validator:latest"
              env:
              - name: CLUSTER_ID
                value: "{{.cluster.id}}"
            restartPolicy: Never
        backoffLimit: 3
```

#### Option 2: HyperShift HostedCluster (trigger HyperShift operator)
```yaml
resources:
  - name: "hosted-cluster"
    description: "HyperShift HostedCluster resource"
    template: |
      apiVersion: hypershift.openshift.io/v1beta1
      kind: HostedCluster
      metadata:
        name: "{{.cluster.name}}"
        namespace: "{{.cluster.namespace}}"
      spec:
        release:
          image: "{{.cluster.spec.openshift_version}}"
        platform:
          type: GCP
          gcp:
            projectID: "{{.cluster.spec.gcp_project}}"
            region: "{{.cluster.spec.region}}"
        dns:
          baseDomain: "{{.cluster.spec.base_domain}}"
```

#### Option 3: Config Connector DNS Sub-Zone (for cluster DNS delegation)
```yaml
resources:
  - name: "dns-subzone"
    description: "Delegated DNS sub-zone for HyperShift"
    template: |
      apiVersion: dns.cnrm.cloud.google.com/v1beta1
      kind: DNSManagedZone
      metadata:
        name: "{{.cluster.name}}-dns-zone"
        namespace: "{{.cluster.namespace | default "cls-system"}}"
        labels:
          cluster-id: "{{.cluster.id}}"
          cluster-generation: "{{.cluster.generation}}"
        annotations:
          cnrm.cloud.google.com/project-id: "{{.cluster.spec.gcp_project}}"
          cnrm.cloud.google.com/deletion-policy: "abandon"
      spec:
        dnsName: "{{randomString 4}}.{{.cluster.spec.base_domain}}."
        description: "Delegated DNS zone for cluster {{.cluster.name}}"
        visibility: "public"

#### Option 4: Multi-Resource (ConfigMap + Job for Maestro)
```yaml
resources:
  - name: "rendered-hostedcluster"
    description: "Rendered HostedCluster YAML for debugging"
    resourceManagement:
      updateStrategy: "in_place"  # ConfigMaps are mutable
    template: |
      apiVersion: v1
      kind: ConfigMap
      metadata:
        name: "hostedcluster-{{.cluster.name}}"
        namespace: "cls-system"
      data:
        hostedcluster.yaml: |
          apiVersion: hypershift.openshift.io/v1beta1
          kind: HostedCluster
          # ... full HostedCluster template here ...

  - name: "maestro-transport"
    description: "Job to send rendered YAML to Maestro"
    resourceManagement:
      updateStrategy: "versioned"  # Jobs are immutable
      cleanup:
        keepCompleted: 2            # Keep 2 transport jobs
    template: |
      apiVersion: batch/v1
      kind: Job
      metadata:
        name: "maestro-send-{{.cluster.id}}-gen-{{.cluster.generation}}-{{.timestamp}}"
        namespace: "cls-system"
      spec:
        template:
          spec:
            containers:
            - name: maestro-client
              image: "gcr.io/my-project/maestro-client:latest"
              command: ["/bin/maestro-client", "create-resource", "--file", "/etc/rendered/hostedcluster.yaml"]
              volumeMounts:
              - name: rendered-hostedcluster
                mountPath: /etc/rendered
                readOnly: true
            volumes:
            - name: rendered-hostedcluster
              configMap:
                name: "hostedcluster-{{.cluster.name}}"
            restartPolicy: Never
```


## 4. Simple Controller Architecture

### Repository Structure
```
cls-controller/
├── cmd/controller/              # Main application entry point
├── internal/
│   ├── controller/             # Core controller logic
│   ├── config/                 # CRD loading and validation
│   └── templating/            # Go template rendering
├── config/
│   ├── crds/                   # ControllerConfig CRD
│   └── examples/               # Example configurations
└── docs/                       # Documentation
```

### Core Components

#### Simple Controller Engine
```go
type Controller struct {
    // CRD configuration (loaded once at startup)
    config        *ControllerConfig

    // Templates (compiled once at startup)
    resourceTemplates   map[string]*template.Template  // keyed by resource name

    // SDK integration
    sdkClient     *sdk.Client
    k8sClient     client.Client

    logger        *zap.Logger
}

// Event processing (happens on every event)
func (c *Controller) HandleClusterEvent(event *ClusterEvent) error {
    // 1. Fetch current cluster spec from API (includes generation)
    cluster, err := c.apiClient.GetCluster(ctx, event.ClusterID)
    if err != nil {
        return err
    }

    // 2. Check preconditions (simple field comparisons)
    if !c.evaluatePreconditions(cluster) {
        // Report status indicating preconditions not met
        return c.reportPreconditionFailure(cluster)
    }

    // 3. Get or create all resources (render templates and create/update)
    resources, err := c.getOrCreateAllResources(cluster)
    if err != nil {
        // Report resource creation failure
        return c.reportResourceFailure(cluster, err)
    }

    // 4. Evaluate current status of all resources and report immediately
    return c.evaluateAndReportMultiResource(resources, cluster)
}

// Multi-resource management
func (c *Controller) getOrCreateAllResources(cluster *Cluster) (map[string]*unstructured.Unstructured, error) {
    resources := make(map[string]*unstructured.Unstructured)

    for _, resourceConfig := range c.config.Resources {
        resource, err := c.getOrCreateResource(resourceConfig, cluster)
        if err != nil {
            return nil, fmt.Errorf("failed to create resource %s: %w", resourceConfig.Name, err)
        }
        resources[resourceConfig.Name] = resource
    }

    return resources, nil
}

func (c *Controller) getOrCreateResource(resourceConfig ResourceConfig, cluster *Cluster) (*unstructured.Unstructured, error) {
    // Determine update strategy from per-resource configuration
    strategy := c.determineUpdateStrategy(resourceConfig, resourceKind)

    if strategy == "versioned" {
        return c.getOrCreateVersionedResource(resourceConfig, cluster)
    } else {
        return c.getOrUpdateInPlaceResource(resourceConfig, cluster)
    }
}
```

## 5. Deployment Model

### One-Controller-Per-Configuration
Each ControllerConfig gets its own controller deployment:

```yaml
# For GCP environment validation
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gcp-environment-validation
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: controller
        image: cls-controller:latest
        env:
        - name: CONTROLLER_CONFIG_NAME
          value: "gcp-environment-validation"
        - name: PUBSUB_SUBSCRIPTION
          value: "cls-gcp-validation-events"
```

### Scaling
- **Multiple controller types**: Deploy more controller instances for different use cases
- **Within controller type**: Increase replicas for the same ControllerConfig
- **Event isolation**: Each controller type has its own Pub/Sub subscription

## 6. Real Examples

### Example 1: GCP Environment Validation

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: gcp-environment-validation
  namespace: cls-system
spec:
  name: "gcp-environment-validation"
  description: "Validates GCP environment before cluster creation"

  # Only process GCP clusters
  preconditions:
    - field: "spec.provider"
      operator: "eq"
      value: "gcp"

  resources:
    - name: "validation-job"
      description: "GCP environment validation job"
      resourceManagement:
        updateStrategy: "versioned"  # Required for Jobs (immutable resources)
        versionInterval: "5m"        # Wait 5 minutes before recreating completed jobs
        newGenerationOnly: false     # Allow time-based recreation within same generation
        keepVersions: 3              # Keep 3 validation results
      template: |
        apiVersion: batch/v1
        kind: Job
        metadata:
          name: "gcp-validate-{{.cluster.id}}-gen-{{.cluster.generation}}-{{.timestamp}}"
          namespace: "cls-system"
          labels:
            cluster-id: "{{.cluster.id}}"
            cluster-generation: "{{.cluster.generation}}"
            controller: "gcp-environment-validation"
        spec:
          template:
            spec:
              containers:
              - name: validator
                image: "gcr.io/my-project/gcp-validator:latest"
                env:
                - name: CLUSTER_ID
                  value: "{{.cluster.id}}"
                - name: GCP_PROJECT
                  value: "{{.cluster.spec.gcp_project}}"
                - name: GCP_REGION
                  value: "{{.cluster.spec.region}}"
                volumeMounts:
                - name: gcp-key
                  mountPath: /etc/gcp
                  readOnly: true
              volumes:
              - name: gcp-key
                secret:
                  secretName: gcp-service-account-key
              restartPolicy: Never
          backoffLimit: 2
          activeDeadlineSeconds: 300

  statusConditions:
    - name: "Applied"
      status: "True"
      reason: "JobCreated"
      message: "GCP validation job has been created and is running"

    - name: "Available"
      status: "{{$complete := false}}{{$failed := false}}{{range .resources.validation-job.status.conditions}}{{if eq .type \"Complete\"}}{{if eq .status \"True\"}}{{$complete = true}}{{end}}{{end}}{{if eq .type \"Failed\"}}{{if eq .status \"True\"}}{{$failed = true}}{{end}}{{end}}{{end}}{{if $complete}}True{{else if $failed}}False{{else}}Unknown{{end}}"
      reason: "{{$complete := false}}{{$failed := false}}{{range .resources.validation-job.status.conditions}}{{if eq .type \"Complete\"}}{{if eq .status \"True\"}}{{$complete = true}}{{end}}{{end}}{{if eq .type \"Failed\"}}{{if eq .status \"True\"}}{{$failed = true}}{{end}}{{end}}{{end}}{{if $complete}}ValidationPassed{{else if $failed}}ValidationFailed{{else}}ValidationInProgress{{end}}"
      message: "{{$complete := false}}{{$failed := false}}{{range .resources.validation-job.status.conditions}}{{if eq .type \"Complete\"}}{{if eq .status \"True\"}}{{$complete = true}}{{end}}{{end}}{{if eq .type \"Failed\"}}{{if eq .status \"True\"}}{{$failed = true}}{{end}}{{end}}{{end}}GCP environment validation {{if $complete}}passed{{else if $failed}}failed{{else}}is running{{end}}"
```

### Example 2: Maestro HyperShift Cluster Creation with Direct API Integration

This controller uses the new Maestro target configuration for direct API integration:

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: maestro-hostedcluster
  namespace: cls-system
spec:
  name: "maestro-hostedcluster"
  description: "Creates HyperShift HostedClusters via Maestro API with placement-driven targeting"

  # Configure Maestro API target using placement decision data
  target:
    type: "maestro"
    maestroConfig:
      endpoint: "{{.cluster.status.maestroEndpoint}}"
      consumer: "{{.cluster.status.assignedConsumer}}"

  # Only process HyperShift clusters with placement decision data available
  preconditions:
    - field: "spec.infrastructure_type"
      operator: "eq"
      value: "hypershift"
    - field: "spec.provider"
      operator: "eq"
      value: "gcp"
    - field: "status.assignedConsumer"
      operator: "exists"
    - field: "status.maestroEndpoint"
      operator: "exists"
    - field: "status.pullSecretName"
      operator: "exists"
    - field: "status.sshKeyName"
      operator: "exists"

  resources:
    - name: "hosted-cluster"
      description: "HyperShift HostedCluster resource created via Maestro"
      resourceManagement:
        updateStrategy: "in_place"  # HostedCluster resources are mutable
      template: |
        apiVersion: hypershift.openshift.io/v1beta1
        kind: HostedCluster
        metadata:
          name: "{{.cluster.name}}"
          namespace: "{{.cluster.status.hostingNamespace | default "clusters"}}"
          labels:
            cluster.x-k8s.io/cluster-name: "{{.cluster.name}}"
            hypershift.openshift.io/hosted-cluster: "{{.cluster.name}}"
            cls-cluster-id: "{{.cluster.id}}"
          annotations:
            cluster.open-cluster-management.io/managedcluster-name: "{{.cluster.name}}"
            cls.redhat.com/cluster-generation: "{{.cluster.generation}}"
        spec:
          release:
            image: "{{.cluster.spec.openshift_version}}"
          pullSecret:
            name: "{{.cluster.status.pullSecretName}}"
          sshKey:
            name: "{{.cluster.status.sshKeyName}}"
          networking:
            clusterNetwork:
            - cidr: "{{.cluster.spec.cluster_cidr | default "10.132.0.0/14"}}"
            serviceNetwork:
            - cidr: "{{.cluster.spec.service_cidr | default "172.31.0.0/16"}}"
            networkType: "{{.cluster.spec.network_type | default "OVNKubernetes"}}"
          platform:
            type: GCP
            gcp:
              projectID: "{{.cluster.spec.gcp_project}}"
              region: "{{.cluster.spec.region}}"
              resourceTags:
              - key: "cluster-id"
                value: "{{.cluster.id}}"
              - key: "managed-by"
                value: "cls-backend"
              - key: "hosting-cluster"
                value: "{{.cluster.status.hostingCluster}}"
          infraID: "{{.cluster.name}}-{{substr 0 8 .cluster.id}}"
          dns:
            baseDomain: "{{.cluster.spec.base_domain}}"
            privateZoneID: "{{.cluster.status.privateZoneID}}"
            publicZoneID: "{{.cluster.status.publicZoneID}}"
          services:
          - service: APIServer
            servicePublishingStrategy:
              type: LoadBalancer
          - service: OAuthServer
            servicePublishingStrategy:
              type: Route
          - service: OIDC
            servicePublishingStrategy:
              type: Route
          - service: Konnectivity
            servicePublishingStrategy:
              type: Route
          - service: Ignition
            servicePublishingStrategy:
              type: Route
          autoscaling: {}

  statusConditions:
    - name: "Applied"
      status: "True"
      reason: "HostedClusterCreated"
      message: "HostedCluster {{.cluster.name}} has been created via Maestro to {{.cluster.status.hostingCluster}}"

    - name: "Available"
      status: "{{if .resources.hosted-cluster.status.conditions}}{{$available := \"Unknown\"}}{{range .resources.hosted-cluster.status.conditions}}{{if eq .type \"Available\"}}{{$available = .status}}{{end}}{{end}}{{$available}}{{else}}Unknown{{end}}"
      reason: "{{if .resources.hosted-cluster.status.conditions}}{{$reason := \"ClusterProvisioning\"}}{{range .resources.hosted-cluster.status.conditions}}{{if eq .type \"Available\"}}{{if eq .status \"True\"}}{{$reason = \"ClusterAvailable\"}}{{else}}{{$reason = \"ClusterNotAvailable\"}}{{end}}{{end}}{{end}}{{$reason}}{{else}}ClusterProvisioning{{end}}"
      message: "HostedCluster {{.cluster.name}} {{if .resources.hosted-cluster.status.conditions}}{{$message := \"is being provisioned\"}}{{range .resources.hosted-cluster.status.conditions}}{{if eq .type \"Available\"}}{{if eq .status \"True\"}}{{$message = \"is available and ready\"}}{{else}}{{$message = printf \"is not available: %s\" .message}}{{end}}{{end}}{{end}}{{$message}}{{else}}is being provisioned - check resource status for details{{end}}"

    - name: "Healthy"
      status: "{{if .resources.hosted-cluster.status.observedGeneration}}{{if eq .resources.hosted-cluster.metadata.generation .resources.hosted-cluster.status.observedGeneration}}True{{else}}False{{end}}{{else}}Unknown{{end}}"
      reason: "{{if .resources.hosted-cluster.status.observedGeneration}}{{if eq .resources.hosted-cluster.metadata.generation .resources.hosted-cluster.status.observedGeneration}}HostedClusterSynced{{else}}HostedClusterOutOfSync{{end}}{{else}}HostedClusterSyncPending{{end}}"
      message: "HostedCluster {{.cluster.name}} {{if .resources.hosted-cluster.status.observedGeneration}}{{if eq .resources.hosted-cluster.metadata.generation .resources.hosted-cluster.status.observedGeneration}}is in sync with desired state{{else}}is being updated to match desired state{{end}}{{else}}sync status is unknown{{end}}"
```

#### Expected Cluster Specification with Placement Data

```json
{
  "id": "cluster-789",
  "name": "production-west",
  "organization_domain": "redhat.com",
  "generation": 15,
  "spec": {
    "provider": "gcp",
    "infrastructure_type": "hypershift",
    "gcp_project": "my-gcp-project",
    "base_domain": "example.com",
    "openshift_version": "quay.io/openshift-release-dev/ocp-release:4.14.0",
    "cluster_cidr": "10.132.0.0/14",
    "service_cidr": "172.31.0.0/16",
    "network_type": "OVNKubernetes"
  },
  "status": {
    "assignedConsumer": "consumer-west-001",
    "maestroEndpoint": "maestro-west.example.com:443",
    "hostingCluster": "management-west-gcp",
    "hostingNamespace": "clusters-west",
    "pullSecretName": "production-west-pull-secret",
    "sshKeyName": "production-west-ssh-key",
    "privateZoneID": "projects/my-gcp-project/managedZones/private-zone-west",
    "publicZoneID": "projects/my-gcp-project/managedZones/public-zone-west"
  }
}
```

This example demonstrates:
- **Direct Maestro Integration**: No intermediate Jobs or ConfigMaps - resources created directly via Maestro API
- **Placement-Driven Configuration**: Maestro endpoint and consumer come from placement controller decisions
- **Dependency Management**: Uses preconditions to ensure all required placement data exists
- **Resource References**: Pull secrets, SSH keys, and DNS zones provided by other controllers via cluster status
- **Unified Status Interface**: Same `.resources.hosted-cluster.status` access pattern regardless of target type
- **Template-Driven Endpoints**: Maestro configuration uses cluster status fields populated by placement decisions

### Example 3: HyperShift DNS Sub-Zone Management with Config Connector

```yaml
apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: hypershift-dns-subzone
  namespace: cls-system
spec:
  name: "hypershift-dns-subzone"
  description: "Creates DNS sub-zones for HyperShift cluster delegation using Config Connector"

  # Only process HyperShift clusters that need DNS sub-zones
  preconditions:
    - field: "spec.provider"
      operator: "eq"
      value: "gcp"
    - field: "spec.infrastructure_type"
      operator: "eq"
      value: "hypershift"
    - field: "spec.dns_management_enabled"
      operator: "eq"
      value: true
    - field: "spec.parent_dns_zone"
      operator: "exists"

  resources:
    - name: "dns-subzone"
      description: "Delegated DNS sub-zone for HyperShift cluster"
      resourceManagement:
        updateStrategy: "in_place"  # Config Connector resources are mutable
      template: |
        apiVersion: dns.cnrm.cloud.google.com/v1beta1
        kind: DNSManagedZone
        metadata:
          name: "{{.cluster.name}}-{{randomString 4}}-subzone"
          namespace: "{{.cluster.namespace | default "cls-system"}}"
          labels:
            cluster-id: "{{.cluster.id}}"
            cluster-generation: "{{.cluster.generation}}"
            controller: "hypershift-dns-subzone"
            cluster-name: "{{.cluster.name}}"
          annotations:
            cnrm.cloud.google.com/project-id: "{{.cluster.spec.gcp_project}}"
            cnrm.cloud.google.com/deletion-policy: "abandon"  # Keep DNS sub-zone if resource is deleted
        spec:
          # Create delegated DNS sub-zone for HyperShift operator
          dnsName: "{{randomString 4}}.{{.cluster.spec.base_domain}}."
          description: "Delegated DNS sub-zone for HyperShift cluster {{.cluster.name}}"
          visibility: "public"

  statusConditions:
    - name: "Applied"
      status: "True"
      reason: "DNSManagedZoneCreated"
      message: "DNS sub-zone {{.resources.dns-subzone.spec.dnsName}} for HyperShift cluster {{.cluster.name}} has been created"

    - name: "Available"
      status: "{{if .resources.dns-subzone.status.conditions}}{{range .resources.dns-subzone.status.conditions}}{{if eq .type \"Ready\"}}{{.status}}{{end}}{{end}}{{else}}Unknown{{end}}"
      reason: "{{if .resources.dns-subzone.status.conditions}}{{range .resources.dns-subzone.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}DNSZoneReady{{else}}DNSZoneNotReady{{end}}{{end}}{{end}}{{else}}DNSZonePending{{end}}"
      message: "DNS sub-zone {{.resources.dns-subzone.spec.dnsName}} {{if .resources.dns-subzone.status.conditions}}{{range .resources.dns-subzone.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}is ready for HyperShift delegation{{else}}{{.message}}{{end}}{{end}}{{end}}{{else}}is being created{{end}}"

    - name: "Healthy"
      status: "{{if .resources.dns-subzone.status.observedGeneration}}{{if eq .resources.dns-subzone.metadata.generation .resources.dns-subzone.status.observedGeneration}}True{{else}}False{{end}}{{else}}Unknown{{end}}"
      reason: "{{if .resources.dns-subzone.status.observedGeneration}}{{if eq .resources.dns-subzone.metadata.generation .resources.dns-subzone.status.observedGeneration}}DNSZoneSynced{{else}}DNSZoneOutOfSync{{end}}{{else}}DNSZoneSyncPending{{end}}"
      message: "DNS sub-zone {{.resources.dns-subzone.spec.dnsName}} {{if .resources.dns-subzone.status.observedGeneration}}{{if eq .resources.dns-subzone.metadata.generation .resources.dns-subzone.status.observedGeneration}}is in sync with desired state{{else}}is being updated to match desired state{{end}}{{else}}sync status is unknown{{end}}"
```

#### Expected Cluster Specification

```json
{
  "id": "cluster-123",
  "name": "production-east",
  "organization_domain": "redhat.com",
  "generation": 42,
  "spec": {
    "provider": "gcp",
    "infrastructure_type": "hypershift",
    "gcp_project": "my-gcp-project",
    "base_domain": "example.com",
    "parent_dns_zone": "example-com-zone",
    "dns_management_enabled": true
  }
}
```

#### DNS Sub-Zone Results
After deployment: `abcd.example.com` delegated sub-zone ready for HyperShift operator to create records like:
- `api.abcd.example.com` → API server endpoint
- `*.apps.abcd.example.com` → Application ingress

This example demonstrates:
- **Config Connector Integration**: Using Google Cloud resources declaratively for DNS delegation
- **In-Place Strategy**: DNS sub-zones are mutable and updated efficiently
- **HyperShift Integration**: Creates delegated zones for HyperShift operator control
- **Random Sub-Zone Naming**: Uses 4-character random strings for unique sub-domains
- **Policy Control**: DNS sub-zones can be preserved even if controller resource is deleted


## 7. Implementation Plan

### Phase 1: Foundation and Core Framework (Sprint 1-3)

#### 1.1 CRD and Schema Definition
- **ControllerConfig CRD**: Implement complete CRD with all fields from design
  - Basic resource template configuration
  - Preconditions array with field/operator/value syntax
  - Target configuration (kube-api, maestro)
  - Resource management strategies (in_place, versioned)
  - Status conditions template configuration
  - Cleanup policies for versioned resources
- **CRD Validation**: OpenAPI v3 schema validation for all fields
- **CRD Installation**: Helm chart or kustomize for CRD deployment

#### 1.2 Core Controller Engine
- **Event Processing Loop**: Handle cluster events (created/updated/deleted/reconcile)
- **Configuration Loading**: Load and validate ControllerConfig from Kubernetes API
- **Template Compilation**: Pre-compile Go templates at startup with error handling
- **Graceful Startup/Shutdown**: Proper lifecycle management with health checks

#### 1.3 Internal SDK Components
- **Pub/Sub Client**: Subscribe to cluster events with configurable subscriptions
- **API Client**: cls-backend REST API integration for cluster specs and status reporting
- **Error Handling**: Retry logic, circuit breakers, and failure recovery
- **Metrics Integration**: Basic Prometheus metrics for event processing

### Phase 2: Resource Management and Strategies (Sprint 4-6)

#### 2.1 Precondition System
- **Field Evaluation Engine**: Support nested field paths (e.g., `spec.provider`, `status.assignedConsumer`)
- **Operator Implementation**: All operators (`eq`, `ne`, `in`, `notin`, `exists`, `notexists`)
- **JSONPath Support**: Complex field access like `status.conditions[?(@.type=='Ready')].status`
- **Precondition Failure Reporting**: Clear status reporting when preconditions fail

#### 2.2 Template System and Security
- **Go Template Engine**: Standard Go template processing with security restrictions
- **Template Functions**: Core functions (`join`, `split`, `replace`, `trim`, `lower`, `upper`)
- **Advanced Functions**: `randomString`, `substr`, `toJson`, `fromJson`, `base64encode`, `base64decode`
- **Template Context**: Provide `.cluster`, `.resources`, `.controller`, `.timestamp` context
- **Security Model**: Sandboxed template execution with restricted function set

#### 2.3 Resource Update Strategies
- **Strategy Detection**: Automatic strategy selection based on resource kind (Jobs = versioned)
- **In-Place Strategy**: Update existing mutable resources efficiently
- **Versioned Strategy**: Create new resources with generation + timestamp naming
- **Strategy Configuration**: Per-resource strategy override in ControllerConfig

### Phase 3: Advanced Features and Client Management (Sprint 7-9) ✅ **COMPLETED for Remote Targets**

#### 3.1 Multi-Target Client System ✅ **IMPLEMENTED**
- **✅ Client Manager**: Unified interface for different target types
- **✅ Local Kubernetes Client**: Default local cluster API access
- **✅ Remote Kubernetes Client**: Kubeconfig-based remote cluster access with caching
- **Maestro gRPC Client**: Direct Maestro API integration with connection pooling (pending)
- **✅ Client Selection Logic**: Automatic client selection based on target configuration

#### 3.2 Resource Lifecycle Management
- **Resource Creation**: Template rendering and Kubernetes resource creation
- **Resource Updates**: Handle spec changes for in-place strategy
- **Resource Retrieval**: Get current resource status for status reporting
- **Multi-Resource Support**: Handle multiple resources per controller with dependency tracking

#### 3.3 Completion Detection and Cleanup
- **Completion Detection Engine**: Unified field evaluation for resource completion
- **Cleanup Policies**: Configurable retention policies for versioned resources
- **Wait for Completion**: Optional blocking on old generation completion
- **Timeout Handling**: Force cleanup after completion timeout expires
- **Generation Tracking**: Proper generation transition handling

### Phase 4: Status Reporting and Observability (Sprint 10-11)

#### 4.1 Two-Layer Status Reporting
- **Controller Conditions**: Interpreted status (Applied, Available, Healthy)
- **Raw Resource Status**: Complete `.status` field from Kubernetes resources
- **Template-Based Conditions**: Dynamic status condition evaluation using templates
- **Generation Tracking**: Proper observedGeneration reporting

#### 4.2 Waiting States and Error Handling
- **Waiting Status**: Clear reporting during completion waits with timeout tracking
- **Error Status**: Detailed error reporting for template failures, API errors, etc.
- **Precondition Failures**: Informative status when preconditions aren't met
- **Resource Conflicts**: Handle resource creation conflicts and naming issues

#### 4.3 Observability and Monitoring
- **Prometheus Metrics**: Controller performance, resource creation success/failure rates
- **Structured Logging**: JSON logging with correlation IDs and cluster context
- **Health Endpoints**: Kubernetes readiness/liveness probes
- **Debug Information**: Resource status dumps and template rendering debug

### Phase 5: Testing and Validation (Sprint 12-13)

#### 5.1 Unit Testing
- **Template Rendering Tests**: All template functions and security boundaries
- **Precondition Evaluation**: All operators and field access patterns
- **Resource Strategy Tests**: Both in-place and versioned strategies
- **Client Manager Tests**: Mock clients for all target types
- **Status Reporting Tests**: Condition evaluation and report generation

#### 5.2 Integration Testing
- **End-to-End Tests**: Full workflow with test clusters and mock cls-backend
- **Multi-Resource Scenarios**: Controllers with multiple resources and dependencies
- **Error Scenarios**: Network failures, API errors, timeout conditions
- **Target Type Tests**: Local Kubernetes, remote Kubernetes, and Maestro targets

#### 5.3 Example Controllers
- **GCP Validation Job**: Complete implementation with versioned strategy
- **DNS Sub-Zone Management**: Config Connector integration with in-place strategy
- **Maestro HyperShift**: Multi-resource controller with ConfigMap + Job
- **Direct Maestro Integration**: HostedCluster creation via Maestro API

### Phase 6: Production Readiness (Sprint 14-15)

#### 6.1 Security and RBAC
- **Container Security**: runAsNonRoot, readOnlyRootFilesystem, dropped capabilities
- **RBAC Generation**: Automatic ClusterRole generation from resource templates
- **Secret Management**: Secure handling of kubeconfig secrets and service account keys
- **Network Policies**: Optional network isolation for controller pods

#### 6.2 Deployment and Operations
- **Helm Charts**: Production-ready Helm chart with configurable values
- **Kustomize Base**: Alternative kustomize deployment option
- **Multi-Controller Deployment**: Support for multiple controller instances
- **Resource Management**: Memory and CPU limits, PodDisruptionBudget

#### 6.3 Documentation and Migration
- **Usage Guide**: Complete guide for creating ControllerConfig resources
- **Template Reference**: Documentation for all available template functions
- **Migration Guide**: Migration from existing controllers to generalized controller
- **Troubleshooting Guide**: Common issues and debugging procedures

### Phase 7: Advanced Features and Optimization (Sprint 16+)

#### 7.1 Performance Optimization
- **Resource Caching**: Intelligent caching of Kubernetes resources
- **Batch Processing**: Batch multiple events for efficiency
- **Connection Pooling**: Optimize client connections and reuse
- **Memory Management**: Efficient template and resource management

#### 7.2 Advanced Template Features
- **Custom Functions**: Extensible template function system
- **Template Validation**: Pre-deployment template validation
- **Template Debugging**: Enhanced debugging tools for template development
- **Template Libraries**: Reusable template components

#### 7.3 Enhanced Observability
- **Distributed Tracing**: OpenTelemetry integration for request tracing
- **Advanced Metrics**: Detailed performance and business metrics
- **Alerting Integration**: PagerDuty/Slack integration for critical failures
- **Dashboard Templates**: Grafana dashboards for controller monitoring

### Acceptance Criteria

#### Phase 1-3 (MVP)
- [ ] ControllerConfig CRD deployed and validated
- [ ] Basic controller handles cluster events and creates simple Job resources
- [ ] Template rendering works with basic functions
- [ ] Preconditions prevent resource creation when conditions not met
- [ ] Status reporting works for simple scenarios

#### Phase 4-6 (Production Ready)
- [ ] All update strategies (in-place, versioned) work correctly
- [x] **Multi-target support (local, remote, maestro) implemented** ✅ **Remote targets production-ready**
- [ ] Cleanup policies work for versioned resources
- [x] **Two-layer status reporting provides complete visibility** ✅ **Working with remote targets**
- [x] **Security model implemented and tested** ✅ **All authentication methods tested**
- [x] **Production deployment ready with RBAC and monitoring** ✅ **Remote targets deployed successfully**

#### Phase 7+ (Advanced)
- [ ] Performance optimizations reduce resource usage by 50%
- [ ] Advanced template features enable complex use cases
- [ ] Complete observability stack provides operational insights
- [ ] Migration from existing controllers completed successfully

### Dependencies and Risks

#### External Dependencies
- **cls-backend API**: Cluster specification and status reporting endpoints
- **Maestro API**: gRPC client for direct Maestro integration
- **Config Connector**: For GCP resource management examples

#### Risk Mitigation
- **Template Security**: Comprehensive security review of template execution
- **Resource Conflicts**: Robust handling of naming conflicts and race conditions
- **API Compatibility**: Backward compatibility strategy for CRD schema changes
- **Performance**: Load testing with realistic cluster counts and event volumes

## 8. Benefits

### Simple and Focused
- **One responsibility**: Create a Kubernetes resource based on cluster events
- **Easy to understand**: 5-field CRD vs 50+ field enterprise config
- **Fast development**: Template any resource, deploy the controller
- **Standard patterns**: Uses normal Kubernetes resources and RBAC

### Flexible
- **Jobs for direct work**: Validation, backup, scripts, API calls, etc.
- **Custom Resources for operators**: CAPG, CAPI, HyperShift, ArgoCD, etc.
- **Any existing operator**: Leverage the entire Kubernetes operator ecosystem
- **Easy scaling**: Deploy more controller instances for different operators/purposes

### Operational
- **Kubernetes-native**: Uses standard Kubernetes resources and patterns
- **GitOps friendly**: CRDs work with ArgoCD, Flux, etc.
- **Observable**: Standard Job monitoring and logging
- **Secure**: Container isolation and RBAC provide security boundaries

This simplified design focuses on the core value: **making it easy to create any Kubernetes resource in response to cluster events**.