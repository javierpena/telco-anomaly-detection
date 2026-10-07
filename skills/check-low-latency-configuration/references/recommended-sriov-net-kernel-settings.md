# Recommended kernel settings for SR-IOV workloads on an OpenShift node

## Instructions

- Read `/host/proc/sys/net/ipv4/tcp_rmem` and `/host/proc/sys/net/ipv4/tcp_wmem` on **each assessed node** using `oc debug node/<node>`. Each file contains three values; compare the third (maximum) value with this guideline:
    - /host/proc/sys/net/ipv4/tcp_rmem: 4194304
    - /host/proc/sys/net/ipv4/tcp_wmem: 4194304

Report the actual maximums and whether they meet the guideline; these settings alone do not establish overall low-latency readiness.
