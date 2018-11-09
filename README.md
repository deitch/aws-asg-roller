# Amazon Web Services AutoScaling Group Roller

Rolling updates for AWS AutoScaling Groups!

AWS AutoScaling Groups (ASG) are wonderful. They let you declare a config, a minimum and maximum number of ec2 nodes, a desired number, and it keeps that number going for you. It even lets you set it up to scale up or down automatically based on cloudwatch events, effectively adjusting the desired number of nodes in the ASG, in response to load.

The challenge is: how do you update it?

If you change the launch configuration, it does **not** cause new nodes to be rolled in. Even if it did, you would want them to roll in sanely and slowly, one at a time, rather than all at once. Further, you may have app-specific "readiness" requirements, of which AWS simply isn't aware. For example, if you are running Kubernetes workloads on the nodes, you may want to drain the nodes _before_ terminating a node.

[Terraform](https://terraform.io) does a decent job, with a little extra work, making a blue/green deployment: 

1. Create a new auto-scaling group
2. Make sure all of the nodes in the new ASG are functioning
3. Terminate the old one

If this is good enough for you, check out either [this](https://medium.com/@endofcake/using-terraform-for-zero-downtime-updates-of-an-auto-scaling-group-in-aws-60faca582664) or [this](https://www.joshdurbin.net/posts/2018-05-auto-scaling-rollout-on-aws-with-terraform/) blog post.

However, even if this "big bang" switchover works for you, you _still_ might want app-specific "readiness" before rolling over. To use our previous example, drain all of the existing Kubernetes workers before destroying the old ASG. The above blue/green examples do not work.

The other offerred solution is to use CloudFormation. While the AWS ASG API does not offer rolling updates, AWS CloudFormation does. You can set the update method to `RollingUpdate`, and a change in the launch configuration will cause AWS to add a new node and terminate an old one when the new one is ready, one at a time, until all of the nodes are running the new configuration.

This has two challenges:

1. You might not be using CloudFormation, or want to mix it into your terraform or other deployer.
2. AWS _still_ doesn't know about your app being ready (drained those kubernetes workers yet?).

## ASG Roller
Enter ASG Roller.

ASG Roller is a simple service that watches your AWS ASG, checks the nodes, and, if the nodes are not in sync with the config, updates them.

The update methodology is simple:

1. Increment `desired` setting.
2. Watch the new node come online.
3. When new node is ready, select and terminate one old node.
4. Repeat until the number of nodes with the correct configuration matches the _original_ `desired` setting. At this point, there is likely to be one old node left.
5. Decrement the `desired` setting. 

## App Awareness
In addition to the above, ASG Roller is able to insert app-specific logic at two distinct points:

* Testing if the new node is "ready for usage".
* Preparing the old node for terminnation.

### Ready for Usage
AWS's definition of "ready for usage" normally is one of:

* node is up and running
* node responds to ELB health checks, one of TCP or other supported protocol checks (like HTTP)

In addition, ASG Roller supports specific logic, such as checking if Kubernetes registers the node as online and `Ready`. As of this writing, the only supported method is Kubernetes node, but others are in the works, and we are happy to accept pull requests for more.

### Preparing for Termination
Prior to terminating the old node, ASG Roller can execute commands to prepare the node for termination. AWS ASG does nothing other than shutting the node down. While well-built apps should be able to handle termination of a node without disruption, in real-world scenarios we often prefer a clean shutdown.

We can execute such a clean shutdown via pluggable commands.

As of this writing, the only supported method is Kubernetes draining, but others are in the works, and we are happy to accept pull requests for more.

## Deployment
ASG Roller is available as a docker image. To run on a node:

```
docker run -d deitch/aws-asg-roller:<version>
```

### Permissions
AWS ASG Roller requires IAM rights to:

* Read the information about an ASG
* Modify the min, max and desired parameters of an ASG
* Read the launch configuration for an ASG
* Terminate ASG nodes

These permissions are as follows:

```
```

These permissions can be set either via runninn ASG Roller on an AWS node that has the correct role, or via API keys to a user that has the correct roles/permissions.

* If the AWS environment variables `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`are set, it will use those
* If the AWS environment variables are not set, it will fall back to relying on the local node's IAM role

### Running in Kubernetes
To run in Kubernetes:

```yml
apiVersion: core/v1
kind: ServiceAccount
metadata:
  name: asg-roller
  labels:
    name: asg-roller
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1beta1
kind: ClusterRole
metadata:
  name: asg-roller
  labels:
    name: asg-roller
rules:
  - apiGroups:
      - "*"
    resources:
      - "*"
    verbs:
      - get
      - list
      - watch
  - apiGroups:
      - "*"
    resources:
      - nodes
    verbs:
      - get
      - list
      - watch
      - update
      - patch
  - apiGroups:
      - "*"
    resources:
      - pods/eviction
    verbs:
      - get
      - list
      - create
  - apiGroups:
      - "*"
    resources:
      - pods
    verbs:
      - get
      - list
---
apiVersion: rbac.authorization.k8s.io/v1beta1
kind: ClusterRoleBinding
metadata:
  name: asg-roller
  labels:
    name: asg-roller
roleRef:
  kind: ClusterRole
  name: asg-roller
  apiGroup: rbac.authorization.k8s.io
subjects:
  - kind: ServiceAccount
    name: asg-roller
    namespace: kube-system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: aws-asg-roller
  labels:
    name: aws-asg-roller
  namespace: kube-system # or in another namespace, if you prefer
spec:
  replicas: 1
  template:
    metadata:
      labels:
        name: aws-asg-roller
    spec:
      containers:
      - name: aws-asg-roller
        # the below is if you are using AWS credentials; if you are relying on the node's IAM role, remove the `envFrom` section
        envFrom:
        - secretRef:
            name: aws-asg-roller
        image: 'deitch/aws-asg-roller'
        imagePullPolicy: Always
      restartPolicy: Always
      serviceAccountName: asg-roller
      # to allow it to run on master
      tolerations:
        - effect: NoSchedule
          operator: Exists
      # we specifically want to run on master - remove the remaining lines if you do not care where it runns
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                - key: kubernetes.io/role
                  operator: In
                  values: ["master"]
```

Several key areas of potential modification:

* Allowed to run on master nodes: the above config allows it to run on master nodes. If you do not want to allow it to run on masters, remove the `tolerations`
* Required to run on master nodes: the above config requires it to run on master nodes. If you do not want to require it on masters, remove the `affinity`
* Image version: use a real version; don't use no tag (implying `latest`)
* Credentials: the above example reads the AWS credentials as environment variables from a secret named `aws-asg-roller`. If you have a different secret, use that one; if you are relying on host IAM roles, remove the `envFrom` entirely.


## Configuration
ASG Roller takes its configuration via environment variables. All environment variables that affect ASG Roller begin with `ROLLER_`.

* `ROLLER_KUBE`: If set to `true`, will check if a new node is ready via-a-vis Kubernetes before declaring it "ready", and will drain an old node before eliminating it. Defaults to `true` when running in Kubernetes as a pod, `false` otherwise.

