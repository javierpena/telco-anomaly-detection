# Checking for softnet packet-drop errors or high time_squeeze values

## Instructions

- Read file `/host/proc/net/softnet_stat` from the node running the pod, using the `read_text_file_openshift_host` tool.
- The file contains per-CPU statistics. Column 2 shows drops, Column 3 shows time squeezes (time out of quota), encoded in hexadecimal.
- Any non-zero value in the drops column is a problem. A time_squeeze value that is non-zero and increasing over time indicates the CPU cannot keep up with network traffic and may cause latency spikes.
