# Finding errors or packet drops at the TCP or UDP layers on an OpenShift node

## Instructions

- Filter the following node-exporter counters to the assessed node (`instance` mapped to that node). Use `increase(metric{instance="<node-instance>"}[5m])` or compare two samples over a known interval. Zero increase is not the same as an absent metric. TCP counters:
    - node_netstat_Tcp_InErrs
    - node_netstat_TcpExt_ListenDrops
    - node_netstat_TcpExt_TCPTimeouts
- UDP counters:
    - node_netstat_Udp_InErrors
    - node_netstat_Udp6_InErrors
    - node_netstat_Udp_RcvbufErrors
    - node_netstat_Udp_SndbufErrors

Report node, metric, delta and interval. If metrics are missing, mark the corresponding checks unable to verify instead of reporting zero errors.
