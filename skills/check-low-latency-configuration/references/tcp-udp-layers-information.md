# Finding errors or packet drops at the TCP or UDP layers on an OpenShift node

## Instructions

- Use the following Prometheus metrics to obtain the TCP errors from the node:
    - node_netstat_Tcp_InErrs
    - node_netstat_TcpExt_ListenDrops
    - node_netstat_TcpExt_TCPTimeouts
- Use the following Prometheus metrics to obtain the UDP errors from the node:
    - node_netstat_Udp_InErrors
    - node_netstat_Udp6_InErrors
    - node_netstat_Udp_RcvbufErrors
    - node_netstat_Udp_SndbufErrors
