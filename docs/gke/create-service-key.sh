#!/bin/bash
#

PROJECT_ID=$1
SERVICE_ACCOUNT_NAME=$2
DIR=`pwd`

usage() {
    echo "$1"
    echo ""
    echo "Usage: "
    echo "./create-service-key.sh <Project ID> <Service Account Name>"
    echo ""
    echo "   Project ID: "
    echo "   The GCP project identifier you can find via: 'gcloud config get-value project'"
    echo ""
    echo "   Service Account Name: "
    echo "   The desired service account name to create"
    echo ""
}

if [ "$PROJECT_ID" == "help" ]; then
    usage "Help"
    exit 1
fi

if [ "$PROJECT_ID" == "" ] || [ "$SERVICE_ACCOUNT_NAME" == "" ]; then
    usage "Invalid Parameters"
    exit 1
fi

cat <<EOF > kubecost-turndown-role.yaml
title: "Kubecost Turndown"
description: "Permissions needed to run kubecost turndown on GKE"
stage: "ALPHA"
includedPermissions:
- container.clusters.get
- container.clusters.update
- compute.instances.list
- iam.serviceAccounts.actAs
- container.nodes.create
- container.nodes.delete
- container.nodes.get
- container.nodes.getStatus
- container.nodes.list
- container.nodes.proxy
- container.nodes.update
- container.nodes.updateStatus
EOF

gcloud iam roles create kubecost.turndown --project $PROJECT_ID --file kubecost-turndown-role.yaml
rm -f kubecost-turndown-role.yaml

gcloud iam service-accounts create $SERVICE_ACCOUNT_NAME --display-name $SERVICE_ACCOUNT_NAME --format json && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role projects/$PROJECT_ID/roles/kubecost.turndown && \
    gcloud iam service-accounts keys create $DIR/service-key.json --iam-account $SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com

if [ "$?" == "1" ]; then 
    echo "Failed to create service account key"
    exit 1
fi

# Instead of using gcloud to try and force kubectl to update kubeconfig current-context, 
# we'll just note that kubectl should be set to the correct context beforehand
# gcloud container clusters get-credentials $CLUSTER_ID

# Determine if there is already a key 
kubectl describe secret kubecost-turndown-service-key -n kubecost > /dev/null 2>&1
if [ "$?" == "0" ]; then
    echo "Located an existing secret 'kubecost-turndown-service-key'. Deleting..."
    kubectl delete secret kubecost-turndown-service-key -n kubecost 
fi

kubectl create secret generic kubecost-turndown-service-key -n kubecost --from-file=$DIR/service-key.json

# We could have this script go ahead and have this script run kubectl apply...
# kubectl apply -f kubernetes/kubecost-turndown.yaml
# watch -g -n 5 "kubectl get service kubecost-turndown-service -n kubecost -o jsonpath='{range.status.loadBalancer.ingress[0]}{.ip}{end}'"
# service_ip=`kubectl get service kubecost-turndown-service -n kubecost -o jsonpath='{range.status.loadBalancer.ingress[0]}{.ip}{end}'`
# echo "Use curl to POST to http://$service_ip:9731/schedule"
