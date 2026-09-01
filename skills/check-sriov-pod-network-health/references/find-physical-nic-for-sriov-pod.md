# How to find the physical NICs (PF) associated to a virtual funcion (VF) attached to an OpenShift pod

## Instructions

Look at the SriovNetworkNodeState resource from the `openshift-sriov-network-operator` namespace. There is one resource for each node in the OpenShift cluster, so you need to use the resource with the same name as the node running the pod.

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
status:
  interfaces:
  - deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: 10000 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:40
    mtu: 1500
    name: ens2f0
    pciAddress: "0000:51:00.0"
    totalvfs: 64
    vendor: "8086"
  - Vfs:
    - deviceID: "1889"
      driver: vfio-pci
      pciAddress: "0000:51:09.0"
      vendor: "8086"
      vfID: 0
    - deviceID: "1889"
      driver: vfio-pci
      pciAddress: "0000:51:09.1"
      vendor: "8086"
      vfID: 1
    - deviceID: "1889"
      driver: iavf
      mac: 02:a1:7d:e1:43:db
      mtu: 1500
      name: ens2f1v2
      pciAddress: "0000:51:09.2"
      vendor: "8086"
      vfID: 2
    deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: 10000 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:41
    mtu: 1500
    name: ens2f1
    numVfs: 3
    pciAddress: "0000:51:00.1"
    totalvfs: 64
    vendor: "8086"
  - deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: 10000 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:42
    mtu: 1500
    name: ens2f2
    pciAddress: "0000:51:00.2"
    totalvfs: 64
    vendor: "8086"
  - deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: -1 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:43
    mtu: 1500
    name: ens2f3
    pciAddress: "0000:51:00.3"
    totalvfs: 64
    vendor: "8086"
  - deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: 25000 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:c8
    mtu: 1500
    name: ens4f0
    pciAddress: 0000:8a:00.0
    totalvfs: 64
    vendor: "8086"
  - deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: 25000 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:c9
    mtu: 1500
    name: ens4f1
    pciAddress: 0000:8a:00.1
    totalvfs: 64
    vendor: "8086"
  - deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: 25000 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:ca
    mtu: 1500
    name: ens4f2
    pciAddress: 0000:8a:00.2
    totalvfs: 64
    vendor: "8086"
  - deviceID: "1593"
    driver: ice
    eSwitchMode: legacy
    linkSpeed: 25000 Mb/s
    linkType: ETH
    mac: 50:7c:6f:1f:b2:cb
    mtu: 1500
    name: ens4f3
    pciAddress: 0000:8a:00.3
    totalvfs: 64
    vendor: "8086"
  syncStatus: Succeeded

```

We can see that, for example, NIC ens2f1 has 3 VFs, with PCI IDs 0000:51:09.0, 0000:51:09.1 and 0000:51:09.2. Match those PCI IDs with the information displayed by the k8s.v1.cni.cncf.io/network-status annotation of the pod.
