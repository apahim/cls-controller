# Generalized cls-controller Implementation Plan

## Overview
Create a generalized, configurable controller that can create Kubernetes resources based on cluster events.

### Architecture Principles

**Event-Driven Execution**: The controller uses Pub/Sub for lightweight event notifications (cluster created, updated, deleted, reconcile). Events trigger immediate action execution.

**API-Based Data Transfer**: All cluster specifications and status updates happen via the cls-backend REST API. The controller fetches cluster specs on-demand and reports status back.

**Template-Driven Resources**: The controller uses Go templates to create either Jobs (for direct work) or Custom Resources (to trigger existing operators like CAPG, CAPI, etc.).

**CRD-Based Configuration**: Controller configurations are defined using a simple CRD that gets loaded once at startup.

**Simple and Secure**: Basic container security and K8s RBAC provide sufficient isolation.

## 1. Simple Security Model

### Basic Security Principles
- **Container Security**: All Jobs run with `runAsNonRoot`, `readOnlyRootFilesystem`, and dropped capabilities
- **RBAC**: Controllers have minimal permissions - create Jobs, read cluster specs, write status
- **Secret Management**: Secrets mounted as volumes, never in environment variables
- **Network Isolation**: Jobs run in isolated namespaces with network policies (optional)

### Template Security
Templates use standard Go template syntax with basic functions:
- String manipulation: `join`, `split`, `replace`, `trim`, `lower`, `upper`
- Encoding: `toJson`, `fromJson`, `base64encode`, `base64decode`
- Default values: `default`
- Random generation: `randomString <length>` - generates random alphanumeric string (e.g., `{{randomString 4}}` → `abcd`)
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
            required: ["name", "resourceTemplate"]
            properties:
              name:
                type: string
                description: "Controller name"
              description:
                type: string
                description: "What this controller does"

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

              # The Kubernetes resource to create when an event matches
              resourceTemplate:
                type: string
                description: "Kubernetes resource YAML template (Job for direct work, or CR for operators like CAPG/CAPI)"

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

              # Resource management configuration
              resourceManagement:
                type: object
                description: "Resource update and cleanup strategies"
                properties:
                  updateStrategy:
                    type: string
                    enum: ["in_place", "versioned"]
                    default: "in_place"
                    description: "How to handle resource updates: 'in_place' updates existing resource, 'versioned' creates new resource per generation"
                  cleanup:
                    type: object
                    description: "Cleanup policies (only applies when updateStrategy is 'versioned')"
                    properties:
                      retentionPolicy:
                        type: object
                        description: "Resource retention settings"
                        properties:
                          completedResourcesPerGeneration:
                            type: integer
                            default: 2
                            description: "Keep last N completed resources per generation"
                          totalCompletedResources:
                            type: integer
                            default: 5
                            description: "Keep last N completed resources across all generations"
                          maxAge:
                            type: string
                            default: "24h"
                            description: "Keep resources newer than this duration (e.g., '24h', '7d')"
                      cleanupTrigger:
                        type: string
                        enum: ["before_create", "after_create", "periodic"]
                        default: "before_create"
                        description: "When to trigger cleanup"
                      cleanupBehavior:
                        type: object
                        description: "Cleanup behavior settings"
                        properties:
                          deleteFailedResources:
                            type: boolean
                            default: true
                            description: "Delete failed resources immediately"
                          preserveRunningResources:
                            type: boolean
                            default: true
                            description: "Keep running resources (safety)"
                          deletionGracePeriod:
                            type: string
                            default: "30s"
                            description: "Grace period before forced deletion"

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
1. **Event Received**: Controller gets cluster event (created/updated/deleted/reconcile)
2. **Fetch Cluster Spec**: Get current cluster spec from cls-backend API (includes generation)
3. **Precondition Check**: Evaluate optional preconditions against cluster spec
4. **Resource Creation**: Render and create Kubernetes resource from template (if not exists)
5. **Status Check**: Read current resource status
6. **Report Status**: Send current status back to cls-backend with observedGeneration

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
- **eq**: Field equals value
- **ne**: Field not equals value
- **in**: Field value is in array
- **notin**: Field value is not in array
- **exists**: Field exists (any value)
- **notexists**: Field does not exist


