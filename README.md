# Telco Anomaly Detection Operator

A Kubernetes operator that monitors telco workloads across Red Hat Advanced Cluster Management (ACM) managed clusters and triggers AI-driven health investigations using [OpenShift Lightspeed](https://www.redhat.com/en/technologies/cloud-computing/openshift/lightspeed).

## Overview

The operator runs on an ACM hub. It configures Thanos alert rules for monitored clusters; AlertManager sends fired alerts to a webhook. An alert, or an enabled periodic check, creates an `AgenticRun` in `openshift-lightspeed` on the affected spoke. The hub keeps a `TelcoHealthCheckRun` audit record and synchronizes the spoke run's condition and analysis result.

```
Spoke metrics → observability-addon → Hub Thanos Ruler → AlertManager
                                                   │ POST /webhook
                                                   ▼
                                            Hub Alert Receiver ──┐
                                            Hub Controller (RDS) ─┴─► Spoke AgenticRun
                                                                     │
                                                                     ▼
                                                               OpenShift Lightspeed

Spoke AgenticRun + AnalysisResult → Hub Controller watch → Hub TelcoHealthCheckRun
```

## Prerequisites

- OpenShift hub cluster with ACM installed
- ACM Multicluster Observability (MCO) operator deployed
- OpenShift Lightspeed installed on each monitored spoke cluster
- ACM `ManagedCluster` resources for spoke clusters, each with an `<name>-admin-kubeconfig` Secret in namespace `<name>`
- Spoke kubeconfigs with access to create and read `AgenticRun` resources and list/watch `AgenticRun` and `AnalysisResult` resources in `openshift-lightspeed`

## Components

| Component | Description |
|---|---|
| **Controller** | Reconciles the singleton `TelcoHealthcheck`; manages Thanos rules, AlertManager, the MCO metrics allowlist, periodic RDS checks, spoke status watches, and run-record cleanup |
| **Alert Receiver** | HTTP webhook server (`POST /webhook`) that receives AlertManager payloads and creates AgenticRuns on the affected spoke cluster |
| **Skills OCI image** | OCI image containing Lightspeed skills referenced by the `skills` configuration of individual AgenticRuns |

## CRD: TelcoHealthcheck

The operator is configured via a single `TelcoHealthcheck` custom resource (`ran.openshift.io/v1alpha1`, short name `thc`).

```yaml
apiVersion: ran.openshift.io/v1alpha1
kind: TelcoHealthcheck
metadata:
  name: telco-healthcheck  # cluster-scoped singleton; must use this exact name
spec:
  # Monitor all clusters except the hub.
  # Use `include` to whitelist specific clusters instead (mutually exclusive).
  managedClusters:
    exclude:
      - local-cluster

  # Required field; an empty list is allowed.
  managedNamespaces:
    - openshift-sriov-network-operator
    - openshift-ovn-kubernetes

  # Enable Thanos alert rules for each anomaly category.
  alerts:
    hostNetwork: true      # node-level network drop/error alerts
    podNetwork: true       # pod-level container network error alerts
    hostReservedCPU: false # reserved-CPU overuse alerts
    ovsProcessCPU: false   # OVS process CPU alerts
    userAlerts: false      # include labeled user-defined alert ConfigMaps

  # Optional: send action-required run alerts to an external Alertmanager.
  # alertManager:
  #   url: https://alertmanager.example.com
  #   authType: bearer     # none (default), bearer, or basic
  #   credentialsSecret:
  #     name: alertmanager-credentials

  periodicHealthChecks:
    period: 6h             # default interval; 0 disables periodic checks
    # minJitter: 30s      # default minimum per-cluster creation delay
    # maxJitter: 5m       # default maximum; set both bounds to 0s to disable jitter
    rdsCompliance:
      enabled: false       # enable the currently implemented periodic check
      # period: 24h        # optional RDS-specific override
      # maxJitter: 10m     # optional override of just the upper bound

  logLevel: info           # info or debug; changes on next reconcile
  # purgeInterval: 168h   # optional retention for hub run records
```

Only RDS compliance currently uses the periodic schedule. Enabling it deploys
`kube-compare-mcp` in the operator namespace; with a nonzero default period, it
also creates a run on each monitored spoke when due. The RDS check can override
the default interval with `rdsCompliance.period`. Periodic AgenticRun creation
is spread across an independent 30s–5m window per cluster by default. Either
RDS jitter bound can override its global bound independently; bounds must be
non-negative and the effective minimum must not exceed the maximum. To create
runs synchronously, set both global bounds to `0s` (unless overridden for RDS).
The status timestamp marks when the interval was claimed, so a controller
restart during a delay may skip that interval's run on some spokes.

### Alert types

| Alert | Spec field | Trigger expression |
|---|---|---|
| `TelcoHealthCheckHostNetwork` | `alerts.hostNetwork` | Node network receive/transmit drop rate > 1 |
| `TelcoHealthCheckPodNetwork` | `alerts.podNetwork` | Container network errors or dropped packets > 1 |
| `TelcoHealthCheckHostReservedCPU` | `alerts.hostReservedCPU` | `openshift:cpu_usage_cores:sum > 3` |
| `TelcoHealthCheckOVSProcessCPU` | `alerts.ovsProcessCPU` | OVS DB or vswitchd process CPU rate > 1 core |

With `alerts.userAlerts: true`, the controller also includes labeled user-defined alert ConfigMaps in its Thanos rules and MCO metrics allowlist. See [Adding a new alert](docs/adding-a-new-alert.md) for their format. Built-in alerts and the periodic checks are configured from embedded assets in `internal/controller/assets/`; the controller restores their ConfigMap contents on each reconcile. To change a built-in prompt, rule, skill, or MCP server, edit the corresponding asset and rebuild the controller image.

### Run records

Each spoke run has a namespaced `TelcoHealthCheckRun` (`thcr`) audit record on the hub in `telco-healthcheck-system`. Its status tracks the latest AgenticRun condition type and reason (`agenticRunStatus.type` and `.phase`), an AnalysisResult summary, and `agenticRunActionRequired`. The action-required field follows `AnalysisResult.status.actionRequired` while the type is `Analyzed`; any other non-empty type sets it to `"False"`. It is omitted when the type is empty, or while `Analyzed` has no analysis value. Returning to `Analyzed` restores the current AnalysisResult value.

```bash
oc get thcr -n telco-healthcheck-system
```

Set `spec.purgeInterval` to remove old records with an hourly CronJob; without it, records are retained until the singleton is deleted. The controller also exposes run and monitored-cluster gauges through the `telco-anomaly-controller-metrics` Service on port 8080 (`GET /metrics`).

### External Alertmanager (optional)

Set `spec.alertManager.url` to the external Alertmanager **base URL** to send `TelcoActionRequired` alerts when a run's `agenticRunActionRequired` becomes `"True"`. The operator sends a resolution when it changes from `"True"` to `"False"` or becomes unset, and renews alerts while action remains required. If `alertManager` is omitted, no run alerts are sent. This destination is separate from the ACM AlertManager that sends incoming alerts to the operator.

`authType` defaults to `none`. For `bearer` or `basic`, set `credentialsSecret.name` to a Secret in `telco-healthcheck-system`; authenticated URLs must use HTTPS. Bearer auth reads the Secret's `token` key; basic auth reads its `username` and `password` keys. For example, create a bearer-token Secret before setting the field:

```bash
kubectl -n telco-healthcheck-system create secret generic alertmanager-credentials --from-file=token=/path/to/token
```

For basic auth, create the Secret with `--from-file=username=/path/to/username --from-file=password=/path/to/password` instead. The operator reads credentials for each delivery, so Secret rotation takes effect without restarting it. Failed deliveries remain queued for retry. Alerts include the run's cluster, summary, and AgenticRun name; when available, their URL links to the spoke console using the `ManagedCluster` console URL claim. See [architecture](docs/architecture.md#external-action-required-alerts) for delivery details.

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
make container-push REGISTRY=quay.io/youruser
```

`REGISTRY` selects the image tags for these commands; the deployment manifests and embedded skill references use `quay.io/javierpena` by default. Update the controller and alert receiver image references in `config/manager/` and any skill image references in `internal/controller/assets/` when using another registry.

### 4. Deploy to the hub cluster

```bash
make deploy
```

This applies the CRDs, RBAC, validating webhook, Services, and Deployments. The operator runs in `telco-healthcheck-system`. The webhook uses an OpenShift service-CA serving certificate; wait for the controller Deployment to become Ready before creating the singleton CR, because admission fails until the webhook CA bundle is available.

### 5. Create a TelcoHealthcheck CR

```bash
kubectl apply -f config/samples/ran_v1alpha1_telcohealthcheck.yaml
```

The sample enables host and pod network alerts and host-reserved-CPU alerts; RDS compliance and user-defined alerts are disabled. Adjust the sample for your monitored clusters before applying it.

### 6. Configure AgenticRun investigations (optional)

Each enabled built-in alert and periodic check gets a controller-managed ConfigMap in `telco-healthcheck-system`. Its `request`, `skills`, and `mcpServers` fields supply the Lightspeed prompt and tools. Change the corresponding embedded asset and rebuild/redeploy the controller to update a built-in configuration:

| ConfigMap | Alert / check |
|---|---|
| `telco-anomaly-host-network-config` | `TelcoHealthCheckHostNetwork` |
| `telco-anomaly-pod-network-config` | `TelcoHealthCheckPodNetwork` |
| `telco-anomaly-host-reserved-cpu-config` | `TelcoHealthCheckHostReservedCPU` |
| `telco-anomaly-ovs-process-cpu-config` | `TelcoHealthCheckOVSProcessCPU` |
| `telco-anomaly-rds-compliance-config` | RDS compliance periodic check |

The pod-network built-in currently uses a placeholder `request: "test"`. User-defined alerts can supply their own prompt and tools in their labeled ConfigMap without rebuilding the operator; see [Adding a new alert](docs/adding-a-new-alert.md).

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
