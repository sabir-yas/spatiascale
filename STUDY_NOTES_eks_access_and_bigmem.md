# Study notes: EKS console access entries + the bigmem node group attempt

Personal reference log of the exact commands and outputs from debugging EKS
console access and attempting (then abandoning) a temporary big-memory node
group for a full-scale ingestion test. Not part of the project's working
docs — just for reviewing later to understand what each command actually did.

## 1. EKS console access entries

**Problem:** the AWS Console EKS page said "Your current IAM principal
doesn't have access to Kubernetes objects on this cluster," even though
`kubectl` worked fine.

**Why both can be true at once:** `kubectl` authenticates straight against
the Kubernetes API server using your IAM credentials (via the AWS EKS auth
plugin) — it doesn't go through the AWS Console at all. The Console's EKS
page, though, needs the cluster's `authenticationMode` to include `API`, and
needs an explicit *access entry* mapping your IAM principal to a Kubernetes
RBAC policy, before it can render pod/node detail for you. Two separate
authorization paths to the same cluster.

### Check current auth mode

```
aws eks describe-cluster --name spatiascale --region us-east-1 --query "cluster.accessConfig" --output json
```
```json
{
    "authenticationMode": "API_AND_CONFIG_MAP"
}
```
`API_AND_CONFIG_MAP` means both the old ConfigMap-based auth (what `kubectl`
was already using) and the newer API-based access-entry system are active.
This had already been switched from `CONFIG_MAP`-only earlier in the session
(a change that took a surprisingly long time to finish propagating).

### Get your own identity (need the ARN to register)

```
aws sts get-caller-identity --output json
```
```json
{
    "UserId": "AIDAWLQNGR7DFNM3VY6DD",
    "Account": "437040615366",
    "Arn": "arn:aws:iam::437040615366:user/spatialscale-terraform"
}
```

### Create the access entry (registers the IAM principal with the cluster)

```
aws eks create-access-entry --cluster-name spatiascale --region us-east-1 \
  --principal-arn arn:aws:iam::437040615366:user/spatialscale-terraform
```

### Associate an access policy (grants actual permissions)

```
aws eks associate-access-policy --cluster-name spatiascale --region us-east-1 \
  --principal-arn arn:aws:iam::437040615366:user/spatialscale-terraform \
  --policy-arn arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy \
  --access-scope type=cluster
```
Two separate calls because they're two separate concepts: `create-access-entry`
just says "this IAM principal is known to the cluster," `associate-access-policy`
says "and here's what it's allowed to do" (cluster-admin, scoped to the whole
cluster rather than one namespace).

### Verify both landed

```
aws eks describe-access-entry --cluster-name spatiascale --region us-east-1 \
  --principal-arn arn:aws:iam::437040615366:user/spatialscale-terraform \
  --query "accessEntry.{arn:principalArn,createdAt:createdAt}" --output json
```
```json
{
    "arn": "arn:aws:iam::437040615366:user/spatialscale-terraform",
    "createdAt": "2026-07-06T12:47:31.749000-06:00"
}
```

```
aws eks list-associated-access-policies --cluster-name spatiascale --region us-east-1 \
  --principal-arn arn:aws:iam::437040615366:user/spatialscale-terraform --output json
```
```json
{
    "associatedAccessPolicies": [
        {
            "policyArn": "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy",
            "accessScope": { "type": "cluster", "namespaces": [] },
            "associatedAt": "2026-07-06T12:47:31.813000-06:00",
            "modifiedAt": "2026-07-06T13:56:47.245000-06:00"
        }
    ],
    "clusterName": "spatiascale",
    "principalArn": "arn:aws:iam::437040615366:user/spatialscale-terraform"
}
```

**Lesson:** all of this was already correct on the AWS side, but the Console
kept showing the error anyway — a UI caching issue, not a real permissions
gap. Confirmed by cross-checking `aws sts get-caller-identity` (which IAM
principal you actually are) against `kubectl config current-context` (which
cluster kubectl is pointed at) and `aws eks list-access-entries` (who the
cluster actually recognizes) — all three agreed, so the CLI/API layer was
never the problem.

**Near-mistake avoided:** the Console's own "create access entry" wizard
defaulted to `arn:aws:iam::437040615366:root` (the AWS account root
principal) rather than the actual IAM user being used
(`spatialscale-terraform`). Granting cluster-admin to root would have been
unnecessary and unwise — always double check *which* principal ARN a
generated form is about to grant before confirming.

## 2. The bigmem node group attempt (and why it failed)