### Status Condition Examples
```yaml
# Job-based controller
statusConditions:
  - name: "Applied"
    status: "True"
    reason: "JobCreated"
    message: "Job {{.resource.metadata.name}} has been created"

  - name: "Available"
    status: "{{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}True{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}False{{else}}Unknown{{end}}"
    reason: "{{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}JobSucceeded{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}JobFailed{{else}}JobRunning{{end}}"
    message: "Job {{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}completed successfully{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}failed{{else}}is running with {{.resource.status.active}} active pods{{end}}"

# Config Connector DNS sub-zone controller
statusConditions:
  - name: "Applied"
    status: "True"
    reason: "DNSManagedZoneCreated"
    message: "DNS managed zone has been created"

  - name: "Available"
    status: "{{if .resource.status.conditions}}{{range .resource.status.conditions}}{{if eq .type \"Ready\"}}{{.status}}{{end}}{{end}}{{else}}Unknown{{end}}"
    reason: "{{if .resource.status.conditions}}{{range .resource.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}DNSZoneReady{{else}}DNSZoneNotReady{{end}}{{end}}{{end}}{{else}}DNSZonePending{{end}}"
    message: "DNS sub-zone {{if .resource.status.conditions}}{{range .resource.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}is ready for delegation{{else}}{{.message}}{{end}}{{end}}{{end}}{{else}}is being created{{end}}"

  - name: "Healthy"
    status: "{{if .resource.status.observedGeneration}}{{if eq .resource.metadata.generation .resource.status.observedGeneration}}True{{else}}False{{end}}{{else}}Unknown{{end}}"
    reason: "{{if .resource.status.observedGeneration}}{{if eq .resource.metadata.generation .resource.status.observedGeneration}}DNSZoneSynced{{else}}DNSZoneOutOfSync{{end}}{{else}}DNSZoneSyncPending{{end}}"
    message: "DNS sub-zone {{if .resource.status.observedGeneration}}{{if eq .resource.metadata.generation .resource.status.observedGeneration}}is in sync with desired state{{else}}is being updated to match desired state{{end}}{{else}}sync status is unknown{{end}}"

# HyperShift HostedCluster controller
statusConditions:
  - name: "Applied"
    status: "True"
    reason: "HostedClusterCreated"
    message: "HostedCluster resource has been created"

  - name: "Available"
    status: "{{if .resource.status.conditions}}Unknown{{else}}Unknown{{end}}"
    reason: "ClusterProvisioning"
    message: "HostedCluster is being provisioned - check resource status for details"
```

### ObservedGeneration Tracking
All status reports must include the `observedGeneration` from the cluster spec to ensure status corresponds to the correct cluster version.

### Available Template Context
Templates have access to:
- `.resource` - The created Kubernetes resource with its current status
- `.cluster` - The original cluster spec from cls-backend (includes `.generation`)
- `.controller` - Controller metadata (name, type, etc.)
- `.timestamp` - Unix timestamp for unique resource naming (e.g., `1704067200`)
- `randomString <length>` - Function to generate random alphanumeric strings (e.g., `{{randomString 4}}` → `abcd`)

**Note**: The `.resource` object contains the live state of the Kubernetes resource created by the controller, allowing status conditions to access its current status, metadata, and spec fields.

### Reconciliation-Based Status Reporting

The controller operates on a simple principle: **report what you see right now**. There's no waiting or watching - cls-backend handles scheduling reconciliation events to check progress.

#### Event-Driven Flow
1. **cluster.created**: Create resource, report initial status
2. **cluster.reconcile**: Check resource status, report current state
3. **cluster.reconcile**: (repeat) Check resource status, report current state
4. **cluster.reconcile**: Resource completed → report final success/failure

#### Status Progression Example
```
Event 1 (created):  Create Job → Report "Applied: True, Available: Unknown"
Event 2 (reconcile): Job running → Report "Applied: True, Available: Unknown"
Event 3 (reconcile): Job completed → Report "Applied: True, Available: True"
```

The cls-backend scheduler determines how often to send reconcile events based on the resource type and expected completion time.

## 3a. Resource Update Strategies

The controller supports two distinct update strategies to handle different types of Kubernetes resources and operational requirements:

### Strategy Overview

| Strategy | Use Case | Resource Types | Naming | Cleanup |
|----------|----------|----------------|---------|---------|
| **in_place** | Default for mutable resources | Deployments, ConfigMaps, CRs | Static name | None needed |
| **versioned** | Immutable resources or audit trail | Jobs, Pods, or any resource | Generation + timestamp | Configurable retention |

### When Each Strategy is Used

#### Automatic Strategy Selection
The controller automatically determines the appropriate strategy based on resource type:

