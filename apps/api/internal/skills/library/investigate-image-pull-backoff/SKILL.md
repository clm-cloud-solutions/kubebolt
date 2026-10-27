---
name: investigate-image-pull-backoff
description: How to investigate an image that cannot be pulled (ImagePullBackOff): a tag that does not exist, or a registry or identity problem.
listed: false
---
get_resource_describe on the pod (events have the pull error), then
get_workload_history.

Read the exact pull error in the pod events and tell the two families apart:
'not found' / 'manifest unknown' (the image or tag does not exist — a bad deploy
a rollback can fix) versus 'unauthorized' / 'denied' / 403 / 'no basic auth
credentials' (an identity or registry problem). For a cloud registry — ECR
(*.dkr.ecr.<region>.amazonaws.com), ACR (*.azurecr.io), Artifact Registry
(*-docker.pkg.dev) or gcr.io — and when you have cloud read tools, check that
the repository exists and holds the tag, and whether the NODE identity can still
pull from it (the node's instance role, kubelet managed identity or node service
account, and the registry's own access policy). A pull permission lost on the
node identity breaks every image from that registry, including the one a
rollback would restore: say so in rootCauseSummary.
