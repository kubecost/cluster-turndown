# Scripts
This directory contains scripts that will assist with `cluster-turndown`.

### GKE Service Key Creation
In order for cluster-turndown to have permissions to perform turndown operations on your cluster, it requires a service account key for GKE. This script automates service account creation with the required permissions, then creates a kubernetes secret containing the key. When the cluster-turndown application is installed on the cluster, it will automatically mount the service key created by the script.

#### Prerequisites
Note that in order to run `gke-create-service-key.sh` successfully, you will need:
* Google Cloud, `gcloud` installed and authenticated. 
* `kubectl` installed  with target cluster in kubeconfig
    * `kubectl config current-context` should point to the target cluster before running the script.

#### Required Permissions 
In order to turndown the node pools on GKE, you'll need to provide a service account key with the following permissions:
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

#### Script Breakdown
The bash script, `gke-create-service-key.sh` will perform the work required to generate a service account key and create a kubernetes secret with the key. The exact steps performed by this script are as follows:
* Create a new Role with the permissions above
* Create a new Service Account assigned the custom Role.
* Generate a JSON Service Key `service-key.json`
* Use `kubectl` to create a kubernetes namespace `turndown`
* Use `kubectl` to create a kubernetes secret containing the service-key in the `turndown` namespace

#### Running the Script
The easiest way to use this script is to run:

```bash
$ ./scripts/gke-create-service-key.sh <Project ID> <Service Account Name>
```
The parameters to supply the script are as follows:
* **Project ID**: The GCP project identifier you can find via: `gcloud config get-value project`
* **Service Account Name**: The desired service account name to create

#### Notes
If you have run this script more than once, the custom permissions role may have already been created. You may see an error similar to the following:
```
ERROR: (gcloud.iam.roles.create) Resource in project [PROJECT_ID] is the subject of a conflict: A role named cluster.turndown in projects/[PROJECT_ID] already exists.
```
This error is harmless, and the script should continue.