---
name: check-sriov-pod-network-health
description: Run a complete health check of the network for an OpenShift pod that uses SR-IOV. Use when user wants to troubleshoot or check the health of a pod running workloads that use SR-IOV.
---


# Check SR-IOV pod network health

## When to Use

- Use this skill when you need to check the configuration of an OpenShift pod using SR-IOV, to achieve high performance
- This skill is helpful to verify the configuration of the OpenShift cluster, nodes and pod to obtain high SR-IOV network performance

## Rules

- Always use the MCP tools at your disposal
- NEVER try to run any commands outside of MCP tool calls
- You will focus the analysis on pod $ARGUMENTS[0] from namespace $ARGUMENTS[1]

## Prerequisite: Gather pod and node identity

Before starting any checks, retrieve the pod definition for pod $ARGUMENTS[0] in namespace $ARGUMENTS[1] and record the following for use in all subsequent steps:
- The name of the node the pod is running on (field `spec.nodeName`). Refer to this as NODE_NAME throughout the rest of the analysis.
- The containerID of the pod's main container. Refer to this as CONTAINER_ID.
- The PerformanceProfile resource from the cluster that applies to NODE_NAME. Refer to this as PERFORMANCE_PROFILE.

## Step 1: Check OpenShift node configuration

1. Check the CPU usage of all reserved cores, as defined by PERFORMANCE_PROFILE. Refer to `references/cpu-per-core.md` for detailed instructions.
2. Make sure the topology policy defined in PERFORMANCE_PROFILE is either "single-numa-node" or "restricted".

## Step 2: Check pod configuration

1. Make sure the following annotations, including their required values, are included in the pod definition:
    - cpu-load-balancing.crio.io: disable
    - cpu-quota.crio.io: disable
    - irq-load-balancing.crio.io: disable
2. Make sure the the pod's CPU utilization is above 90%.
3. Make sure the pod's containers are not being throttled by the CFS. Use the following Prometheus query, replacing `<pod>` and `<namespace>` with the pod name and namespace: `rate(container_cpu_cfs_throttled_periods_total{pod="<pod>", namespace="<namespace>"}[5m]) / rate(container_cpu_cfs_periods_total{pod="<pod>", namespace="<namespace>"}[5m])`. A value above 0.25 (25%) indicates significant throttling.
4. Make sure the QoS class for the pod is Guaranteed.
5. Find the node CPUs assigned to the pod using CONTAINER_ID. Refer to `references/find-cpus-for-pod.md` for detailed instructions. Record the resulting CPU list as POD_CPUS for use in Steps 3 and 4.

## Step 3: Check OpenShift node network configuration

1. Find the physical NICs used by the SR-IOV VFs associated to the pod on NODE_NAME. Refer to `references/find-physical-nic-for-sriov-pod.md` for detailed instructions. Record the physical NIC names as PF_NICS and the VF interface names as VF_NICS for use in subsequent checks.
2. Determine if the pod is using kernel or DPDK for SR-IOV. Refer to `references/detect-sriov-type.md` for detailed instructions. Record the SR-IOV type as SRIOV_TYPE for use in subsequent checks.
3. Check the MTU for all physical NICs used by the SR-IOV VFs associated to the pod. They must be 1500 or higher.
4. Check the MTU for all SR-IOV VFs associated to the pod. They must be 1500 or higher.
5. Make sure there are no errors or packet drops shown for any physical NICs used by the SR-IOV VFs associated to the pod. Refer to `references/nic-errors-packet-drops.md` for detailed instructions.

## Step 4: Check OpenShift kernel node networking configuration (optional, only if SRIOV_TYPE is kernel)

Run the following steps ONLY if SRIOV_TYPE is kernel.

1. Check the combined channels for the physical NICs used by the SR-IOV VFs associated to the pod. The number of combined channels must be at least 16 for each NIC.
2. Check the statistics for all physical NICs in PF_NICS. You can use the query_ethtool tool to get that information. For each NIC, compare the tx_queue_*_packets and rx_queue_*_packets counters across all queues. If the highest queue count exceeds the lowest by more than 20%, the traffic distribution is imbalanced and should be flagged.
3. Check for any drops or errors at the TCP and UDP layers on the node running the pod. Refer to `references/tcp-udp-layers-information.md` for detailed instructions.

## Step 5: Check low-level OpenShift node configuration

1. Check which processes are running on POD_CPUS on NODE_NAME. If there is any kernel process running on those CPUs, ensure it is a per-cpu kernel thread and not any other type of process.
2. Check the IRQs allowed to run on POD_CPUS on NODE_NAME. No IRQ related to a network driver should be allowed to run on the isolated CPUs used by the pod. It is ok to have those IRQs running on the system's reserved CPUs.
3. Check the kernel settings under /proc/sys/net are correct. Refer to `references/recommended-sriov-net-kernel-settings.md` for detailed information.
4. Check for softnet packet-drop errors or high time_squeeze values, which can indicate network contention on the node running the pod. Refer to `references/softnet.md` for detailed instructions.
5. Check for a high number of SMI received by the node's CPU. Refer to `references/smi.md` for detailed instructions.

## Step 6: Final report

Report status of each of the checks, providing a summary of the next steps.
