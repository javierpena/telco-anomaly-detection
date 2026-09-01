# How to find the CPUs assigned to an OpenShift pod

## Instructions

- Get the containerID value for the pod's main container. It will look similar to the following:

``` 
    containerID: cri-o://3cda616db88c87a1f7b95ec477362802d86c370f86edc309b1e559bc5d9097b7
```

- Use the container ID (3cda616db88c87a1f7b95ec477362802d86c370f86edc309b1e559bc5d9097b7 in the above example), and find its cgroup directory on the OpenShift node it is running on. To do so, use the `search_files_openshift_host` MCP tool, with the following parameters:
  - path: "/host/sys/fs/cgroup"
  - pattern: "**/*<container id>*/*container/cpuset.cpus.effective", replacing `<container id>` with the container ID obtained in the previous step

- Take the first entry from the search, and read that file, using the `read_text_file_openshift_host` MCP tool. The contents of the file will be the CPUs assigned to the OpenShift pod.
