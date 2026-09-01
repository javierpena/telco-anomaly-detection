# Telco Anomaly Detection Operator

A Kubernetes operator that monitors telco workloads across Red Hat Advanced Cluster Management (ACM) managed clusters and autonomously triggers AI-driven health investigations using [OpenShift Lightspeed](https://www.redhat.com/en/technologies/cloud-computing/openshift/lightspeed).

## Overview

The operator runs on an ACM hub cluster and watches telco spoke clusters for anomalies. When a Thanos alert fires (or on a configurable schedule), it creates an `AgenticRun` resource on the affected spoke cluster so that OpenShift Lightspeed can investigate the issue autonomously and propose remediation steps.

```
Spoke clusters                              Hub cluster
──────────────────────────────              ─────────────────────────────────────────────
 node_exporter / kube-state                 Thanos Receive → Thanos Store
       │ metrics                                    │
       ▼                                    Thanos Ruler (evaluates custom alert rules)
 observability-addon                                │ alert fires
       │ remote_write                       AlertManager
       └──────────────────────────────►            │ POST /webhook
                                           Alert Receiver pod
                                                   │
                                           Controller pod (periodic)
                                                   │
                                           AgenticRun CR on spoke
                                                   │
                                           OpenShift Lightspeed investigates
```

## Prerequisites

- OpenShift hub cluster with ACM installed
- ACM Multicluster Observability (MCO) operator deployed
- OpenShift Lightspeed installed on each monitored spoke cluster
- ACM `ManagedCluster` resources for spoke clusters, each with an `<name>-admin-kubeconfig` Secret in namespace `<name>`

## Components

| Component | Description |
|---|---|
| **Controller** | Reconciles `TelcoHealthcheck` CRs; manages Thanos alert rules, AlertManager config, custom metrics collection, and periodic AgenticRun creation |
| **Alert Receiver** | HTTP webhook server (`POST /webhook`) that receives AlertManager payloads and creates AgenticRuns on the affected spoke cluster |
| **Skills OCI image** | OCI image containing Lightspeed skill definition files, mounted into AgenticRuns to guide the investigation |

## CRD: TelcoHealthcheck

The operator is configured via a single `TelcoHealthcheck` custom resource (`ran.openshift.io/v1alpha1`, short name `thc`).

```yaml
apiVersion: ran.openshift.io/v1alpha1
kind: TelcoHealthcheck
metadata:
  name: telco-healthcheck-sample
  namespace: telco-healthcheck-system
spec:
  # Monitor all clusters except the hub.
  # Use `include` to whitelist specific clusters instead (mutually exclusive).
  managedClusters:
    exclude:
      - local-cluster

  # Namespaces to monitor on each spoke cluster (required).
  managedNamespaces:
    - openshift-sriov-network-operator
    - openshift-ovn-kubernetes

  # Enable Thanos alert rules for each anomaly category.
  alerts:
    hostNetwork: true      # node-level network drop/error alerts
    podNetwork: true       # pod-level container network error alerts
    hostReservedCPU: false # reserved-CPU overuse alerts

  periodicHealthChecks:
    period: 6h             # run a health check on every spoke every 6 hours
    rdsCompliance:
      enabled: false       # RDS compliance check via kube-compare-mcp
```

### Alert types

| Alert | Spec field | Trigger expression |
|---|---|---|
| `TelcoHealthCheckHostNetwork` | `alerts.hostNetwork` | Node network receive/transmit drop rate > 1 |
| `TelcoHealthCheckPodNetwork` | `alerts.podNetwork` | Container network errors or dropped packets > 1 |
| `TelcoHealthCheckHostReservedCPU` | `alerts.hostReservedCPU` | `openshift:cpu_usage_cores:sum > 3` |

## Getting Started

### 1. Install tools

```bash
make install-tools   # installs controller-gen and golangci-lint
```

### 2. Build

```bash
make build           # compile controller and alert receiver binaries
```

### 3. Build and push container images

```bash
make container-build REGISTRY=quay.io/youruser
```

### 4. Deploy to the hub cluster

```bash
make deploy
```

This applies the CRD, RBAC, and Deployments. The operator runs in the `telco-healthcheck-system` namespace.

### 5. Create a TelcoHealthcheck CR

```bash
kubectl apply -f config/samples/ran_v1alpha1_telcohealthcheck.yaml
```

### 6. Customise AgenticRun prompts (optional)

Each alert type and periodic check reads its Lightspeed prompt and tool configuration from a ConfigMap in `telco-healthcheck-system`. Edit these to provide richer investigation prompts or point to your skill OCI image:

| ConfigMap | Alert / check |
|---|---|
| `telco-anomaly-host-network-config` | `TelcoHealthCheckHostNetwork` |
| `telco-anomaly-pod-network-config` | `TelcoHealthCheckPodNetwork` |
| `telco-anomaly-host-reserved-cpu-config` | `TelcoHealthCheckHostReservedCPU` |
| `telco-anomaly-rds-compliance-config` | RDS compliance periodic check |

### 7. Undeploy

```bash
make undeploy
```

## Development

```bash
make build       # compile
make test        # run all tests
make lint        # golangci-lint
make vet         # go vet
make generate    # regenerate DeepCopy methods after API changes
make manifests   # regenerate CRD YAML and RBAC after API changes
```

## Architecture

See [`docs/architecture.md`](docs/architecture.md) for the full design documentation covering data flows, namespaces, RBAC, and all Kubernetes resources managed by the operator.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
