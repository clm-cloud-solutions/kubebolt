---
name: credential-errors
description: A login or credential error that began at a point in time (a database rejecting the password, 28P01; an API answering 401 or AccessDenied) — whether the pods hold a stale credential, the cluster missed the new one, or the source changed. Load it before you conclude on a credential error.
---
Read the Secret the workload takes the credential from (get_resource_yaml): its
last write, against the pods' start (get_workload_pods) and the first failure
in the logs. Three cases:

- **Written after the pods started, and the pods read it at start** (an
  environment variable, or an app that reads the file once): the cluster holds
  the new credential and the pods still use the old one. Restarting them is the
  fix.
- **Not written since before the failure:** the credential changed at its
  source after the cluster last received it. A restart changes nothing: the pods
  would read the same value. Find why the cluster did not get it. A Secret
  synced by External Secrets (label `reconcile.external-secrets.io/managed`,
  owned by an ExternalSecret) is rewritten by its controller: read that
  controller's Deployment (often in `external-secrets`); no ready replica means
  nothing syncs. Then read the source's own record.
- **Written, read, and still rejected:** the source and the database disagree,
  for example a rotation that changed one of them. Name both times.

**Name the change at its source** whenever you can read it, even when the fix is
in the cluster: when it changed and who changed it. On AWS:
`secretsmanager DescribeSecret` gives LastRotatedDate and LastChangedDate, and
CloudTrail gives who rotated or changed it. Never read the value.
