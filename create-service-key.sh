#!/bin/bash
#

# PROJECT_ID=$(gcloud config get-value project)
PROJECT_ID=$1
SERVICE_ACCOUNT_NAME=$2
DESCRIPTION=$3

usage() {
    echo "$1"
    echo ""
    echo "Usage: "
    echo "./create-service-key.sh <Project ID> <Service Account Name> <Description (Optional)>"
    echo ""
    echo "   Project ID: "
    echo "   The GCP project identifier you can find via: 'gcloud config get-value project'"
    echo ""
    echo "   Service Account Name: "
    echo "   The desired service account name to create"
    echo ""
    echo "   Description (Optional): "
    echo "   A description of the service account"
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

gcloud iam service-accounts create $SERVICE_ACCOUNT_NAME --display-name "$DESCRIPTION" --format json && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role roles/compute.admin && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role roles/container.admin && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role roles/container.clusterAdmin && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role roles/container.viewer && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role roles/container.clusterViewer && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role roles/container.developer && \
    gcloud projects add-iam-policy-binding $PROJECT_ID --member serviceAccount:$SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com --role roles/iam.serviceAccountUser && \
    gcloud iam service-accounts keys create ./service-key.json --iam-account $SERVICE_ACCOUNT_NAME@$PROJECT_ID.iam.gserviceaccount.com