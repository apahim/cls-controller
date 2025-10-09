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

# CAPG GCPCluster controller
statusConditions:
  - name: "Applied"
    status: "True"
    reason: "GCPClusterCreated"
    message: "GCPCluster resource has been created"

  - name: "Available"
    status: "{{if .resource.status.ready}}True{{else}}Unknown{{end}}"
    reason: "{{if .resource.status.ready}}InfrastructureReady{{else}}InfrastructureProvisioning{{end}}"
    message: "GCP infrastructure {{if .resource.status.ready}}is ready{{else}}is being provisioned{{end}}"

  - name: "Healthy"
    status: "{{if .resource.status.network.ready}}True{{else}}False{{end}}"
    reason: "{{if .resource.status.network.ready}}NetworkHealthy{{else}}NetworkConfiguring{{end}}"
    message: "VPC {{.resource.status.network.name}} {{if .resource.status.network.ready}}is healthy{{else}}is being configured{{end}}"

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

### Two-Layer Status Reporting

The controller reports status at two levels:

#### 1. Controller Conditions (Interpreted)
These are the controller's interpretation of what's happening:
- **Applied**: "True" when resource is created, "False" if creation failed
- **Available**: Controller's assessment of resource readiness based on statusConditions
- **Healthy**: Optional condition for more complex health checks

#### 2. Resource Status (Raw)
The complete, unfiltered status from the actual Kubernetes resource:
- **Job**: `.status` with conditions, active/succeeded counts, completion time
- **GCPCluster**: `.status` with ready flag, network info, failure reasons
- **HostedCluster**: `.status` with version info, kubeconfig, conditions

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
```json
{
  "cluster_id": "cluster-123",
  "controller_name": "gcp-environment-validation",
  "observed_generation": 42,  // Always set to .cluster.generation
  "conditions": [
    {
      "name": "Applied",
      "status": "True",
      "reason": "JobCreated",
      "message": "GCP validation job has been created"
    },
    {
      "name": "Available",
      "status": "True",
      "reason": "ValidationPassed",
      "message": "GCP environment validation passed"
    }
  ],
  "resource_status": {
    // Raw status from the Kubernetes resource (Job, GCPCluster, etc.)
    "generation": 1,  // Resource's current generation (.metadata.generation)
    // No observed_generation for Jobs since they're immutable
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
    "completionTime": "2024-01-15T10:29:45Z"
  },
  "timestamp": "2024-01-15T10:30:00Z"
}
```

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

**GCPCluster Status (CAPG)**:
```json
"resource_status": {
  "generation": 2,  // GCPCluster was updated once (e.g., network config changed)
  "observed_generation": 2,  // CAPG controller has processed generation 2
  "ready": true,
  "network": {
    "name": "my-cluster-network",
    "ready": true,
    "selfLink": "https://www.googleapis.com/compute/v1/projects/my-project/global/networks/my-cluster-network"
  },
  "failureDomains": {
    "us-central1-a": {
      "controlPlane": true
    }
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

#### Option 2: CAPG GCPCluster (trigger CAPG operator)
```yaml
resourceTemplate: |
  apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
  kind: GCPCluster
  metadata:
    name: "{{.cluster.name}}"
    namespace: "{{.cluster.namespace}}"
  spec:
    project: "{{.cluster.spec.gcp_project}}"
    region: "{{.cluster.spec.region}}"
    network:
      name: "{{.cluster.spec.vpc_name}}"
    credentialsRef:
      name: gcp-credentials
      namespace: "{{.cluster.namespace}}"
```

#### Option 3: CAPI Cluster (trigger CAPI operator)
```yaml
resourceTemplate: |
  apiVersion: cluster.x-k8s.io/v1beta1
  kind: Cluster
  metadata:
    name: "{{.cluster.name}}"
    namespace: "{{.cluster.namespace}}"
  spec:
    clusterNetwork:
      pods:
        cidrBlocks:
        - "{{.cluster.spec.pod_cidr}}"
      services:
        cidrBlocks:
        - "{{.cluster.spec.service_cidr}}"
    infrastructureRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: GCPCluster
      name: "{{.cluster.name}}"
    controlPlaneRef:
      apiVersion: controlplane.cluster.x-k8s.io/v1beta1
      kind: KubeadmControlPlane
      name: "{{.cluster.name}}-control-plane"
```

#### Option 4: HyperShift HostedCluster (trigger HyperShift operator)
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

  resourceTemplate: |
    apiVersion: batch/v1
    kind: Job
    metadata:
      name: "gcp-validate-{{.cluster.id}}"
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

  resourceTemplate: |
    apiVersion: batch/v1
    kind: Job
    metadata:
      name: "maestro-create-{{.cluster.id}}"
      namespace: "cls-system"
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