## Remote Cluster (e.g. Management Cluster)

Requires Workload Identity.

Permission to the CLS-Controller (running on the Regional Cluster) Service Account:
```
GCLOUD_PROJECT="[GCLOUD_PROJECT]"                       # Google Cloud Project Name
IAM_SERVICE_ACCOUNT_NAME="[IAM_SERVICE_ACCOUNT_NAME]"   # Service Account name as defined in GCloud IAM

kubectl create clusterrolebinding remote-cls-controller-cluster-admin-binding \
    --clusterrole=cluster-admin \
    --user="$IAM_SERVICE_ACCOUNT_NAME@$GCLOUD_PROJECT.iam.gserviceaccount.com"

gcloud projects add-iam-policy-binding $GCLOUD_PROJECT \
     --member="serviceAccount:$IAM_SERVICE_ACCOUNT_NAME@$GCLOUD_PROJECT.iam.gserviceaccount.com" \
     --role="roles/container.clusterViewer" 
```

## Regional Cluster (Cluster running the CLS-Controller)

Export Environment Variables:
```
export SECRET_NAME="[SECRET_NAME]"        # Same name as specified in the ControllerConfig "target.secretRef.name"
export NAMESPACE="[NAMESPACE]"            # Secret Namespace, usually "cls-system"
export ENDPOINT="https://[ENDPOINT]"              # Replace with the Remote Cluster (Management Cluster) DNS Endpoint
```
Extract CA certificate directly from the cluster:
```
CA_CERT=$(echo | openssl s_client -servername $(echo $ENDPOINT | cut -d'/' -f3) -connect $(echo $ENDPOINT | cut -d'/' -f3):443 -showcerts 2>/dev/null | awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/ {if(++count==2) print; if(count>2) print}')
```

Create the Secret in the Regional Cluster:
```
kubectl create secret generic $SECRET_NAME -n $NAMESPACE \
--from-literal=endpoint="$ENDPOINT" \
--from-literal=ca-cert="$CA_CERT" \
--dry-run=client -o yaml | kubectl apply -f -
```