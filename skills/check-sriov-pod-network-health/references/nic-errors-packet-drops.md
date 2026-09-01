# Finding errors or packet drops on a NIC from an OpenShift node

## Instructions

- Use the following Prometheus queries to obtain the number of network drops from a NIC. Replace `<NIC>` with the network interface name:
  - node_network_receive_drop_total{device="<NIC>"}
  - node_network_transmit_drop_total{device="<NIC>"}
- Use the following Prometheus queries to obtain the number of network errors from a NIC. Replace `<NIC>` with the network interface name:
  - node_network_receive_errs_total{device="<NIC>"}
  - node_network_transmit_errs_total{device="<NIC>"}
