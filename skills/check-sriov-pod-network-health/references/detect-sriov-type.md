# How to find if a device is using kernel or DPDK for SR-IOV

## Instructions

Look at the SriovNetworkNodeState resource from the `openshift-sriov-network-operator` namespace. There is one resource for each node in the OpenShift cluster, so you need to use the resource with the same name as the node running the pod.

Find the interface in use for the SR-IOV virtual function, and check the following fields under spec.interfaces.vfGroups:

- deviceType
- isRdma

The following combinations are possible:

- If deviceType is set to `netdevice` and isRdma is set to `true`, the device is using DPDK.
- If deviceType is set to `vfio-pci` and isRdma is set to `false` or absent, the device is using DPDK.
- In any other case, the device is using the kernel for SR-IOV.

If we take the following example:

```
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovNetworkNodeState
metadata:
  annotations:
    sriovnetwork.openshift.io/current-state: Idle
    sriovnetwork.openshift.io/desired-state: Idle
  creationTimestamp: "2025-10-07T14:08:14Z"
  generation: 2
  name: sno3.r207-sno3.r207.lab.eng.cert.redhat.com
  namespace: openshift-sriov-network-operator
  ownerReferences:
  - apiVersion: sriovnetwork.openshift.io/v1
    blockOwnerDeletion: true
    controller: true
    kind: SriovNetworkNodePolicy
    name: default
    uid: 9517d1ad-e44a-4e35-996d-090ea4d20c0d
  resourceVersion: "38911435"
  uid: 5eb6a304-883e-4267-add4-9e03083c7608
spec:
  interfaces:
  - linkType: eth
    name: ens2f1
    numVfs: 3
    pciAddress: "0000:51:00.1"
    vfGroups:
    - deviceType: vfio-pci
      policyName: dpdk-nic-1
      resourceName: dpdk_nic_1
      vfRange: 0-1
```

The SR-IOV virtual functions associated to NIC ens2f1 are using DPDK, because deviceType is `vfio-pci` and isRdma is not defined.
