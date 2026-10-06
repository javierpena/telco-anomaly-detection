# How to find SR-IOV pods on a given namespace

## Instructions

For each pod in the namespace:

- Check the `k8s.v1.cni.cncf.io/networks` annotation. If the annotation does not exist, the pod does not use any SR-IOV networks.
- Find the name and namespace for each additional network. Then, check the NetworkAttachmentDefinition referenced by that name and namespace. A NetworkAttachmentDefinition resource will look like the following:

```
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  annotations:
    k8s.v1.cni.cncf.io/resourceName: openshift.io/dpdk_nic_1
  creationTimestamp: "2026-03-12T16:03:45Z"
  generation: 1
  name: dpdk-network-1
  namespace: default
  resourceVersion: "38911451"
  uid: 39f3ae67-bc14-4b7a-baf2-0bd648103ef1
spec:
  config: |-
    {
        "cniVersion": "0.3.1",
        "name": "dpdk-network-1",
        "type": "sriov",
        "vlan": 0,
        "spoofchk": "off",
        "trust": "on",
        "vlanQoS": 0,
        "logLevel": "info",
        "ipam": {
            "type": "host-local",
            "ranges": [
                [
                    {
                        "subnet": "10.0.1.0/24"
                    }
                ]
            ],
            "dataDir": "/run/my-orchestrator/container-ipam-state-1"
        }
    }
```

In this example, the `spec.config` field contains "type": "sriov", which indicates that this network is using SR-IOV, which means the pod uses SR-IOV. If the type is different, the pod is NOT using SR-IOV.
