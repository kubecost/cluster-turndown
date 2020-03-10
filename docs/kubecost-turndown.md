# Kubecost Turndown
Kubecost Turndown is an automated scaledown and scaleup of a Kubernetes cluster's backing node pool based on a custom schedule and criteria. 

### GKE Setup

#### Service Account
In order to setup the scheduled turndown, you'll need to use a service account key with the following permissions:
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

Included with the documentation is a `gke/create-service-key.sh` bash script which will:
* Create a new Role with the permissions above
* Create a new Service Account
* Assign the new Service Account the custom Role
* Generate a JSON Service Key `service-key.json`
* Use `kubectl` to create a kubernetes secret containing the service-key in the `kubecost` namespace

Note that in order to run `create-service-key.sh` successfully, you will need:
* Google Cloud, `gcloud` installed and authenticated. 
* `kubectl` installed  with target cluster in kubeconfig
    * `kubectl config current-context` should point to the target cluster before running the script.

The easiest way to use this script is to run:

```bash
$ ./gke/create-service-key.sh <Project ID> <Service Account Name>
```
The parameters to supply the script are as follows:
* **Project ID**: The GCP project identifier you can find via: `gcloud config get-value project`
* **Service Account Name**: The desired service account name to create

Note that if you have run this script more than once, the custom permissions role may have already been created. You may see an error similar to the following:
```
ERROR: (gcloud.iam.roles.create) Resource in project [PROJECT_ID] is the subject of a conflict: A role named kubecost.turndown in projects/[PROJECT_ID] already exists.
```
This error is harmless, as the script should continue.

---

### AWS (Kops) Setup

The process for AWS clusters is mostly the same; however, there is not an automated script which creates the secret for you. Instead, you will want to create a new file `service-key.json` and use this template to fill in the proper values:
```json
{
    "aws_access_key_id": "<ACCESS_KEY_ID>",
    "aws_secret_access_key": "<SECRET_ACCESS_KEY>"
}
```

Then run the following to create the secret:
```bash
$ kubectl create secret generic kubecost-turndown-service-key -n <install-namespace> --from-file=service-key.json
```

---

#### Enabling the Turndown Deployment
In order to get the `kubecost-turndown` pod running on your cluster, you can set `turndown.enabled` in `values.yaml` to `true`, or adding the `--set turndown.enabled=true` to `helm install/upgrade` This will create a the following for cluster specific access via the Kubernetes API:
* `ServiceAccount`
* `ClusterRole` 
* `ClusterRoleBinding`
* `PersistentVolumeClaim`
* `Deployment`
* `Service`

#### Verify the Pod is Running
You can verify that the pod is running by issuing the following:

```bash
$ kubectl get pods -l app=kubecost-turndown -n <install-namespace>
```

---
### Setting a Turndown Schedule
To use the `kubecost-turndown` service, you can navigate to [@Webb: Want to document frontend use here?]


### Cancelling a Schedule During Turndown
When the cluster is in the turndown state, only the `kubecost-turndown` pod will be reachable. In order to cancel the currrent turndown schedule and turn up the cluster, you will need to `kubectl port-forward` to the service or directly to the pod on port `9731`. Then, issue a `cURL` http request to the `/cancel` endpoint. 

To port-forward to the `kubecost-turndown-service`:
```bash
$ kubectl port-forward svc/kubecost-turndown-service -n <install-namespace> 9731
```

To cancel turndown schedule:
```bash
$ curl http://localhost:9731/cancel
```

Note that cancelling while turndown is in the act of scaling down or up will result in a failed request. Please note the response from the above endpoint to ensure success.

Additionally, if the turndown schedule is cancelled between a turndown and turn up, the turn up will occur automatically upon cancel. 

#### Helper Utility Script for Scheduling (Bash)
There is a `set-schedule.sh` bash script provided with this documentation that can assist in setting a turndown schedule granted you have setup port-forwarding to `9731` mentioned in the previous section.
* Sends a request to the scheduling endpoint via `cURL`.

Make sure that your dates are properly RFC3339 formatted.

```bash
$ ./set-schedule.sh <START> <END> <REPEAT>
```

For example, to set the scheule for Jan 10, 2020 at 7:00 PM EST (UTC-5) to Jan 11, 9:00 AM EST (UTC-5) set to repeat daily, I would use the following:

```bash
$ ./set-schedule.sh "2020-01-10T19:00:00-05:00" "2020-01-11T09:00:00-05:00" "daily"
```

### Turndown Strategies

#### GKE Masterless Strategy
When the turndown schedule occurs, a new node pool with a single `g1-small` node is created. Taints are added to this node to only allow specific pods to be scheduled there. We update our `kubecost-turndown` deployment such that the turndown pod is allowed to schedule on the singleton node. Once the pod is moved to the new node, it will start back up and resume scaledown. This is done by cordoning all nodes in the cluster (other than our new g1-small node), and then reducing the node pool sizes to 0. 

#### GKE AutoScaler Strategy
Whenever there exists at least one NodePool with the cluster-autoscaler enabled, the turndown will resize all non-autoscaling nodepools to 0, and schedule the turndown pod on one of the autoscaler nodepool nodes. Once it is brought back up, it will start a process called "flattening" which attempts to set deployment replicas to 0, turn off jobs, and annotate pods with labels that allow the autoscaler to do the rest of the work. Flattening persists pre-turndown values in the annotations of Kubernetes objects. When turn up occurs, deployments and daemonsets are "expanded" to their original sizes/replicas.

#### AWS Standard Strategy
This turndown strategy schedules the turndown pod on the Master node, then resizes all AutoScalingGroups other than the master to 0. Similar to flattening in GKE, the previous min/max/current values of the ASG prior to turndown will be set on the tag. When turn up occurrs, those values can be read from the tags and restored to their original sizes. For the standard strategy, turn up will reschedule the turndown pod off the Master upon completion. This is to allow any modifications via kops without resetting any cluster specific scheduling setup by turndown.
