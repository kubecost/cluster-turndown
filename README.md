# Cluster Turndown
Cluster Turndown is an automated scaledown and scaleup of a Kubernetes cluster's backing nodes based on a custom schedule and turndown criteria. This feature can be used to reduce spend during down hours and/or reduce surface area for security reasons. See more at the end of this document on the strategies used for turning down clusters from different cloud providers.

**Note: Cluster Turndown is currently in _ALPHA_**

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

There is a helper bash script located at `scripts/gke-create-service-key.sh` which will:
* Create a new Role with the permissions above
* Create a new Service Account
* Assign the new Service Account the custom Role
* Generate a JSON Service Key `service-key.json`
* Use `kubectl` to create a kubernetes secret containing the service-key in the provided namespace

Note that in order to run `create-service-key.sh` successfully, you will need:
* Google Cloud, `gcloud` installed and authenticated. 
* `kubectl` installed  with target cluster in kubeconfig
    * `kubectl config current-context` should point to the target cluster before running the script.

The easiest way to use this script is to run:

```bash
$ ./scripts/gke-create-service-key.sh <Project ID> <Service Account Name> <Namespace>
```
The parameters to supply the script are as follows:
* **Project ID**: The GCP project identifier you can find via: `gcloud config get-value project`
* **Service Account Name**: The desired service account name to create
* **Namespace**: The kubernetes namespace to create the secret in.

Note that if you have run this script more than once, the custom permissions role may have already been created. You may see an error similar to the following:
```
ERROR: (gcloud.iam.roles.create) Resource in project [PROJECT_ID] is the subject of a conflict: A role named cluster.turndown in projects/[PROJECT_ID] already exists.
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
$ kubectl create secret generic cluster-turndown-service-key -n <install-namespace> --from-file=service-key.json
```

---

#### Enabling the Turndown Deployment
In order to get the `cluster-turndown` pod running on your cluster, you'll need to `kubectl apply -f `artifacts/cluster-turndown-full.yaml` **after** making a single adjustment (see below). In this yaml, you'll find the definitions for the following:
* `ServiceAccount`
* `ClusterRole` 
* `ClusterRoleBinding`
* `PersistentVolumeClaim`
* `Deployment`
* `Service`

In order for the yaml to apply correctly, you'll need to update the `ClusterRoleBinding.subject.namespace` to match your install namespace. This value appears as `$$YOUR_NAMESPACE_HERE$$` temporarily, so it should be easy to locate. For example, if you plan on using `mynamespace` as your namespace name, then modify the `ClusterRoleBinding` in `artifacts/cluster-turndown-full.yaml` replacing `$$YOUR_NAMESPACE_HERE$$` with `mynamespace`. 

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-turndown
  labels:
    app: cluster-turndown
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-turndown
subjects:
  - kind: ServiceAccount
    name: cluster-turndown
    namespace: mynamespace
```

and you should applly the yaml like so:

```bash
$ kubectl apply -f artifacts/cluster-turndown-full.yaml -n mynamespace
```

#### Verify the Pod is Running
You can verify that the pod is running by issuing the following:

```bash
$ kubectl get pods -l app=cluster-turndown -n <install-namespace>
```

---
### Setting a Turndown Schedule
Cluster Turndown uses a Kubernetes Custom Resource Definition to create schedules. There is an example resource located at `artifacts/example-schedule.yaml`:

```yaml
apiVersion: kubecost.k8s.io/v1alpha1
kind: TurndownSchedule
metadata:
  name: example-schedule
  finalizers:
  - "finalizer.kubecost.k8s.io"
spec:
  start: 2020-03-12T00:00:00Z
  end: 2020-03-12T12:00:00Z
  repeat: daily
```

This definition will create a schedule that starts by turning down at the designated `start` date-time and turning back up at the designated `end` date-time. There are three possible values for `repeat`:
* **none**: Single schedule turndown and turnup. 
* **daily**: Start and End times will reschedule every 24 hours.
* **weekly**: Start and End times will reschedule every 7 days.

To create this schedule, you may modify `example-schedule.yaml` to your desired schedule and run:

```bash
$ kubectl apply -f artifacts/example-schedule.yaml
```

**_NOTE_**: The `turndownschedule` resource can be listed via `kubectl` as well:

```bash
$ kubectl get turndownschedules
```

or using the shorthand:

```bash
$ kubectl get tds
```


### Cancelling a Schedule During Turndown
A turndown can be cancelled before turndown actually happens or after. This is performed by deleting the resource:

```bash
$ kubectl delete tds example-schedule
```

Note that cancelling while turndown is in the act of scaling down or up will result in a delayed cancellation, as the schedule must complete it's operation before processing the deletion/cancellation.

Additionally, if the turndown schedule is cancelled between a turndown and turn up, the turn up will occur automatically upon cancel. 

#### Limitations
* The internal scheduler only allows one schedule at a time to be used. Any additional schedule resources created will fail (`kubectl get tds -o yaml` will display the status).
* **DO NOT** attempt to `kubectl edit` a turndown schedule. This is currently not supported.

### Turndown Strategies

#### GKE Masterless Strategy
When the turndown schedule occurs, a new node pool with a single g1-small node is created. Taints are added to this node to only allow specific pods to be scheduled there. We update our kubecost-turndown deployment such that the turndown pod is allowed to schedule on the singleton node. Once the pod is moved to the new node, it will start back up and resume scaledown. This is done by cordoning all nodes in the cluster (other than our new g1-small node), and then reducing the node pool sizes to 0.

#### GKE Autoscaler Strategy
Whenever there exists at least one NodePool with the cluster-autoscaler enabled, the turndown will resize all non-autoscaling nodepools to 0, and schedule the turndown pod on one of the autoscaler nodepool nodes. Once it is brought back up, it will start a process called "flattening" which attempts to set deployment replicas to 0, turn off jobs, and annotate pods with labels that allow the autoscaler to do the rest of the work. Flattening persists pre-turndown values in the annotations of Kubernetes objects. When turn up occurs, deployments and daemonsets are "expanded" to their original sizes/replicas. There are four annotations that can be applied for this process:
* **kubecost.kubernetes.io/job-suspend**: Stores a bool containing the previous paused state of a kubernetes CronJob.
* **kubecost.kubernetes.io/turn-down-replicas**: Stores the previous number of replicas set on the deployment. 
* **kubecost.kubernetes.io/turn-down-rollout**: Stores the previous maxUnavailable for the deployment rollout. 
* **kubecost.kubernetes.io/safe-evict**: For autoscaling clusters, we use the `cluster-autoscaler.kubernetes.io/safe-to-evict` to have the autoscaler do the work for us. We want to make sure we preserve any deployments that previously had this annotation set, so when we scale back up, we don’t reset this value unintentionally. 

#### AWS Standard Strategy
This turndown strategy schedules the turndown pod on the Master node, then resizes all Auto Scaling Groups other than the master to 0. Similar to flattening in GKE, the previous min/max/current values of the ASG prior to turndown will be set on the tag. When turn up occurs, those values can be read from the tags and restored to their original sizes. For the standard strategy, turn up will reschedule the turndown pod off the Master upon completion (occurs 5 minutes after turn up). This is to allow any modifications via kops without resetting any cluster specific scheduling setup by turndown. The **tag** label used to store the min/max/current values for a node group is `kubecost.turndown.previous`. Once turn up happens and the node groups are resized to their original size, the tag is deleted.
