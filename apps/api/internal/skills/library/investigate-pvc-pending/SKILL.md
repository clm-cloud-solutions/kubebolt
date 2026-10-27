---
name: investigate-pvc-pending
description: How to investigate a PersistentVolumeClaim stuck Pending.
listed: false
---
Read the PVC's events and its StorageClass. Provisioning failures reported by
a cloud CSI driver are usually outside the cluster: a disk/volume quota in the
region, a zone that does not match the node's (WaitForFirstConsumer vs. an
immediate binding), a KMS key or permission the driver's identity lacks, a
volume type or size the region does not offer. When you have cloud read tools,
check the quota and the backing resource the StorageClass names, and report the
provider, region and zone.
