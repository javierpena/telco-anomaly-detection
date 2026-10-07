# Checking for softnet packet-drop errors or high time_squeeze values

## Instructions

- Read `/host/proc/net/softnet_stat` twice on the **same assessed node** using `oc debug node/<node>`; note the sampling interval.
- Rows are per CPU. Hexadecimal columns 2 and 3 are cumulative drops and time_squeeze counts. Convert to numbers and compare deltas for the same CPU; a nonzero historical total is not evidence of a current issue.
- Flag increasing drops or sustained time_squeeze increases on the measured CPUs, recording CPU, delta and interval. If only one snapshot is available, report the totals without concluding current contention.
