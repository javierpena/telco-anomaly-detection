---
name: check-reserved-cpu-usage
description: Diagnose high usage of reserved CPUs on OpenShift nodes using node-scoped metrics and workload and interrupt evidence.
---


# Check OpenShift reserved CPU usage

## Scope and rules

- Investigate the node named in the alert or request; if none is given, identify the affected nodes before expanding to the cluster. Use read-only `oc`/`kubectl` and metrics queries.
- Report each finding as **pass**, **deviation**, or **unable to verify**; do not infer that a past alert was transient from one current sample.

## 1. Identify reserved CPUs

List PerformanceProfiles once. Match each `spec.nodeSelector` to node labels and record `spec.cpu.reserved` per matching node. Do not combine CPU IDs from different nodes. If no profile applies, inspect the node's effective CPU reservation (for example its kubelet configuration); `systemd.cpu_affinity` alone does not establish the reserved CPU set. If the reservation cannot be verified, report that and avoid claims about reserved-CPU utilization.

## 2. Measure usage on the affected node

Use a five-minute per-CPU query that retains node identity (adjust the `instance` selector to the metric labels in this environment):

```promql
100 * (1 - rate(node_cpu_seconds_total{mode="idle",instance="<node-instance>"}[5m]))
```

Match `<node-instance>` to the affected node; do not assume it equals the Kubernetes node name. Check **each reserved CPU** against 90%, recording the CPU ID, value, node, and observation time. If none currently exceeds 90%, report current usage and, if available, compare with the alert's firing interval before describing it as resolved or transient. Continue with cause checks when an alert is still firing or other evidence warrants it.

## 3. Investigate contributors on affected nodes

- Look for pods with `target.workload.openshift.io/management` and compare their five-minute CPU usage on the same node. The annotation identifies candidates, not proof of CPU placement; confirm their assigned CPUs when attributing usage. Include host processes if pod usage does not explain the load.
- Use `oc debug node/<node>` to read `/host/proc/interrupts` twice over a known interval. Compare **deltas** for reserved-CPU columns and identify the IRQ/device; a lifetime total alone does not demonstrate high current activity.

## 4. Report

For each node, give the reserved CPU source, per-CPU utilization, likely contributors and interrupt evidence, with measured values and intervals. Mark missing metrics, permissions, or reservation data **unable to verify** and suggest remediation only for supported deviations.