```go
func (c *Controller) determineUpdateStrategy(resourceKind string) string {
    // Force versioned strategy for immutable resources
    if c.isImmutableResourceKind(resourceKind) {
        return "versioned"
    }

    // Use configured strategy for mutable resources
    return c.config.ResourceManagement.UpdateStrategy
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
  resourceManagement:
    updateStrategy: "in_place"  # Default - no cleanup needed

  resourceTemplate: |
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
  resourceManagement:
    updateStrategy: "versioned"  # Creates new resource per generation
    cleanup:
      retentionPolicy:
        completedResourcesPerGeneration: 2
        maxAge: "24h"
      cleanupBehavior:
        deleteFailedResources: true

  resourceTemplate: |
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
func (c *Controller) determineUpdateStrategy(resourceKind string) string {
    // Force versioned strategy for immutable resources
    if c.isImmutableResourceKind(resourceKind) {
        return "versioned"
    }

    // Use configured strategy for mutable resources
    return c.config.ResourceManagement.UpdateStrategy
}
```

**Key Principles for Versioned Strategy**:
1. **One active resource per cluster** at any time
2. **Generation-aware cleanup** - delete resources from old generations immediately
3. **Continuous enforcement** - recreate completed resources within same generation
4. **Incremental naming** - `resource-{cluster-id}-gen-{generation}-{timestamp}`

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

### Configurable Resource Cleanup (Versioned Strategy Only)

#### Cleanup Policies
When using `updateStrategy: "versioned"`, the controller supports multiple configurable cleanup policies working together:

1. **Generation-based**: Always clean old generations immediately
2. **Count-based**: Keep N completed resources per generation and total
3. **Age-based**: Clean resources older than configured duration
4. **Status-based**: Optionally clean failed resources immediately
5. **Safety**: Preserve running resources unless explicitly configured otherwise

**Note**: Cleanup policies only apply to versioned strategy. In-place strategy maintains a single resource per cluster, so no cleanup is needed.

#### Cleanup Configuration Examples

**Conservative Cleanup** (Keep More History):
```yaml
resourceManagement:
  updateStrategy: "versioned"
  cleanup:
    retentionPolicy:
      completedResourcesPerGeneration: 5  # Keep 5 completed resources per generation
      totalCompletedResources: 20         # Keep 20 total across all generations
      maxAge: "7d"                        # Keep resources for 7 days
    cleanupBehavior:
      deleteFailedResources: false        # Keep failed resources for debugging
      preserveRunningResources: true      # Never delete running resources
```

**Aggressive Cleanup** (Minimal Storage):
```yaml
resourceManagement:
  updateStrategy: "versioned"
  cleanup:
    retentionPolicy:
      completedResourcesPerGeneration: 1  # Keep only 1 completed resource per generation
      totalCompletedResources: 3          # Keep only 3 total resources
      maxAge: "2h"                        # Keep resources for 2 hours only
    cleanupBehavior:
      deleteFailedResources: true         # Clean up failed resources immediately
      preserveRunningResources: true      # Still preserve running resources
```

#### Cleanup Implementation
```go
func (c *ResourceCleanupManager) CleanupResources(clusterID, currentGeneration string) error {
    // 1. Get all resources for this cluster
    resources, err := c.listResourcesByCluster(clusterID)
    if err != nil {
        return err
    }

    // 2. Clean up old generations (keep only current)
    if err := c.cleanupOldGenerations(resources, currentGeneration); err != nil {
        return err
    }

    // 3. Clean up excess resources in current generation by count
    if err := c.cleanupExcessResourcesInGeneration(resources[currentGeneration]); err != nil {
        return err
    }

    // 4. Clean up resources by age
    if err := c.cleanupResourcesByAge(resources); err != nil {
        return err
    }

    // 5. Clean up failed resources (if configured)
    if c.config.DeleteFailedResources {
        if err := c.cleanupFailedResources(resources); err != nil {
            return err
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

**Example: DNS Sub-Zone Controller Status**
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
    },
    {
      "name": "Healthy",
      "status": "True",
      "reason": "DNSZoneSynced",
      "message": "DNS sub-zone abcd.example.com. is in sync with desired state"
    }
  ],
  "resource_status": {
    // Raw status from the DNSManagedZone resource (.status field)
    "generation": 1,  // from .metadata.generation
    "observed_generation": 1,  // from .status.observedGeneration
    "conditions": [  // from .status.conditions
      {
        "type": "Ready",
        "status": "True",
        "lastTransitionTime": "2024-01-15T10:30:00Z",
        "reason": "UpToDate",
        "message": "The resource is up to date"
      }
    ],
    "observedState": {  // from .status.observedState (Config Connector specific)
      "dnsName": "abcd.example.com.",  // Actual DNS zone name in GCP
      "nameServers": [  // Name servers assigned by Google Cloud DNS
        "ns-cloud-c1.googledomains.com.",
        "ns-cloud-c2.googledomains.com."
      ]
    }
  },
  "timestamp": "2024-01-15T10:30:00Z"
}
```

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

