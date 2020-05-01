#!/bin/bash
#

PROJECT_ID=$1
SERVICE_ACCOUNT_NAME=$2
NAMESPACE=$3
SECRET_NAME=$4
DIR=`pwd`

is_default=0

usage() {
    echo "$1"
    echo ""
    echo "Usage: "
    echo "./gke-create-service-key.sh <Project ID> <Service Account Name> <Namespace> <Secret Name>"
    echo ""
    echo "   Project ID: "
    echo "   The GCP project identifier you can find via: 'gcloud config get-value project'"
    echo ""
    echo "   Service Account Name: "
    echo "   The desired service account name to create"
    echo ""
    echo "   Namespace (Optional, Default: turndown): "
    echo "   The desired namespace to use with the secret. If this namespace is left empty, 'turndown' is used."
    echo ""
    echo "   Secret Name (Optional, Default: cluster-turndown-service-key): "
    echo "   The desired name of the secret. If this option is left empty, 'cluster-turndown-service-key' is used."
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

if [ "$NAMESPACE" == "" ]; then
    NAMESPACE="turndown"
    is_default=1
fi

if [ "$SECRET_NAME" == "" ]; then
    SECRET_NAME="cluster-turndown-service-key"
fi

# Generate a yaml input with desired permissions for running turndown on GKE
cat <<EOF > cluster-turndown-role.yaml
title: "Cluster Turndown"
description: "Permissions needed to run cluster turndown on GKE"
stage: "ALPHA"
includedPermissions:
- container.clusters.get
- container.clusters.update
- container.clusters.delete
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

# Create a new Role using the permissions listened in the yaml and remove permissions yaml
gcloud iam roles create cluster.turndown.v2 --project $PROJECT_ID --file cluster-turndown-role.yaml
rm -f cluster-turndown-role.yaml

# Create a new service account with the provided inputs and assign the new role
gcloud iam service-accounts create $SERVICE_ACCOUNT_NAME --display-name $SERVICE_ACCOUNT_NAME --format json && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role projects/$PROJECT_ID/roles/cluster.turndown.v2 && \
    gcloud iam service-accounts keys create $DIR/service-key.json --iam-account $SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com

if [ "$?" == "1" ]; then 
    echo "Failed to create service account key"
    exit 1
fi

# Create the turndown namespace only if turndown is used
if [ "$is_default" == "1" ]; then 
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Namespace
metadata:
  name: turndown
EOF

fi

# Determine if there is already a key 
kubectl describe secret $SECRET_NAME -n $NAMESPACE > /dev/null 2>&1
if [ "$?" == "0" ]; then
    echo "Located an existing secret 'cluster-turndown-service-key'. Deleting..."
    kubectl delete secret $SECRET_NAME -n $NAMESPACE
fi

# Create the Secret containing the service key
kubectl create secret generic $SECRET_NAME -n $NAMESPACE --from-file=$DIR/service-key.json
