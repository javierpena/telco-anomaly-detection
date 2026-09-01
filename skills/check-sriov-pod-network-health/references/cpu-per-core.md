# How to find CPU usage of certain cores in an OpenShift cluster

## Instructions

The most effective way to find the CPU usage of certain cores in an OpenShift cluster is to run a Prometheus query. The following query will provide the CPU usage for all CPUs over the last 15 minutes:

```
(sum by (cpu)(rate(node_cpu_seconds_total{mode!="idle"}[15m]))*100)
```

Now filter for the CPUs listed as reserved in the Performance Profile resource.

