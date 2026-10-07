# How to find CPU usage of certain cores in an OpenShift cluster

## Instructions

Use the reserved CPU list from the PerformanceProfile matched to the node. Query CPU usage over 15 minutes while preserving the node's `instance` label (map it to the Kubernetes node; it may not equal the node name):

```
100 * (1 - rate(node_cpu_seconds_total{mode="idle",instance="<node-instance>"}[15m]))
```

Inspect only that node's reserved CPU IDs. Never aggregate CPU ID 0 (or any other ID) across nodes. Record the value and interval; without a stated threshold or baseline, report utilization rather than labeling it high solely from a single sample.
