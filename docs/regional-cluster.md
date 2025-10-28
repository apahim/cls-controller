## Additional Configuration (Regional Cluster)

Export Environment Variables:
```
export GCLOUD_PROJECT="[GCLOUD_PROJECT]"                        # Google Cloud Project Name
export IAM_SERVICE_ACCOUNT_NAME="[IAM_SERVICE_ACCOUNT_NAME]"    # Service Account name as defined in GCloud IAM
export KUBE_SERVICE_ACCOUNT_NAME="[$KUBE_SERVICE_ACCOUNT_NAME]" # K8s ServiceAccount name used by the CLS-Controller Deployment 
export SUBSCRIPTION_ID="[SUBSCRIPTION_ID]"                      # Pub/Sub Subscription unique name used by the CLS Controller
export NAMESPACE="[NAMESPACE]"                                  # K8s Namespace used by the CLS-Controller, usually "cls-system"
```

Create a Service Account in Google Cloud:
```
gcloud iam service-accounts create $IAM_SERVICE_ACCOUNT_NAME \
    --project=$GCLOUD_PROJECT \
    --display-name="CLS Controller"
```

Create a Pub/Sub Subscription for the controller:
```
gcloud pubsub subscriptions create $SUBSCRIPTION_ID \
    --topic=cluster-events
```

Add permission for Pub/Sub Subscriber:
```
gcloud pubsub subscriptions add-iam-policy-binding $SUBSCRIPTION_ID \
    --member="serviceAccount:$IAM_SERVICE_ACCOUNT_NAME@$GCLOUD_PROJECT.iam.gserviceaccount.com" \
    --role="roles/pubsub.subscriber" \
    --project=$GCLOUD_PROJECT
```

Add permission for Workload Identity:
```
gcloud iam service-accounts add-iam-policy-binding "$IAM_SERVICE_ACCOUNT_NAME@$GCLOUD_PROJECT.iam.gserviceaccount.com" \
 --role="roles/iam.workloadIdentityUser" \
 --member="serviceAccount:$GCLOUD_PROJECT.svc.id.goog[$NAMESPACE/$KUBE_SERVICE_ACCOUNT_NAME]"
```

Annotate the Kubernetes Service Account:
```
kubectl annotate serviceaccount $KUBE_SERVICE_ACCOUNT_NAME \
    --namespace $NAMESPACE \
    "iam.gke.io/gcp-service-account=$IAM_SERVICE_ACCOUNT_NAME@$GCLOUD_PROJECT.iam.gserviceaccount.com"
```

