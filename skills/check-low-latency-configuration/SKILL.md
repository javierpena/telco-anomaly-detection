---
name: check-low-latency-configuration
description: Assess OpenShift PerformanceProfile nodes for low-latency readiness using per-node CPU, kernel and network evidence.
---

# Check low latency configuration

## Scope and rules

Read-only assessment. List PerformanceProfiles and nodes once; match each profile's `spec.nodeSelector` to node labels, and assess **each matching node** against its profile. Report nodes with no matching profile separately rather than assigning them a profile. Mark each check **pass**, **deviation**, or **unable to verify**.

## 1. Check the PerformanceProfile

- Check reserved CPU usage by node and CPU using `references/cpu-per-core.md`.
- Check the topology policy in the profile (`spec.numa.topologyPolicy`): expected values are `single-numa-node` or `restricted`. State the observed value if different or absent.

## 2. Collect node-level evidence

On each matching node, gather the following in one node-scoped investigation; reuse the node identity and observation interval across checks:

- Network kernel settings: `references/recommended-sriov-net-kernel-settings.md`.
- Softnet drops and time_squeeze: `references/softnet.md`.
- SMI activity (when MSR access is available): `references/smi.md`.
- TCP/UDP error and drop counters: `references/tcp-udp-layers-information.md`.

Counters are cumulative: check increases over a known interval, not merely nonzero totals. If a metric or privileged node read is unavailable, mark that check unable to verify rather than substituting data from a different node.

## 3. Report

For each profile and node, summarize observed values, intervals, deviations, and evidence-based potential fixes. Identify checks that could not be verified; do not claim whole-cluster compliance from a subset of nodes.
