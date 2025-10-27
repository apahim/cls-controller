# cls-hypershift-client Helm Chart

A simple Helm chart for deploying the cls-hypershift-client controller with GCP Config Connector resources.

## Configuration

All configuration is done through simple value replacements in `values.yaml`:

```yaml
# GCP Configuration
gcp:
  projectId: "your-project-id"

# Controller Configuration
controller:
  name: "cls-hypershift-client"
  namespace: "cls-system"
  image:
    repository: "gcr.io/your-project/cls-controller"
    tag: "latest"
  api:
    baseUrl: "http://cls-backend.cls-system:80/api/v1"

# Pub/Sub Configuration
pubsub:
  topic: "cluster-events"
  subscription: "cluster-events-hypershift-client"

# Service Account Names
serviceAccount:
  kubernetes: "cls-hypershift-client"
  gcp: "cls-hypershift-client-sa"

# Remote cluster configuration
remoteCluster:
  secretName: "remote-gke-cluster"
```

## Usage

### Install with Helm
```bash
helm install cls-hypershift-client ./deployments/helm/cls-hypershift-client \
  --namespace cls-system \
  --set gcp.projectId=your-project-id
```

### Deploy with ArgoCD
Use the provided ArgoCD Application in `deployments/argocd/cls-hypershift-client-application.yaml`

### Override Values
```bash
# Different project ID
helm install cls-hypershift-client ./deployments/helm/cls-hypershift-client \
  --set gcp.projectId=production-project \
  --set controller.image.tag=v1.2.3

# Different subscription name
helm install cls-hypershift-client ./deployments/helm/cls-hypershift-client \
  --set pubsub.subscription=cluster-events-prod
```

## Templates

- `gcp-resources.yaml`: Config Connector resources (IAM, Pub/Sub)
- `rbac.yaml`: Kubernetes ServiceAccount, ClusterRole, ClusterRoleBinding
- `deployment.yaml`: Controller Deployment
- `controllerconfig.yaml`: ControllerConfig custom resource

## Prerequisites

1. Config Connector installed and configured
2. CLS Controller CRDs deployed (use separate `cls-controller-crds` application)
3. Namespace `cls-system` created
4. Pub/Sub topic exists