#### Option 1: Job (for direct work)
```yaml
resourceTemplate: |
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
resourceTemplate: |
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
resourceTemplate: |
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
    dnsName: "{{.cluster.spec.dns_subdomain}}.{{.cluster.spec.base_domain}}."
    description: "Delegated DNS zone for cluster {{.cluster.name}}"
    visibility: "public"
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

    // Template (compiled once at startup)
    resourceTemplate   *template.Template

    // SDK integration
    sdkClient     *controllersdk.Client
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
        return nil // Skip this event
    }

    // 3. Get or create resource (fast template render)
    resource, err := c.getOrCreateResource(cluster)
    if err != nil {
        return err
    }

    // 4. Evaluate current status and report immediately
    return c.evaluateAndReport(resource, cluster)
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

  # Resource management - Jobs require versioned strategy
  resourceManagement:
    updateStrategy: "versioned"  # Required for Jobs (immutable resources)
    cleanup:
      retentionPolicy:
        completedResourcesPerGeneration: 2
        totalCompletedResources: 5
        maxAge: "24h"
      cleanupBehavior:
        deleteFailedResources: true
        preserveRunningResources: true

  resourceTemplate: |
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
      status: "{{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}True{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}False{{else}}Unknown{{end}}"
      reason: "{{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}ValidationPassed{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}ValidationFailed{{else}}ValidationInProgress{{end}}"
      message: "GCP environment validation {{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}passed{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}failed{{else}}is running{{end}}"
```

### Example 2: Maestro gRPC HyperShift Cluster Creation

First, deploy the HostedCluster template as a ConfigMap:

