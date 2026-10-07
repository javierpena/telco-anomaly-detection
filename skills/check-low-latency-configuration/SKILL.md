---
name: check-low-latency-configuration
description: Run a complete health check of an OpenShift cluster to verify that its configuration allows low latency workloads to run. Use when user wants to check if the cluster configuration complies with the requirements to run low latency workloads.
---

# Check low latency configuration

## When to use

- Use this skill when you want to check if the OpenShift cluster configuration complies with the requirements to run low latency workloads.

## Rules

- NEVER try to do any change of the current configuration

## Prerequisite: find performance profile resources

The analysis will focus on the nodes matching a performance profile `nodeSelector`.

- Retrieve the PerformanceProfile resource(s) from the cluster.
- For each performance profile, find which nodes it applies to by checking which cluster nodes match its `spec.nodeSelector` field.

## Step 1: Check OpenShift performance profile configuration

1. For each cluster node matching a performance profile, check the CPU usage of its reserved cores. Refer to `references/cpu-per-core.md` for detailed instructions.
2. Make sure the topology policy defined in the performance profile is either "single-numa-node" or "restricted".

## Step 2: Check low-level OpenShift node configuration

1. Check the kernel settings under /proc/sys/net are correct. Refer to `references/recommended-sriov-net-kernel-settings.md` for detailed information.
2. Check for softnet packet-drop errors or high time_squeeze values, which can indicate network contention on the node running the pod. Refer to `references/softnet.md` for detailed instructions.
3. Check for a high number of SMI received by the node's CPU. Refer to `references/smi.md` for detailed instructions.
4. Check for any drops or errors at the TCP and UDP layers on the cluster nodes. Refer to `references/tcp-udp-layers-information.md` for detailed instructions.

## Step 3: Final report

Report findings for each cluster node, together with potential fixes.
