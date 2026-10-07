# Find NIC errors and drops on an OpenShift node

Use the following Prometheus counters for the PF on the pod's node. Filter by both `device="<PF>"` and the node's `instance` label; a device name alone is not unique across nodes. Observe increases over an interval using `increase(metric{...}[5m])`, or compare two samples. Raw values are lifetime counters, not current error rates:

- Drops: `node_network_receive_drop_total`, `node_network_transmit_drop_total`
- Errors: `node_network_receive_errs_total`, `node_network_transmit_errs_total`

Record the node, PF, interval and increase. If a VF's own statistics are needed, PF counters alone cannot attribute the error to that VF.
