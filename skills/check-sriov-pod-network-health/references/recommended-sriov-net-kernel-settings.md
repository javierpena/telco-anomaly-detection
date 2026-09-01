# Recommended kernel settings for SR-IOV workloads on an OpenShift node

## Instructions

-  Read the values of `/host/proc/sys/net/ipv4/tcp_rmem` and `/host/proc/sys/net/ipv4/tcp_wmem` from the node running the pod, using the `read_text_file_openshift_host` tool. The highest value from each file should be at least:
    - /host/proc/sys/net/ipv4/tcp_rmem: 4194304
    - /host/proc/sys/net/ipv4/tcp_wmem: 4194304
