# Find CPUs assigned to an OpenShift container

- For each relevant container, get its `containerID` from `status.containerStatuses` and strip the runtime prefix (such as `cri-o://`).
- On that container's node, locate its **exact** cgroup using the ID; inspect its `cpuset.cpus.effective` (cgroup v2) or effective cpuset in the node's cgroup layout. Verify the cgroup belongs to the intended pod UID/container rather than taking the first partial-ID match.
- Record the resulting CPUs per container. If the cgroup cannot be located, report CPU placement as unable to verify; a cpuset describes permitted CPUs, not proof that every CPU is exclusively assigned.