```yaml
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: hostedcluster-template
  namespace: cls-system
data:
  hostedcluster.yaml: |
    apiVersion: hypershift.openshift.io/v1beta1
    kind: HostedCluster
    metadata:
      name: "{{.cluster.name}}"
      namespace: "{{.cluster.namespace}}"
      labels:
        cluster.x-k8s.io/cluster-name: "{{.cluster.name}}"
        hypershift.openshift.io/hosted-cluster: "{{.cluster.name}}"
      annotations:
        cluster.open-cluster-management.io/managedcluster-name: "{{.cluster.name}}"
    spec:
      release:
        image: "{{.cluster.spec.openshift_version}}"
      pullSecret:
        name: pull-secret
      sshKey:
        name: ssh-key
      networking:
        clusterNetwork:
        - cidr: "{{.cluster.spec.cluster_cidr | default \"10.132.0.0/14\"}}"
        serviceNetwork:
        - cidr: "{{.cluster.spec.service_cidr | default \"172.31.0.0/16\"}}"
        networkType: "{{.cluster.spec.network_type | default \"OVNKubernetes\"}}"
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
      infraID: "{{.cluster.name}}-{{substr 0 8 .cluster.id}}"
      dns:
        baseDomain: "{{.cluster.spec.base_domain}}"
        privateZoneID: "{{.cluster.spec.private_zone_id}}"
        publicZoneID: "{{.cluster.spec.public_zone_id}}"
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

apiVersion: cls.redhat.com/v1alpha1
kind: ControllerConfig
metadata:
  name: maestro-hostedcluster
  namespace: cls-system
spec:
  name: "maestro-hostedcluster"
  description: "Creates HyperShift HostedClusters via Maestro gRPC"

  # Only process HyperShift clusters with Maestro enabled
  preconditions:
    - field: "spec.infrastructure_type"
      operator: "eq"
      value: "hypershift"
    - field: "spec.use_maestro"
      operator: "eq"
      value: true

  # Conservative cleanup for long-running cluster creation
  resourceManagement:
    updateStrategy: "versioned"  # Required for Jobs (immutable resources)
    cleanup:
      retentionPolicy:
        completedResourcesPerGeneration: 3
        totalCompletedResources: 10
        maxAge: "72h"
      cleanupBehavior:
        deleteFailedResources: false  # Keep failed resources for debugging
        preserveRunningResources: true

  resourceTemplate: |
    apiVersion: batch/v1
    kind: Job
    metadata:
      name: "maestro-create-{{.cluster.id}}-gen-{{.cluster.generation}}-{{.timestamp}}"
      namespace: "cls-system"
      labels:
        cluster-id: "{{.cluster.id}}"
        cluster-generation: "{{.cluster.generation}}"
        controller: "maestro-hostedcluster"
    spec:
      template:
        spec:
          containers:
          - name: maestro-client
            image: "gcr.io/my-project/maestro-client:latest"
            env:
            - name: CLUSTER_ID
              value: "{{.cluster.id}}"
            - name: CLUSTER_NAME
              value: "{{.cluster.name}}"
            - name: HOSTING_CLUSTER_ID
              value: "{{.cluster.spec.hosting_cluster_id}}"
            - name: MAESTRO_GRPC_URL
              value: "maestro-grpc.maestro-system.svc.cluster.local:8090"
            command: ["/bin/maestro-client", "create-hostedcluster", "--template", "/etc/templates/hostedcluster.yaml"]
            volumeMounts:
            - name: maestro-certs
              mountPath: /etc/maestro/certs
              readOnly: true
            - name: hostedcluster-template
              mountPath: /etc/templates
              readOnly: true
          volumes:
          - name: maestro-certs
            secret:
              secretName: maestro-client-certs
          - name: hostedcluster-template
            configMap:
              name: hostedcluster-template
          restartPolicy: Never
      backoffLimit: 3
      activeDeadlineSeconds: 1800

  statusConditions:
    - name: "Applied"
      status: "True"
      reason: "JobCreated"
      message: "Maestro cluster creation job has been created and is running"

    - name: "Available"
      status: "{{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}True{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}False{{else}}Unknown{{end}}"
      reason: "{{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}CreationSucceeded{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}CreationFailed{{else}}CreationInProgress{{end}}"
      message: "Cluster creation {{if eq .resource.status.conditions[?(@.type==\"Complete\")].status \"True\"}}completed successfully{{else if eq .resource.status.conditions[?(@.type==\"Failed\")].status \"True\"}}failed{{else}}is in progress{{end}}"
```

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

  # Config Connector resources are mutable - use in-place updates
  resourceManagement:
    updateStrategy: "in_place"
    # No cleanup needed - single DNS sub-zone per cluster

  resourceTemplate: |
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
      message: "DNS sub-zone {{.resource.spec.dnsName}} for HyperShift cluster {{.cluster.name}} has been created"

    - name: "Available"
      status: "{{if .resource.status.conditions}}{{range .resource.status.conditions}}{{if eq .type \"Ready\"}}{{.status}}{{end}}{{end}}{{else}}Unknown{{end}}"
      reason: "{{if .resource.status.conditions}}{{range .resource.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}DNSZoneReady{{else}}DNSZoneNotReady{{end}}{{end}}{{end}}{{else}}DNSZonePending{{end}}"
      message: "DNS sub-zone {{.resource.spec.dnsName}} {{if .resource.status.conditions}}{{range .resource.status.conditions}}{{if eq .type \"Ready\"}}{{if eq .status \"True\"}}is ready for HyperShift delegation{{else}}{{.message}}{{end}}{{end}}{{end}}{{else}}is being created{{end}}"

    - name: "Healthy"
      status: "{{if .resource.status.observedGeneration}}{{if eq .resource.metadata.generation .resource.status.observedGeneration}}True{{else}}False{{end}}{{else}}Unknown{{end}}"
      reason: "{{if .resource.status.observedGeneration}}{{if eq .resource.metadata.generation .resource.status.observedGeneration}}DNSZoneSynced{{else}}DNSZoneOutOfSync{{end}}{{else}}DNSZoneSyncPending{{end}}"
      message: "DNS sub-zone {{.resource.spec.dnsName}} {{if .resource.status.observedGeneration}}{{if eq .resource.metadata.generation .resource.status.observedGeneration}}is in sync with desired state{{else}}is being updated to match desired state{{end}}{{else}}sync status is unknown{{end}}"
```

#### Expected Cluster Specification

```json
{
  "id": "cluster-123",
  "name": "production-east",
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

### Phase 1: Core Framework
1. **CRD Definition**: Create simple ControllerConfig CRD
2. **Basic Controller**: Event handling, CRD loading, template compilation
3. **SDK Integration**: Use cls-controller-sdk for Pub/Sub and API calls
4. **Resource Creation**: Render and create any Kubernetes resource from templates

### Phase 2: Status and Testing
1. **Status Reporting**: Read resource status and report via cls-backend API
2. **Template Functions**: Basic Go template functions (join, split, etc.)
3. **Unit Tests**: Test template rendering and event handling
4. **Integration Tests**: End-to-end testing with test clusters

### Phase 3: Production
1. **Examples**: Create real ControllerConfig examples for Jobs, Deployments, ConfigMaps
2. **Documentation**: Usage guide and migration instructions
3. **RBAC**: Generate ClusterRole from resource templates
4. **Deployment**: Create deployment manifests

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