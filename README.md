# Cluster Turndown
Cluster Turndown is an automated scaledown and scaleup of a Kubernetes cluster's backing nodes based on a custom schedule and turndown criteria. This feature can be used to reduce spend during down hours and/or reduce surface area for security reasons. See more at the end of this document on the strategies used for turning down clusters from different cloud providers.

**Note: Cluster Turndown is currently in _ALPHA_**

### GKE Setup

We have provided a shell script capable of performing the required steps in setting up a service account for use with `cluster-turndown`. [More info](scripts/README.md) 
    
##### Running the Setup Script
To use [this setup script](scripts/gke-create-service-key.sh) supply the following parameters:

```bash
$ ./scripts/gke-create-service-key.sh <Project ID> <Service Account Name>
```
The parameters to supply the script are as follows:
* **Project ID**: The GCP project identifier you can find via: `gcloud config get-value project`
* **Service Account Name**: The desired service account name to create, e.g. `turndown-service`

---

### EKS & AWS Kops Setup

For EKS cluster provisioning, if using `eksctl`, make sure that you use the `--managed` option when creating the cluster. Unmanaged nodegroups should be upgraded to managed. [More info](https://eksctl.io/usage/eks-managed-nodes/). 

Create a new User or IAM role with **AutoScalingFullAccess** permissions. 

JSON definition of those permissions:
```
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": "autoscaling:*",
            "Resource": "*"
        },
        {
            "Effect": "Allow",
            "Action": "cloudwatch:PutMetricAlarm",
            "Resource": "*"
        },
        {
            "Effect": "Allow",
            "Action": [
                "ec2:DescribeAccountAttributes",
                "ec2:DescribeAvailabilityZones",
                "ec2:DescribeImages",
                "ec2:DescribeInstanceAttribute",
                "ec2:DescribeInstances",
                "ec2:DescribeKeyPairs",
                "ec2:DescribeLaunchTemplateVersions",
                "ec2:DescribePlacementGroups",
                "ec2:DescribeSecurityGroups",
                "ec2:DescribeSpotInstanceRequests",
                "ec2:DescribeSubnets",
                "ec2:DescribeVpcClassicLink"
            ],
            "Resource": "*"
        },
        {
            "Effect": "Allow",
            "Action": [
                "elasticloadbalancing:DescribeLoadBalancers",
                "elasticloadbalancing:DescribeTargetGroups"
            ],
            "Resource": "*"
        },
        {
            "Effect": "Allow",
            "Action": "iam:CreateServiceLinkedRole",
            "Resource": "*",
            "Condition": {
                "StringEquals": {
                    "iam:AWSServiceName": "autoscaling.amazonaws.com"
                }
            }
        }
    ]
}
```

Create a new file, service-key.json, and use the access key id and secret access key to fill out the following template:


```json
{
    "aws_access_key_id": "<ACCESS_KEY_ID>",
    "aws_secret_access_key": "<SECRET_ACCESS_KEY>"
}
```

Then run the following to create the turndown namespace:

```bash
$ kubectl apply -f artifacts/turndown-namespace.yaml
```

Then run the following to create the secret:

```bash
$ kubectl create secret generic cluster-turndown-service-key -n turndown --from-file=service-key.json
```

---

## Deploying
After completing setup, run the following command to get the `cluster-turndown` pod running on your cluster:

```bash
$ kubectl apply -f https://github.com/kubecost/cluster-turndown/releases/latest/download/cluster-turndown-full.yaml
```

In this yaml, you'll find the definitions for the following:

* `ServiceAccount`
* `ClusterRole` 
* `ClusterRoleBinding`
* `Deployment`

#### Verify the Pod is Running
You can verify that the pod is running by issuing the following:

```bash
$ kubectl get pods -l app=cluster-turndown -n turndown
```

---
## Setting a Turndown Schedule
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

This definition will create a schedule that starts by turning down at the designated `start` date-time and turning back up at the designated `end` date-time. Both the `start` and `end` times should be in [RFC3339](https://tools.ietf.org/html/rfc3339) format, i.e. times based on offsets to UTC. There are three possible values for `repeat`:
* **none**: Single schedule turndown and turnup. 
* **daily**: Start and End times will reschedule every 24 hours.
* **weekly**: Start and End times will reschedule every 7 days.

To create this schedule, you may modify `example-schedule.yaml` to your desired schedule and run:

```bash
$ kubectl apply -f artifacts/example-schedule.yaml
```

## Viewing a Turndown Schedule
The `turndownschedule` resource can be listed via `kubectl` as well:

```bash
$ kubectl get turndownschedules
```

or using the shorthand:

```bash
$ kubectl get tds
```

Details regarding the status of the turndown schedule can be found by outputting as json or yaml:

```bash
$ kubectl get tds example-schedule -o yaml

apiVersion: kubecost.k8s.io/v1alpha1
kind: TurndownSchedule
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"kubecost.k8s.io/v1alpha1","kind":"TurndownSchedule","metadata":{"annotations":{},"finalizers":["finalizer.kubecost.k8s.io"],"name":"example-schedule"},"spec":{"end":"2020-03-17T00:35:00Z","repeat":"daily","start":"2020-03-17T00:20:00Z"}}
  creationTimestamp: "2020-03-17T00:18:39Z"
  finalizers:
  - finalizer.kubecost.k8s.io
  generation: 1
  name: example-schedule
  resourceVersion: "33573"
  selfLink: /apis/kubecost.k8s.io/v1alpha1/turndownschedules/example-schedule
  uid: d9b16aed-67e4-11ea-b591-42010a8e0075
spec:
  end: "2020-03-17T00:35:00Z"
  repeat: daily
  start: "2020-03-17T00:20:00Z"
status:
  current: scaledown
  lastUpdated: "2020-03-17T00:36:39Z"
  nextScaleDownTime: "2020-03-18T00:21:38Z"
  nextScaleUpTime: "2020-03-18T00:36:38Z"
  scaleDownId: 38ebf595-4e2b-46e9-951a-1e3ceff30536
  scaleDownMetadata:
    repeat: daily
    type: scaledown
  scaleUpID: 869ec89f-a8d8-450b-9ebb-71cd4d7fbaf8
  scaleUpMetadata:
    repeat: daily
    type: scaleup
  state: ScheduleSuccess
```

The `Status` field displays the current status of the schedule including next schedule times, specific schedule identifiers, and the overall state of schedule.

* **State**: The state of the turndown schedule. This can be:
  * **ScheduleSuccess**: The schedule has been set and is waiting to run. 
  * **ScheduleFailed**: The scheduling failed due to a schedule already existing, scheduling for a date-time in the past.
  * **ScheduleCompleted**: For schedules with `repeat: none`, the schedule will move to a completed state after turn up. 
* **Current**: The next action to run.
* **LastUpdated**: The last time the status was updated on the schedule.
* **NextScaleDownTime**: The next time a turndown will be executed.
* **NextScaleUpTime**: The next time at turn up will be executed.
* **ScaleDownId**: Specific identifier assigned by the internal scheduler for turndown.
* **ScaleUpId**: Specific identifier assigned by the internal scheduler for turn up.
* **ScaleDownMetadata**: Metadata attached to the scaledown job, assigned by the turndown scheduler.
* **ScaleUpMetadata**: Metadata attached to the scale up job, assigned by the turndown scheduler.

## Cancelling a Turndown Schedule
A turndown can be cancelled before turndown actually happens or after. This is performed by deleting the resource:

```bash
$ kubectl delete tds example-schedule
```

Note that cancelling while turndown is in the act of scaling down or scaling up will result in a delayed cancellation, as the schedule must complete it's operation before processing the deletion/cancellation.

If the turndown schedule is cancelled between a turndown and turn up, the turn up will occur automatically upon cancel. 

### Limitations
* The internal scheduler only allows one schedule at a time to be used. Any additional schedule resources created will fail (`kubectl get tds -o yaml` will display the status).
* **DO NOT** attempt to `kubectl edit` a turndown schedule. This is currently not supported. Recommended approach for modifying is to delete and then create a new schedule.
* 20-minute minimim time window between start and end of turndown schedule

## Turndown Strategies

#### GKE Masterless Strategy
When the turndown schedule occurs, a new node pool with a single g1-small node is created. Taints are added to this node to only allow specific pods to be scheduled there. We update our cluster-turndown deployment such that the turndown pod is allowed to schedule on the singleton node. Once the pod is moved to the new node, it will start back up and resume scaledown. This is done by cordoning all nodes in the cluster (other than our new g1-small node), and then reducing the node pool sizes to 0.

#### GKE Autoscaler Strategy
Whenever there exists at least one NodePool with the cluster-autoscaler enabled, the turndown will resize all non-autoscaling nodepools to 0, and schedule the turndown pod on one of the autoscaler nodepool nodes. Once it is brought back up, it will start a process called "flattening" which attempts to set deployment replicas to 0, turn off jobs, and annotate pods with labels that allow the autoscaler to do the rest of the work. Flattening persists pre-turndown values in the annotations of Kubernetes objects. When turn up occurs, deployments and daemonsets are "expanded" to their original sizes/replicas. There are four annotations that can be applied for this process:
* **kubecost.kubernetes.io/job-suspend**: Stores a bool containing the previous paused state of a kubernetes CronJob.
* **kubecost.kubernetes.io/turn-down-replicas**: Stores the previous number of replicas set on the deployment. 
* **kubecost.kubernetes.io/turn-down-rollout**: Stores the previous maxUnavailable for the deployment rollout. 
* **kubecost.kubernetes.io/safe-evict**: For autoscaling clusters, we use the `cluster-autoscaler.kubernetes.io/safe-to-evict` to have the autoscaler do the work for us. We want to make sure we preserve any deployments that previously had this annotation set, so when we scale back up, we don’t reset this value unintentionally. 

#### AWS kops Strategy
This turndown strategy schedules the turndown pod on the Master node, then resizes all Auto Scaling Groups other than the master to 0. Similar to flattening in GKE, the previous min/max/current values of the ASG prior to turndown will be set on the tag. When turn up occurs, those values can be read from the tags and restored to their original sizes. For the standard strategy, turn up will reschedule the turndown pod off the Master upon completion (occurs 5 minutes after turn up). This is to allow any modifications via kops without resetting any cluster specific scheduling setup by turndown. The **tag** label used to store the min/max/current values for a node group is `cluster.turndown.previous`. Once turn up happens and the node groups are resized to their original size, the tag is deleted.
