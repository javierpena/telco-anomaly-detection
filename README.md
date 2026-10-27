# Telco Anomaly Detection Operator

A Kubernetes operator that monitors telco workloads across Red Hat Advanced Cluster Management (ACM) managed clusters and triggers AI-driven health investigations using [OpenShift Lightspeed](https://www.redhat.com/en/technologies/cloud-computing/openshift/lightspeed).

## Overview

The operator runs on an ACM hub. It configures Thanos alert rules for monitored clusters; AlertManager sends fired alerts to a webhook. An alert, or an enabled periodic check, creates an `AgenticRun` in `openshift-lightspeed` **on the hub**, with `spec.targetCluster` naming the affected spoke. The hub keeps a `TelcoHealthCheckRun` audit record and synchronizes the hub run's condition and analysis result.

```
Spoke metrics → observability-addon → Hub Thanos Ruler → AlertManager
                                                   │ POST /webhook
                                                   ▼
                                            Hub Alert Receiver ──┐
                                        Hub Controller (periodic) ─┴─► Hub AgenticRun (targetCluster: spoke)
                                                                     │
                                                                     ▼
                                                               OpenShift Lightspeed

Hub AgenticRun + AnalysisResult → Hub Controller watch → Hub TelcoHealthCheckRun
```

## Prerequisites

- OpenShift hub cluster with ACM installed
- ACM Multicluster Observability (MCO) operator deployed
- On the hub: compatible lightspeed-operator, lightspeed-agentic-operator (CRD with `spec.targetCluster`) and lightspeed-hub
- A `HubConfig` named `cluster` with `spec.clusterRegistryMode: secret` (the checked-out hub does not yet implement `mce`)
- An ACM `ManagedCluster` and a same-named, cluster-scoped `SpokeCluster` for each monitored spoke, with `spec.apiServer` and `spec.credentialSource.secret` referencing a kubeconfig supplied to lightspeed-hub. Check its `Connected` and `Provisioned` conditions and the resulting `spoke-kubeconfig-<name>` Secret in `openshift-lightspeed`. The telco operator does not read these credentials or connect to spokes.

## Components

| Component | Description |
|---|---|
| **Controller** | Reconciles the singleton `TelcoHealthcheck`; manages Thanos rules, AlertManager, the MCO metrics allowlist, periodic hub runs, hub-local run/result status reconciliation, and run-record cleanup |
| **Alert Receiver** | HTTP webhook server (`POST /webhook`) that receives AlertManager payloads and creates targeted AgenticRuns on the hub |
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
      enabled: false       # opt in to RDS compliance checks
      # period: 24h        # optional RDS-specific override
      # maxJitter: 10m     # optional override of just the upper bound
    lowLatencyCheck:
      enabled: false       # opt in to low-latency readiness checks
      # period: 12h        # optional low-latency-specific override
      # minJitter: 1m      # optional override of the lower bound
      # maxJitter: 10m     # optional override of the upper bound

  logLevel: info           # info or debug; changes on next reconcile
  # purgeInterval: 168h   # optional retention for hub run records
```

All periodic checks are opt-in. With a nonzero global period, each enabled check
creates a hub `AgenticRun` targeting every monitored spoke when due. `rdsCompliance.period`
and `lowLatencyCheck.period` independently override the default interval. Enabling
RDS compliance also deploys `kube-compare-mcp` in the operator namespace.

The `low-latency-check` uses `/skills/check-low-latency-configuration` from the
skills image and requires no MCP servers.

`${CLUSTER_NAME}` is expanded for each spoke. Periodic AgenticRun creation is
spread across an independent 30s–5m window per cluster by default. Each check
can override each global jitter bound independently; bounds must be non-negative
and the effective minimum must not exceed the maximum. To create runs
synchronously, set both global bounds to `0s` (unless overridden per check).
The per-check status timestamp marks when the interval was claimed, so a
controller restart during a delay may skip that interval's run on some spokes.

### Alert types

| Alert | Spec field | Trigger expression |
|---|---|---|
| `TelcoHealthCheckHostNetwork` | `alerts.hostNetwork` | Node network receive/transmit drop rate > 1 |
| `TelcoHealthCheckPodNetwork` | `alerts.podNetwork` | Container network errors or dropped packets > 1 |
| `TelcoHealthCheckHostReservedCPU` | `alerts.hostReservedCPU` | `openshift:cpu_usage_cores:sum > 3` |
| `TelcoHealthCheckOVSProcessCPU` | `alerts.ovsProcessCPU` | OVS DB or vswitchd process CPU rate > 1 core |

With `alerts.userAlerts: true`, the controller also includes labeled user-defined alert ConfigMaps in its Thanos rules and MCO metrics allowlist. See [Adding a new alert](docs/adding-a-new-alert.md) for their format. Built-in alerts and the periodic checks are configured from embedded assets in `internal/controller/assets/`; the controller restores their ConfigMap contents on each reconcile. To change a built-in prompt, rule, skill, or MCP server, edit the corresponding asset and rebuild the controller image.

Both system-managed and user-managed alert ConfigMaps can declare metrics to forward from spokes to the hub. Each field contains a JSON array of metric-name strings:

| Alert ConfigMap field | Metric source | Key in `observability-metrics-custom-allowlist` |
|---|---|---|
| `alertMetrics` | Platform monitoring | `metrics_list.yaml` |
| `alertUserWorkloadMetrics` (optional) | User-workload monitoring | `uwl_metrics_list.yaml` |

For example, `alertUserWorkloadMetrics: '["application_requests_total"]'` adds that metric to the user-workload allowlist. Alert ownership and metric source are independent: either alert type can use either or both fields. The controller deduplicates each list separately and always writes both keys in `open-cluster-management-observability`, using `names: []` for an empty list. An omitted field, empty string, or `[]` requests no metrics. Invalid metric-field data causes the whole alert definition to be logged and skipped, including its Thanos rule. See [Adding a new alert](docs/adding-a-new-alert.md) for examples and validation details.

### Run records

Each hub run has a namespaced `TelcoHealthCheckRun` (`thcr`) audit record on the hub in `telco-healthcheck-system`. Its status tracks the latest AgenticRun condition type and reason (`agenticRunStatus.type` and `.phase`), an AnalysisResult summary, and `agenticRunActionRequired`. The action-required field follows the latest referenced `AnalysisResult.status.actionRequired` while the type is `Analyzed`; any other non-empty type sets it to `"False"`. It is omitted when the type is empty, or while `Analyzed` has no analysis value. Returning to `Analyzed` restores the current AnalysisResult value.

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

For basic auth, create the Secret with `--from-file=username=/path/to/username --from-file=password=/path/to/password` instead. The operator reads credentials for each delivery, so Secret rotation takes effect without restarting it. Failed deliveries remain queued for retry. Alerts include the run's cluster, summary, and AgenticRun name. When the hub `openshift-console/console` Route is available, the URL annotation links to `https://<hub-console-host>/lightspeed/runs/openshift-lightspeed/<AgenticRun name>`; otherwise it is omitted. See [architecture](docs/architecture.md#external-action-required-alerts) for delivery details.

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

This applies the CRDs, RBAC, validating webhook, Services, and Deployments. The operator runs in `telco-healthcheck-system`; `openshift-lightspeed` and `open-cluster-management-observability` must exist before applying the namespaced RBAC. The webhook uses an OpenShift service-CA serving certificate; wait for the controller Deployment to become Ready before creating the singleton CR, because admission fails until the webhook CA bundle is available.

**Upgrading from spoke-created runs:** save the current schedule settings, scale the old alert receiver to zero, and set `periodicHealthChecks.period` to `0s` to pause new creation. Allow any in-flight creates to finish. Leave the old controller running until no `TelcoHealthCheckRun` is annotated `ran.openshift.io/spoke-creation-pending: "true"`; investigate any record that does not resolve before proceeding. Deploy both new images, restore the schedule, and scale the receiver back up. Completed old records remain historical snapshots; new records carry `ran.openshift.io/hub-agentic-run: "true"` and are the only records recovered or synchronized from hub runs. A `Created` record only means the hub API accepted the AgenticRun; use its subsequent status for the investigation outcome.

### 5. Create a TelcoHealthcheck CR

```bash
kubectl apply -f config/samples/ran_v1alpha1_telcohealthcheck.yaml
```

The sample enables host and pod network alerts and host-reserved-CPU alerts; RDS compliance, low-latency checks, and user-defined alerts are disabled. Adjust the sample for your monitored clusters before applying it.

### 6. Configure AgenticRun investigations (optional)

Each enabled built-in alert and periodic check gets a controller-managed ConfigMap in `telco-healthcheck-system`. Its `request`, `skills`, and `mcpServers` fields supply the Lightspeed prompt and tools. Change the corresponding embedded asset and rebuild/redeploy the controller to update a built-in configuration:

| ConfigMap | Alert / check |
|---|---|
| `telco-anomaly-host-network-config` | `TelcoHealthCheckHostNetwork` |
| `telco-anomaly-pod-network-config` | `TelcoHealthCheckPodNetwork` |
| `telco-anomaly-host-reserved-cpu-config` | `TelcoHealthCheckHostReservedCPU` |
| `telco-anomaly-ovs-process-cpu-config` | `TelcoHealthCheckOVSProcessCPU` |
| `telco-anomaly-rds-compliance-config` | RDS compliance periodic check |
| `telco-anomaly-low-latency-check-config` | Low-latency readiness periodic check |

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
