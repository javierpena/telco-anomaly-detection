---
name: check-reserved-cpu-usage
description: Run a complete health check of the reserved CPUs on an OpenShift node. Use when user wants to troubleshoot potential high reserved CPU usage on an OpenShift cluster or node.
---


# Check OpenShift reserved CPU usage

## When to Use

- Use this skill when you need to check the CPU usage on the reserved CPUs of an pod node
- This skill is helpful to identify reserved CPUs, check their usage, and verify any cause for high CPU usage on those CPUs

## Rules

- Use standard Kubernetes `kubectl` or OpenShift `oc` client commands whenever possible

## Prerequisite: Identify reserved CPUs for each cluster node

Before starting any checks, identify the reserved CPUs for each cluster node. Follow these steps:

1. List the PerformanceProfile resources in the cluster. For each PerformanceProfile, record the `spec.nodeSelector` and `spec.cpu.reserved` values as NODE_SELECTOR and RESERVED_CPUS respectively.
2. For each cluster node, check if they match the NODE_SELECTOR value. If so, the associated RESERVED_CPUS value applies to it the node.

If the PerformanceProfile resource is not available, check the node's boot command line parameters, and find the reserved CPU list from `systemd.cpu_affinity`. Record it as RESERVED_CPUS.

## Step 1: Verify CPU usage for reserved CPUs

For each reserved CPU on a cluster node, check if its usage over the last 5 minutes is above 90%. The following query will provide the CPU usage for all CPUs over the last 5 minutes:

```
(sum by (cpu)(rate(node_cpu_seconds_total{mode!="idle"}[5m]))*100)
```

Now filter for the CPUs listed as reserved in the Performance Profile resource. If the CPU usage is below 90%, discard the alert as a transient issue, notify the user and stop the verification.

## Step 2: Check for pods with high CPU usage, running on reserved CPUs

Find all pods using a `target.workload.openshift.io/management` annotation. These pods are running on the reserved CPUs. For those pods, check their CPU usage over the last 5 minutes, and identify any pod with a high CPU usage.

## Step 3: Check for high interrupt rates on reserved CPUs

Use `oc debug` to get inside the cluster node, and check the interrupt rates on the reserved CPUs by reading `/proc/interrupts`. Identify any reserved CPU with a high interrupt usage.

## Step 4: Final report

Report status on each of the checks for the cluster nodes.
