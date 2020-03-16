#!/bin/bash
#

PROJECT_ID=$1
SERVICE_ACCOUNT_NAME=$2
NAMESPACE=$3
DIR=`pwd`

usage() {
    echo "$1"
    echo ""
    echo "Usage: "
    echo "./gke-create-service-key.sh <Project ID> <Service Account Name> <Namespace>"
    echo ""
    echo "   Project ID: "
    echo "   The GCP project identifier you can find via: 'gcloud config get-value project'"
    echo ""
    echo "   Service Account Name: "
    echo "   The desired service account name to create"
    echo ""
    echo "   Namespace: "
    echo "   The kubernetes namespace to create the secret in."
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
    echo "NOTE: Namespace parameter was empty, this will create the secret in the default namespace"
fi

# Generate a yaml input with desired permissions for running turndown on GKE
cat <<EOF > cluster-turndown-role.yaml
title: "Cluster Turndown"
description: "Permissions needed to run cluster turndown on GKE"
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

# Create a new Role using the permissions listened in the yaml and remove permissions yaml
gcloud iam roles create cluster.turndown --project $PROJECT_ID --file cluster-turndown-role.yaml
rm -f cluster-turndown-role.yaml

# Create a new service account with the provided inputs and assign the new role
gcloud iam service-accounts create $SERVICE_ACCOUNT_NAME --display-name $SERVICE_ACCOUNT_NAME --format json && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role projects/$PROJECT_ID/roles/cluster.turndown && \
    gcloud iam service-accounts keys create $DIR/service-key.json --iam-account $SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com

if [ "$?" == "1" ]; then 
    echo "Failed to create service account key"
    exit 1
fi

# Instead of using gcloud to try and force kubectl to update kubeconfig current-context, 
# we'll just note that kubectl should be set to the correct context beforehand
# gcloud container clusters get-credentials $CLUSTER_ID

NSCOMM=""
if [ "$NAMESPACE" != "" ]; then
    NSCOMM="-n $NAMESPACE"
fi

# Determine if there is already a key 
kubectl describe secret cluster-turndown-service-key $NSCOMM > /dev/null 2>&1
if [ "$?" == "0" ]; then
    echo "Located an existing secret 'cluster-turndown-service-key'. Deleting..."
    kubectl delete secret cluster-turndown-service-key $NSCOMM 
fi

# Create the Secret containing the service key
kubectl create secret generic cluster-turndown-service-key $NSCOMM --from-file=$DIR/service-key.json
