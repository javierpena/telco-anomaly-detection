# Checking for SMI on a CPU running an OpenShift pod

## Instructions

- On each assessed node, if the CPU exposes the Intel SMI count MSR `0x34` and MSR access is available, read it on CPUs relevant to the low-latency workload; CPU 0 alone does not represent all CPUs. Take two readings over a known interval on the **same CPU**.
- An increasing count may indicate firmware interruptions; report the delta and interval, not a blanket failure based on a lifetime count. If the register is unsupported or inaccessible, report unable to verify. Reading the MSR may require privileges; do not change host settings.
