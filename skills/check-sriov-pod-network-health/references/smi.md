# Checking for SMI on a CPU running an OpenShift pod

## Instructions

- Read MSR register 0x34 from CPU 0 on the node running the pod. Use the `read_msr_register` tool.
- The value indicates the number of SMI received since the register was last reset. This will not necessarily indicate a potential latency problem. It is a problem if the number increases over time.