**Goal:** temporarily add an `r5.2xlarge` node (needs ~50GB RAM, based on
the real Alpine HPC ingestion run's peak RSS) to test the full 155M-row
dataset on EKS instead of just the 480K-point sample.

### Terraform plan (this part worked)

```
terraform plan -out=tfplan
```
Output showed exactly 2 additions: `aws_eks_node_group.bigmem` (r5.2xlarge,
SPOT, desired=1) and `aws_iam_role_policy.eks_node_s3_read` (lets any node's
IAM role `s3:GetObject` on the transcripts bucket). Nothing else touched.

```
terraform apply "tfplan"
```
The S3 IAM policy applied instantly. The node group got stuck:
```
aws_eks_node_group.bigmem: Still creating... [00m10s elapsed]
...
aws_eks_node_group.bigmem: Still creating... [10m30s elapsed]
```

### Diagnosing the stuck create

Checking the node group directly showed no reported issue — status alone
wasn't enough info:
```
aws eks describe-nodegroup --cluster-name spatiascale --nodegroup-name spatiascale-bigmem \
  --region us-east-1 --query "nodegroup.{status:status,health:health}" --output json
```
```json
{ "status": "CREATING", "health": { "issues": [] } }
```

The real signal was one layer down, in the Auto Scaling Group EKS creates
behind the scenes for a node group:
```
aws autoscaling describe-auto-scaling-groups --region us-east-1 \
  --query "AutoScalingGroups[?contains(Tags[?Key=='eks:nodegroup-name'].Value, 'spatiascale-bigmem')].{Name:AutoScalingGroupName,Desired:DesiredCapacity,Min:MinSize,Max:MaxSize,Instances:Instances}" \
  --output json
```
```json
[{
    "Name": "eks-spatiascale-bigmem-34cf9c82-5216-e115-0b9e-b25aa17f1543",
    "Desired": 1, "Min": 1, "Max": 1,
    "Instances": []
}]
```
`Desired: 1` but `Instances: []` — it wants one instance and has zero. That's
the tell that launches are failing, not just running slow.

The actual error came from the ASG's activity history:
```
aws autoscaling describe-scaling-activities \
  --auto-scaling-group-name "eks-spatiascale-bigmem-34cf9c82-5216-e115-0b9e-b25aa17f1543" \
  --region us-east-1 --max-items 5 \
  --query "Activities[].{Status:StatusCode,Reason:StatusMessage,Time:StartTime}" --output json
```
```json
[{
    "Status": "Failed",
    "Reason": "Could not launch Spot Instances. InvalidParameterCombination - The specified instance type is not eligible for Free Tier. For a list of Free Tier instance types, run 'describe-instance-types' with the filter 'free-tier-eligible=true'. Launching EC2 instance failed.",
    "Time": "2026-07-06T20:08:12.997000+00:00"
}, ...]
```
Root cause: this AWS account (org/edu credits account) is restricted to
Free-Tier-eligible instance types only. `r5.2xlarge` was rejected on every
retry attempt — not a capacity/availability problem, a hard account policy.

### Checking what instance types were actually allowed

```
aws ec2 describe-instance-types --filters "Name=free-tier-eligible,Values=true" \
  --region us-east-1 --query "InstanceTypes[].{Type:InstanceType,vCPU:VCpuInfo.DefaultVCpus,MemGiB:MemoryInfo.SizeInMiB}" \
  --output json
```
```json
[
  {"Type": "c7i-flex.large", "vCPU": 2, "MemGiB": 4096},
  {"Type": "t4g.small",      "vCPU": 2, "MemGiB": 2048},
  {"Type": "t3.micro",       "vCPU": 2, "MemGiB": 1024},
  {"Type": "t4g.micro",      "vCPU": 2, "MemGiB": 1024},
  {"Type": "t3.small",       "vCPU": 2, "MemGiB": 2048},
  {"Type": "m7i-flex.large", "vCPU": 2, "MemGiB": 8192}
]
```
Ceiling is 8GB RAM (`m7i-flex.large`) — nowhere near the ~50GB the full
155M-row ingestion needs in memory. Conclusion: this specific test isn't
achievable on this AWS account without a Free Tier restriction lift, and
wasn't worth pursuing further since the Alpine HPC run (see CLAUDE.md)
already provides real, defensible 500M+-row-class evidence at that memory
scale — repeating it on EKS wouldn't add a new claim, just duplicate a
number already measured.

### Tearing it down

```
terraform destroy -target="aws_eks_node_group.bigmem" -target="aws_iam_role_policy.eks_node_s3_read"
```
(Note: without the quotes around each `-target=...` value, PowerShell threw
`Error: Too many command line arguments` / `Error: Invalid target` — quoting
the whole `-target=resource.name` string fixed it.)

The IAM policy destroyed in 1 second. The node group took **21m17s** to
destroy — much slower than its own failed creation attempts. Likely
explanation: EKS was internally retrying a graceful scale-to-zero on the ASG
(which kept hitting the same Free-Tier rejection) before falling back to a
force-delete path. Throughout, `Instances: []` the whole time — no EC2
instance ever actually launched or ran, so no compute was ever billed for
this attempt, just the ~30 minutes of API-level create/destroy churn.

### Confirming clean teardown afterward

```
aws eks list-nodegroups --cluster-name spatiascale --region us-east-1 --output json
```
```json
{ "nodegroups": ["spatiascale-nodes"] }
```

```
aws autoscaling describe-auto-scaling-groups --region us-east-1 \
  --query "AutoScalingGroups[?contains(Tags[?Key=='eks:nodegroup-name'].Value, 'spatiascale-bigmem')]" --output json
```
```json
[]
```

```
terraform plan
```
```
No changes. Your infrastructure matches the configuration.
```
All three confirm: back to exactly the normal 2-node `t3.small` topology,
no drift between real AWS state and the Terraform config.

## Takeaways to remember

- `describe-nodegroup`'s `status`/`health` fields can look totally clean
  (`"issues": []`) while something is actually failing one layer down in the
  ASG. When an EKS node group seems stuck, check the ASG's `Instances` count
  and `describe-scaling-activities` for the real error — the node group API
  itself doesn't always surface it.
- Free Tier / restricted AWS accounts enforce instance-type allowlists at
  the EC2 launch level, not at Terraform plan/validate time — `terraform
  validate` and `terraform plan` both looked perfectly fine here; the
  rejection only happens when AWS actually tries to launch the instance.
- Destroy can take meaningfully longer than create when the resource never
  successfully came up in the first place — don't assume "still destroying"
  past the create time means something is newly wrong.
- Two independent EKS authorization systems exist side by side
  (ConfigMap-based, used by `kubectl`; access-entry-based, used by the
  Console) and can be out of sync with each other without indicating a real
  problem — cross-check identity/config/entries directly via CLI before
  assuming a Console error reflects a real gap.
