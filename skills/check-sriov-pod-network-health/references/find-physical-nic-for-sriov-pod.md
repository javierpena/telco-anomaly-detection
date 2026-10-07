# Map an attached SR-IOV VF to its physical NIC (PF)

1. For the pod's `spec.nodeName`, read `SriovNetworkNodeState/<node>` in `openshift-sriov-network-operator` once. Read `k8s.v1.cni.cncf.io/network-status` on the pod and extract the PCI address (`device-info.pci.pci-address`, when present) for **each** SR-IOV attachment. Match attachments to the SR-IOV NADs, not to every entry in network status.
2. Match each PCI address to `status.interfaces[].Vfs[].pciAddress`. The containing `status.interfaces[]` entry gives the PF `name` and the VF `vfID` and, for a kernel VF, possibly its host netdevice `name`. Map the PF name to `spec.interfaces[]` and its `vfGroups[]` using the VF ID in `vfRange`. PCI address is the identity; VF names may be absent for `vfio-pci`.
3. If network status lacks a PCI address, corroborate the VF identity using pod resource allocation and node device information; otherwise report the mapping as unable to verify. Do not assign all VFs under a PF to the pod.

Only the relevant shape is needed, for example:

```yaml
status:
  interfaces:
  - name: ens2f1             # PF
    Vfs:
    - pciAddress: "0000:51:09.1"  # attached VF, if matched to the pod
      vfID: 1
      driver: vfio-pci
```
