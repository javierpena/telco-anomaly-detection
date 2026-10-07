---
name: check-sriov-pod-network-health
description: Diagnose OpenShift SR-IOV pod networking and CPU/IRQ placement, correlating each pod VF with its node PF.
---


# Check SR-IOV pod network health

## Scope and rules

- Use the namespace (and optional pod name) supplied in the request. If the namespace is missing, ask for it before querying pods. Analyze only pods in that namespace; never change cluster configuration.
- Record findings per pod **and per attached VF** as **pass**, **deviation**, or **unable to verify**. Do not treat missing telemetry as a pass.

## 1. Identify pods, containers, and VFs

Find SR-IOV pods as described in `references/find-sriov-pods.md`. For each running pod, record `spec.nodeName`, container IDs from `status.containerStatuses`, and the PCI address of each attached SR-IOV VF from the pod's network status. If the pod has no node or running container, report the blocked checks instead of guessing. Fetch the node's SriovNetworkNodeState **once** and map each VF to its PF and VF group using `references/find-physical-nic-for-sriov-pod.md`. Determine kernel versus userspace handling **per VF** using `references/detect-sriov-type.md`.

## 2. Check pod configuration

1. Make sure the following annotations, including their required values, are included in the pod definition:
    - cpu-load-balancing.crio.io: disable
    - cpu-quota.crio.io: disable
    - irq-load-balancing.crio.io: disable
2. Check CFS throttling per container over five minutes: `rate(container_cpu_cfs_throttled_periods_total{pod="<pod>",namespace="<namespace>",container="<container>"}[5m]) / rate(container_cpu_cfs_periods_total{pod="<pod>",namespace="<namespace>",container="<container>"}[5m])`. A ratio above 0.25 indicates significant throttling; if the denominator is zero or the metric is absent, mark it unable to verify. Do not add ratios across containers.
3. Make sure the QoS class for the pod is Guaranteed.
4. Find assigned CPUs for each relevant container ID using `references/find-cpus-for-pod.md`. Record CPUs per container for the placement checks below.

## 3. Check node and VF networking

1. For each mapped VF, check its PF MTU and, where exposed, VF MTU; flag values below 1500. A VF bound to `vfio-pci` may have no host netdevice name: report its MTU as unable to verify unless it can be read from the pod or device tooling.
2. Check PF drops and errors using `references/nic-errors-packet-drops.md`. Attribute counters to the correct node/PF and distinguish historical totals from current increases.

## 4. Check kernel-networked VFs only

For PFs associated with kernel-networked VFs, inspect the **current** combined-channel count and hardware maximum with `ethtool -l`; compare the current value with the workload's expected channel count (the original guideline is at least 16), and report unsupported or unavailable values separately. Compare **per-queue packet deltas** across active queues over the same interval rather than lifetime totals; avoid a 20% imbalance judgment when traffic is too low or queues are intentionally inactive. Skip these checks for userspace-bound VFs.

## 5. Check CPU and IRQ placement

1. For the assigned CPUs on the pod's node, identify competing processes/threads; distinguish a kernel thread bound to its own CPU from unrelated workload on an isolated CPU.
2. Inspect network IRQ effective affinities and match them to the assigned isolated CPUs. Flag network IRQs allowed on those CPUs; reserved CPUs may handle these IRQs.

## 6. Report

For each pod, list its node, containers/CPUs, each VF PCI address → PF → handling mode, observed values and intervals, and checks that could not be verified. Recommend fixes only for supported deviations.
