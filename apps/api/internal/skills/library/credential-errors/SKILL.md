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
  synced by External Secrets (label `reconcile.external-secrets.io/managed`)
  names its ExternalSecret in its ownerReferences: read it
  (get_resource_yaml, type externalsecrets). A refreshTime over two
  refreshIntervals old means the sync stopped, even under Ready=True. Its
  remoteRef key and store
  (secretstores, clustersecretstores: provider, region) name the source. A
  controller with no ready replica (its Deployment, often in
  `external-secrets`) syncs nothing.
- **Written, read, and still rejected:** the source and the database disagree,
  for example a rotation that changed one of them. Name both times.

**Name the change at its source** whenever you can read it, even when the fix is
in the cluster: when it changed and who changed it. On AWS, with the
ExternalSecret's remote key: `secretsmanager DescribeSecret` gives
LastRotatedDate and LastChangedDate, and CloudTrail by that ResourceName gives
who. Logins rejected right after a rotation mean the database took the new
password: the rotation reached it, whatever the version stages show. With no key to go on, ask CloudTrail for the writes in the minutes before
the first failure. Never list the store's secrets, and never read a value.
