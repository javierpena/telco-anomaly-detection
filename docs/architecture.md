# Telco Anomaly Detection Operator — Architecture

> **Maintenance note:** Keep this document in sync with code changes. See `CLAUDE.md` for the reminder.

## Overview

This operator runs on an OpenShift hub cluster managed by Red Hat Advanced Cluster Management (ACM). It monitors telco workloads across multiple ACM spoke clusters by:

1. Configuring Thanos alert rules that fire when network conditions degrade.
2. Forwarding those alerts to an alert receiver webhook.
3. On each alert (or on a periodic schedule), creating an `AgenticRun` resource on the affected spoke cluster so that OpenShift Lightspeed can investigate the anomaly autonomously.

---

## Components

```
Spoke clusters
┌─────────────────────────────────────────────────┐
│  observability-addon (open-cluster-management-  │
│  addon-observability namespace)                 │
│  ┌─────────────────────────────────────────┐   │
│  │ metrics-collector                       │   │
│  │  scrapes node_exporter, kube-state,     │   │
│  │  custom recording rules                 │   │
│  └──────────────┬──────────────────────────┘   │
│                 │ remote_write                  │
│  openshift-lightspeed namespace                │
│  ┌─────────────────────────────────────────┐   │
│  │ AgenticRun CR ← OpenShift Lightspeed    │   │
│  └─────────────────────────────────────────┘   │
└─────────────────┬───────────────────────────────┘
                  │ metrics (HTTPS)
                  ▼
Hub cluster
┌────────────────────────────────────────────────────────────┐
│  open-cluster-management-observability namespace           │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ Thanos Receive  ← spoke metrics-collectors           │  │
│  │ Thanos Store    ─ long-term metric storage           │  │
│  │ Thanos Ruler    ─ evaluates thanos-ruler-custom-rules│  │
│  │                   ConfigMap; fires alerts            │  │
│  │ AlertManager    ─ routes alerts; reads               │  │
│  │                   alertmanager-config Secret         │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                            │
│  telco-healthcheck-system namespace                        │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ Controller pod                                       │  │
│  │  - writes thanos-ruler-custom-rules ConfigMap        │  │
│  │  - writes alertmanager-config Secret                 │  │
│  │  - creates AgenticRuns on spokes (periodic)          │  │
│  │  - manages kube-compare-mcp lifecycle                │  │
│  │                                                      │  │
│  │ Alert Receiver pod                                   │  │
│  │  - POST /webhook ← AlertManager                      │  │
│  │  - creates AgenticRuns on spokes (alert-driven)      │  │
│  │                                                      │  │
│  │ kube-compare-mcp pod (when rdsCompliance.enabled)    │  │
│  │  - MCP server exposing kube-compare tooling          │  │
│  │  - used by AgenticRuns for RDS compliance checks     │  │
│  │                                                      │  │
│  │ ConfigMaps (AgenticRun config)                       │  │
│  │  telco-anomaly-host-network-config                   │  │
│  │  telco-anomaly-pod-network-config                    │  │
│  │  telco-anomaly-rds-compliance-config                 │  │
│  └──────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────┘
```

---

## ACM Multicluster Observability Integration

The operator is built on top of the ACM **Multicluster Observability (MCO)** stack. MCO provides the metric collection, alerting, and routing infrastructure the operator relies on.

### MCO architecture

MCO installs the following components:

| Component | Location | Role |
|---|---|---|
| `observability-addon` | Each spoke cluster (`open-cluster-management-addon-observability` namespace) | Deploys a `metrics-collector` that scrapes Prometheus metrics and remote-writes them to the hub |
| Thanos Receive | Hub (`open-cluster-management-observability`) | Ingestion endpoint that accepts remote_write streams from spoke collectors |
| Thanos Store | Hub | Long-term metric storage backend |
| Thanos Ruler | Hub | Periodically evaluates Prometheus/Thanos recording and alerting rules; fires alerts to AlertManager |
| Thanos Querier | Hub | Federation endpoint used by Grafana and other consumers to query cross-cluster metrics |
| AlertManager | Hub (`open-cluster-management-observability`) | Receives fired alerts from Thanos Ruler and routes them to configured receivers |

### Cluster identification label

Every metric forwarded from a spoke arrives at Thanos Receive with extra labels injected by MCO. The most important for this operator is `cluster`, which is set to the ACM `ManagedCluster` name. Because this label propagates through to fired alerts, `labels.cluster` in an AlertManager payload always identifies the originating spoke cluster — no additional correlation is needed.

### How the operator interacts with MCO

**Thanos Ruler — alert rule configuration**

The controller writes the ConfigMap `thanos-ruler-custom-rules` (key `custom_rules.yaml`) in `open-cluster-management-observability`. Thanos Ruler watches this ConfigMap via a config-reload sidecar and reloads its rule set whenever the content changes. This is the standard ACM mechanism for injecting custom alerting rules into the observability stack.

The alert rules reference Prometheus recording rules that are pre-computed by MCO's default rule set on the spokes (e.g. `instance:node_network_receive_drop_excluding_lo:rate1m`). Because these aggregations already exist, our alert expressions stay concise.

**AlertManager — webhook receiver registration**

The controller reads the Secret `alertmanager-config` (key `alertmanager.yaml`) in `open-cluster-management-observability` and upserts a webhook receiver named `telco-anomaly-webhook` pointing to the alert receiver service. A `continue: true` route is also added so that existing routing — paging, silence, inhibition — is not disrupted.

On `TelcoHealthcheck` deletion the receiver entry and its route are removed, restoring AlertManager to its pre-operator state.

**Alert payload format**

AlertManager delivers alerts in the [AlertManager webhook format](https://prometheus.io/docs/alerting/latest/configuration/#webhook_config). Relevant fields used by the operator:

```json
{
  "alerts": [
    {
      "status": "firing",
      "labels": {
        "alertname": "TelcoHealthCheckHostNetwork",
        "cluster":   "my-spoke-cluster",
        "severity":  "warning"
      },
      "annotations": {
        "cluster": "{{ $labels.cluster }}",
        "node":    "{{ $labels.instance }}"
      }
    }
  ]
}
```

The receiver validates `labels.alertname` against the Thanos rules it wrote itself (by re-reading `thanos-ruler-custom-rules`) and `labels.cluster` against the `status.monitoredClusters` field across all `TelcoHealthcheck` CRs. Both checks must pass before an `AgenticRun` is created.

### Dependency summary

| Operator action | MCO prerequisite |
|---|---|
| Write `thanos-ruler-custom-rules` | MCO Multicluster Observability CR installed; Thanos Ruler running |
| Write `alertmanager-config` | MCO Multicluster Observability CR installed; AlertManager running |
| Receive alerts at `/webhook` | AlertManager routing configured (done by controller on reconcile) |
| Read `instance:node_network_*` metrics | MCO observability-addon deployed on spoke clusters with default recording rules enabled |

---

## CRD: TelcoHealthcheck

**Group / Version / Kind:** `ran.openshift.io/v1alpha1 / TelcoHealthcheck`  
**Scope:** Namespaced  
**Short name:** `thc`

### Spec fields

| Field | Type | Description |
|---|---|---|
| `managedClusters.include` | `[]string` | If non-empty, only these ManagedCluster names are monitored. |
| `managedClusters.exclude` | `[]string` | All ManagedClusters are monitored except these. Mutually exclusive with `include`. |
| `managedNamespaces` | `[]string` | **Required.** Namespaces on each spoke to monitor. An empty list (`[]`) is valid and means no namespace monitoring is active. |
| `alerts.hostNetwork` | `bool` | Enable the `TelcoHealthCheckHostNetwork` Thanos rule. |
| `alerts.podNetwork` | `bool` | Enable the `TelcoHealthCheckPodNetwork` Thanos rule. |
| `alerts.hostReservedCPU` | `bool` | Enable the `TelcoHealthCheckHostReservedCPU` Thanos rule. |
| `alerts.ovsProcessCPU` | `bool` | Enable the `TelcoHealthCheckOVSProcessCPU` Thanos rule. |
| `periodicHealthChecks.period` | `duration` | Default interval between periodic checks. Zero disables all periodic checks. |
| `periodicHealthChecks.rdsCompliance.enabled` | `bool` | Activate the RDS compliance periodic check. |
| `periodicHealthChecks.rdsCompliance.period` | `duration` | Override interval for the RDS compliance check. |
| `logLevel` | `info\|debug` | Controls log verbosity in both pods. Changes take effect on next reconcile without restart. |

### Status fields

| Field | Description |
|---|---|
| `monitoredClusters` | Resolved list of cluster names currently being monitored. |
| `lastPeriodicRunTime` | Timestamp of the last generic periodic check (reserved; not yet written). |
| `lastRDSComplianceRunTime` | Timestamp of the last RDS compliance check. |
| `conditions` | Standard `metav1.Condition` array for reconciliation state. |

### Sample CR

```yaml
apiVersion: ran.openshift.io/v1alpha1
kind: TelcoHealthcheck
metadata:
  name: cluster-monitor
  namespace: telco-healthcheck-system
spec:
  managedClusters:
    exclude:
      - local-cluster
  managedNamespaces:
    - openshift-sriov-network-operator
    - openshift-ovn-kubernetes
  alerts:
    hostNetwork: true
    podNetwork: true
    hostReservedCPU: false
  periodicHealthChecks:
    period: 6h
    rdsCompliance:
      enabled: false
  logLevel: info
```

---

## Controller Component

**Binary:** `cmd/controller/main.go`  
**Image:** `quay.io/javierpena/telco-anomaly-controller:latest`  
**Deployment:** `config/manager/manager.yaml`

### Startup

The entrypoint initialises a controller-runtime `Manager` with the following scheme registrations:
- `k8s.io/client-go/kubernetes/scheme` (core Kubernetes types)
- `open-cluster-management.io/api/cluster/v1` (ManagedCluster)
- `github.com/javierpena/telco-anomaly-detection/api/v1alpha1` (TelcoHealthcheck)

Flags:
- `--metrics-bind-address` (default `:8080`)
- `--health-probe-bind-address` (default `:8081`)
- `--leader-elect` (default `false`)
- `--operator-namespace` (default `telco-healthcheck-system`) — used to find AgenticRun config ConfigMaps and build the alert receiver service URL
- `--alert-receiver-url` — override the in-cluster webhook URL

### Reconcile loop

Triggered by changes to `TelcoHealthcheck` CRs or `ManagedCluster` resources (ManagedCluster events enqueue all TelcoHealthchecks). Steps in order:

1. **Fetch** the `TelcoHealthcheck` CR; skip if not found.
2. **Deletion path** — if `DeletionTimestamp` is set, run cleanup then remove the finalizer.
3. **Finalizer** — ensure `ran.openshift.io/telcohealthcheck-finalizer` is registered; return early if just added (triggers a new reconcile).
4. **Log level sync** — read all `TelcoHealthcheck` CRs; set the logger's atomic level to `debug` if any CR requests it, `info` otherwise.
5. **Ensure AgenticRun ConfigMaps** (`ensureAgenticRunConfigs`) — create the three AgenticRun config ConfigMaps in the operator namespace if they do not already exist. Returns an error (requeueing the object) if creation fails. Never overwrites an existing ConfigMap.
5a. **Reconcile kube-compare-mcp** (`reconcileKubeCompareMCP`) — when `rdsCompliance.enabled` is true, creates the registry credentials secret and applies the kube-compare-mcp ServiceAccount, ClusterRole, ClusterRoleBinding, Deployment, Service, and Route via server-side apply. When false, removes all of those resources. Returns an error that stops the reconcile if any step fails.
6. **Resolve monitored clusters** (`getMonitoredClusters`) — list all `ManagedCluster` resources and apply include/exclude rules from the spec. Result stored in `status.monitoredClusters`.
7. **Reconcile Thanos alert rules** (`reconcileAlertRules`) — create or update the `thanos-ruler-custom-rules` ConfigMap in `open-cluster-management-observability`. Non-fatal if this fails.
8. **Reconcile AlertManager receiver** (`reconcileAlertManagerReceiver`) — read the `alertmanager-config` Secret in `open-cluster-management-observability`, upsert a webhook receiver entry pointing to the alert receiver service URL. Non-fatal if this fails.
8a. **Reconcile MCO custom metrics allowlist** (`reconcileObservabilityMetrics`) — create or update the `observability-metrics-custom-allowlist` ConfigMap in `open-cluster-management-observability`. Content is driven by `spec.alerts`: when `podNetwork` is true the four container-network error/drop metrics are listed; when `ovsProcessCPU` is true the two OVS process CPU metrics are added; when all flags are false the ConfigMap is written with an empty list. Non-fatal if this fails.
9. **Periodic checks** (`runPeriodicChecks`) — for each enabled sub-check, if its period has elapsed, create AgenticRuns on all monitored spoke clusters. Currently only RDS compliance is implemented. Updates `status.lastRDSComplianceRunTime`.
10. **Persist status** — write updated status back to the API server.
11. **Requeue** — return `ctrl.Result{RequeueAfter: <time-until-next-check>}`.

### Cleanup (on deletion)

`cleanupResources` runs four steps (all attempted even if earlier ones fail):
1. Remove the AlertManager webhook receiver entry from `alertmanager-config`.
2. Delete the `thanos-ruler-custom-rules` ConfigMap.
3. Remove all kube-compare-mcp resources (`cleanupKubeCompareMCP`) — idempotent, ignores not-found.
4. Delete the `observability-metrics-custom-allowlist` ConfigMap (`cleanupObservabilityMetrics`) — idempotent, ignores not-found.

---

## Alert Receiver Component

**Binary:** `cmd/alertreceiver/main.go`  
**Image:** `quay.io/javierpena/telco-anomaly-alert-receiver:latest`  
**Deployment:** `config/manager/alertreceiver.yaml`

### Startup

Uses a plain (non-caching) `client.Client` built directly from the in-cluster config. This avoids the overhead of a full controller-runtime `Manager` since the receiver only reads resources on demand.

Flags:
- `--bind-address` (default `:8080`)

### HTTP endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/webhook` | AlertManager webhook payload handler |
| `GET` | `/healthz` | Liveness and readiness probe |

### Webhook processing flow (`POST /webhook`)

```
AlertManager → POST /webhook
  │
  ├─ Parse AlertManager JSON payload
  │
  ├─ getMonitoredClusters()
  │    List all TelcoHealthcheck CRs
  │    Union of status.monitoredClusters → set of valid cluster names
  │    Fetch <name>/<name>-admin-kubeconfig Secret for each
  │    Side-effect: update log level from most-verbose CR
  │
  ├─ getDefinedAlertNames()
  │    Read thanos-ruler-custom-rules ConfigMap
  │    Parse "alert: <name>" lines → set of valid alert names
  │    On failure: log error, skip all alerts (non-fatal)
  │
  └─ For each alert in payload:
       │
       ├─ Check labels.cluster ∈ monitoredClusters  → skip if not
       ├─ Check labels.alertname ∈ definedAlertNames → skip if not
       │
       └─ createAgenticRunOnCluster()
            │
            ├─ Look up alertConfigMaps[alertName] → ConfigMap name
            │    "TelcoHealthCheckHostNetwork"     → telco-anomaly-host-network-config
            │    "TelcoHealthCheckPodNetwork"      → telco-anomaly-pod-network-config
            │    "TelcoHealthCheckHostReservedCPU" → telco-anomaly-host-reserved-cpu-config
            │    "TelcoHealthCheckOVSProcessCPU"   → telco-anomaly-ovs-process-cpu-config
            │    Unknown → log warning, skip
            │
            ├─ LoadRunConfig(HubClient, configMapName, "telco-healthcheck-system")
            │    Parse request / skills / mcpServers from ConfigMap data
            │
            ├─ Build spoke client from kubeconfig bytes
            │
            └─ Create AgenticRun in openshift-lightspeed on spoke cluster
```

---

## AgenticRun Resources

**Group / Version / Kind:** `agentic.openshift.io/v1alpha1 / AgenticRun`  
**Target namespace (on spoke):** `openshift-lightspeed`

The Go client library for `AgenticRun` is not yet published, so all objects are created via `unstructured.Unstructured`.

### Naming

| Trigger | Name pattern |
|---|---|
| Alert | `telco-alert-<alertName>-<unixNano>` |
| Periodic check | `telco-health-<checkType>-<unixNano>` |

### Labels applied

| Label | Value |
|---|---|
| `app.kubernetes.io/managed-by` | `telco-anomaly-detection` |
| `telco-anomaly.io/trigger-type` | `alert` or (periodic path uses `healthcheck-ref` instead) |
| `telco-anomaly.io/trigger-alert` | Alert name (alert path only) |
| `telco-anomaly.io/trigger-cluster` | Cluster name (alert path only) |
| `telco-anomaly.io/healthcheck-ref` | TelcoHealthcheck CR name (periodic path) |
| `telco-anomaly.io/owner-namespace` | TelcoHealthcheck CR namespace (periodic path) |

### Spec fields populated

```yaml
spec:
  request: "<from ConfigMap>"
  analysis:                       # always set; required by the CRD
    agent: "default"
  tools:                          # omitted when both skills and mcpServers are empty
    skills:                       # set independently when there is ≥ 1 skill with non-empty paths
      - image: "quay.io/..."
        paths:
          - "/skills/foo.yaml"
    mcpServers:                   # set independently when mcpServers is non-empty
      - name: my-mcp
        url: "http://..."
```

Skills and mcpServers are populated independently — having one does not require the other. Skills with an empty `paths` list are silently filtered out before building the object. The entire `spec.tools` block is omitted only when both are absent (not an error — the AgenticRun is still created with just `request` and `analysis`).

AgenticRun names are always lowercased (RFC 1123 subdomain requirement).

### ConfigMap-driven configuration

Each trigger type reads its AgenticRun parameters from a dedicated ConfigMap in `telco-healthcheck-system`:

| Trigger type | ConfigMap name |
|---|---|
| `TelcoHealthCheckHostNetwork` alert | `telco-anomaly-host-network-config` |
| `TelcoHealthCheckPodNetwork` alert | `telco-anomaly-pod-network-config` |
| `TelcoHealthCheckHostReservedCPU` alert | `telco-anomaly-host-reserved-cpu-config` |
| `TelcoHealthCheckOVSProcessCPU` alert | `telco-anomaly-ovs-process-cpu-config` |
| `rds-compliance` periodic check | `telco-anomaly-rds-compliance-config` |

**ConfigMap data keys:**

| Key | Format | Maps to |
|---|---|---|
| `request` | Plain string | `spec.request` |
| `skills` | JSON array of `{image, paths[]}` | `spec.tools.skills` |
| `mcpServers` | JSON array of `{name, url, timeoutSeconds?}` | `spec.tools.mcpServers` |

An absent key, empty string, or empty JSON array (`[]`) causes the corresponding field to be omitted from the AgenticRun spec. If `request` is empty the AgenticRun is skipped with an error log.

These ConfigMaps are deployed as static manifests (`config/manager/agenticrun-configs.yaml`) and the controller also ensures they exist at startup via `ensureAgenticRunConfigs` (create-if-absent, never overwrite).

### Variable expansion in ConfigMap values

All string fields (`request`, `mcpServers[*].url`, `mcpServers[*].name`, `skills[*].image`) support `${VAR}` placeholders that are resolved at AgenticRun creation time. Unknown variables are left as `${VAR}` rather than silently blanked, so a misconfigured placeholder remains visible in the AgenticRun spec.

**Standard variables** resolved via `agenticrun.BuildVarMap` on every AgenticRun creation:

| Variable | Value |
|---|---|
| `${OPERATOR_NAMESPACE}` | The operator namespace (`telco-healthcheck-system`) |
| `${CLUSTER_NAME}` | The target spoke cluster name |
| `${NODE_NAME}` | The node that triggered the alert (from `annotations.node`); alert path only — absent for periodic checks |
| `${KUBE_COMPARE_MCP_URL}` | HTTP URL of the `telco-anomaly-kube-compare-mcp` Route (read from `status.ingress[0].host`); absent if the Route is not yet admitted |

The expansion is implemented in `internal/agenticrun/expand.go` (`ExpandVariables`) and `internal/agenticrun/vars.go` (`BuildVarMap`). Both call sites (controller periodic path in `internal/controller/agenticrun.go` and alert receiver path in `internal/alertreceiver/handler.go`) call `BuildVarMap` then `ExpandVariables` between `LoadRunConfig` and `BuildObject`.

---

## Thanos Alert Rules

Managed by `reconcileAlertRules` in `internal/controller/alertrules.go`.

**Target:** ConfigMap `thanos-ruler-custom-rules` in namespace `open-cluster-management-observability`  
**Key:** `custom_rules.yaml`  
**Labels:** `app.kubernetes.io/managed-by: telco-anomaly-detection`

The ConfigMap is created or updated on every reconcile based on the `spec.alerts` field. The Thanos Ruler config-reload sidecar picks up changes automatically.

### Current alert rules

| Alert name | Enabled by | Expression |
|---|---|---|
| `TelcoHealthCheckHostNetwork` | `spec.alerts.hostNetwork: true` | `sum by (clusterID, cluster, instance, prometheus) (instance:node_network_receive_drop_excluding_lo:rate1m > 1)` or transmit equivalent |
| `TelcoHealthCheckPodNetwork` | `spec.alerts.podNetwork: true` | `sum by (clusterID, cluster, instance, pod) (container_network_receive_errors_total > 1)` or receive drops / transmit errors / transmit drops equivalents |
| `TelcoHealthCheckHostReservedCPU` | `spec.alerts.hostReservedCPU: true` | `openshift:cpu_usage_cores:sum > 3` |
| `TelcoHealthCheckOVSProcessCPU` | `spec.alerts.ovsProcessCPU: true` | `irate(ovs_db_process_cpu_seconds_total[10m]) > 1.0 or irate(ovs_vswitchd_process_cpu_seconds_total[10m]) > 1.0` |

---

## AlertManager Configuration

Managed by `reconcileAlertManagerReceiver` in `internal/controller/alertmanager.go`.

**Target:** Secret `alertmanager-config` in namespace `open-cluster-management-observability`  
**Key:** `alertmanager.yaml`

The controller upserts a receiver named `telco-anomaly-webhook` and a matching `continue: true` route. All other config in the Secret is preserved. On deletion, the entry is removed.

**Receiver config injected:**

```yaml
receivers:
  - name: telco-anomaly-webhook
    webhook_configs:
      - url: "http://telco-anomaly-alert-receiver.telco-healthcheck-system.svc.cluster.local:8080/webhook"
        send_resolved: true
route:
  routes:
    - receiver: telco-anomaly-webhook
      continue: true
```

---

## MCO Custom Metrics Allowlist

Managed by `reconcileObservabilityMetrics` in `internal/controller/observabilitymetrics.go`.

**Target:** ConfigMap `observability-metrics-custom-allowlist` in namespace `open-cluster-management-observability`  
**Key:** `metrics_list.yaml`  
**Labels:** `app.kubernetes.io/managed-by: telco-anomaly-detection`

Applied on the hub cluster, this ConfigMap is picked up by MCO and propagated to the `metrics-collector` on every managed cluster, extending the set of metrics forwarded to the hub's Thanos Receive.

The ConfigMap content is computed by `buildMetricsListYAML` from `spec.alerts`. When all alert flags are false the key is written with `names: []` (inert but present). On CR deletion the ConfigMap is removed.

### Current metric groups

| Metric | Enabled by |
|---|---|
| `container_network_receive_errors_total` | `spec.alerts.podNetwork: true` |
| `container_network_receive_packets_dropped_total` | `spec.alerts.podNetwork: true` |
| `container_network_transmit_errors_total` | `spec.alerts.podNetwork: true` |
| `container_network_transmit_packets_dropped_total` | `spec.alerts.podNetwork: true` |
| `openshift:cpu_usage_cores:sum` | `spec.alerts.hostReservedCPU: true` |
| `ovs_db_process_cpu_seconds_total` | `spec.alerts.ovsProcessCPU: true` |
| `ovs_vswitchd_process_cpu_seconds_total` | `spec.alerts.ovsProcessCPU: true` |

### Future enhancement: namespace-scoped collection

The current implementation collects these metrics across all namespaces on every spoke cluster. The MCO `names:` field does not support label or namespace selectors — it accepts only bare metric names.

Namespace-scoped collection is possible via the `recording_rules:` section of `metrics_list.yaml`, where the `expr:` field accepts full PromQL including `{namespace=~"ns1|ns2"}` label matchers. However, recording rules create a **new derived metric** on the spoke (named by the `record:` field) that is distinct from the original metric name. Any Thanos queries or alert expressions on the hub would need to reference the derived name.

A potential implementation, taking advantage of the now-required `spec.managedNamespaces` field:
- When `spec.managedNamespaces` is non-empty: replace `names:` with `recording_rules:`, one entry per metric, with `expr: <metric>{namespace=~"<joined-list>"}` and a `record:` name following the Prometheus convention (e.g. `container_network_receive_errors_total:managed_namespaces`).
- When `spec.managedNamespaces` is empty: `spec.alerts.podNetwork` would have nothing to scope to, so the metric list would remain empty (`names: []`) regardless of alert flags — consistent with the field's semantics that an empty namespace list means no monitoring is active.

---

## Spoke Cluster Access

Each managed cluster's admin kubeconfig is read from a Secret on the hub:

```
Secret name:      <clusterName>-admin-kubeconfig
Secret namespace: <clusterName>
Secret key:       kubeconfig
```

The controller and alert receiver both build a spoke-side `client.Client` from these bytes via `clientcmd.RESTConfigFromKubeConfig`. The spoke client is used exclusively to create `AgenticRun` objects in `openshift-lightspeed`.

---

## Namespaces

| Namespace | Cluster | Purpose |
|---|---|---|
| `telco-healthcheck-system` | Hub | Operator pods, ServiceAccount, AgenticRun config ConfigMaps |
| `open-cluster-management-observability` | Hub | MCO stack: Thanos Receive/Ruler/Store/Querier, AlertManager, `thanos-ruler-custom-rules` ConfigMap, `alertmanager-config` Secret, `observability-metrics-custom-allowlist` ConfigMap |
| `<clusterName>` | Hub | `<clusterName>-admin-kubeconfig` Secret per spoke cluster |
| `open-cluster-management-addon-observability` | Each spoke | MCO observability-addon and `metrics-collector` pods |
| `openshift-lightspeed` | Each spoke | AgenticRun objects created by this operator |

---

## Kubernetes Manifests

### `config/manager/namespace.yaml`

Creates the `telco-healthcheck-system` namespace with Pod Security Standards labels (`enforce/audit/warn: restricted`).

### `config/manager/manager.yaml`

`Deployment` for the controller pod (`telco-anomaly-controller`).

- Image: `quay.io/javierpena/telco-anomaly-controller:latest`
- Args: `--leader-elect`, `--health-probe-bind-address=:8081`, `--metrics-bind-address=:8080`
- Resources: limits 500m CPU / 256Mi RAM; requests 100m CPU / 64Mi RAM
- Security: `runAsNonRoot`, `RuntimeDefault` seccomp, all capabilities dropped

### `config/manager/alertreceiver.yaml`

`Deployment` (`telco-anomaly-alert-receiver`) and `Service` (`telco-anomaly-alert-receiver`, port 8080) for the webhook receiver.

- Image: `quay.io/javierpena/telco-anomaly-alert-receiver:latest`
- Args: `--bind-address=:8080`
- Liveness/readiness: `GET /healthz`
- Resources: limits 200m CPU / 128Mi RAM; requests 50m CPU / 32Mi RAM

### `config/manager/agenticrun-configs.yaml`

Four ConfigMaps deployed alongside the operator (some with real `request` prompts, others still placeholder):

- `telco-anomaly-host-network-config` — real `request` prompt
- `telco-anomaly-pod-network-config` — placeholder `request`
- `telco-anomaly-host-reserved-cpu-config` — real `request` prompt
- `telco-anomaly-rds-compliance-config` — real `request` prompt; `mcpServers` wired to `${KUBE_COMPARE_MCP_URL}`
- `telco-anomaly-ovs-process-cpu-config` — real `request` prompt for OVS process CPU investigation

Edit these to supply real `request`, `skills`, and `mcpServers` values. The controller never overwrites them after initial creation.

### `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml`

The CRD manifest for `TelcoHealthcheck`. Regenerated via `make manifests`.

### `config/rbac/`

| File | Resource |
|---|---|
| `serviceaccount.yaml` | `ServiceAccount` `telco-anomaly-operator` in `telco-healthcheck-system` |
| `role.yaml` | `ClusterRole` `telco-anomaly-operator` with the permissions below |
| `rolebinding.yaml` | `ClusterRoleBinding` binding the role to the ServiceAccount |

**ClusterRole permissions summary:**

| Resource | Verbs |
|---|---|
| `telcohealthchecks` (ran.openshift.io) | get, list, watch, update, patch |
| `telcohealthchecks/status` | get, update, patch |
| `telcohealthchecks/finalizers` | update |
| `managedclusters` (cluster.open-cluster-management.io) | get, list, watch |
| `secrets` | get, list, watch, update, patch, create, delete |
| `configmaps` | get, list, watch, create, update, patch, delete |
| `leases` (coordination.k8s.io) | get, list, watch, create, update, patch, delete |
| `events` | create, patch |
| `serviceaccounts` | get, list, watch, create, update, patch, delete |
| `services` | get, list, watch, create, update, patch, delete |
| `deployments` (apps) | get, list, watch, create, update, patch, delete |
| `routes` (route.openshift.io) | get, list, watch, create, update, patch, delete |
| `clusterroles`, `clusterrolebindings` (rbac.authorization.k8s.io) | get, list, watch, create, update, patch, delete, escalate, bind |

### `config/samples/ran_v1alpha1_telcohealthcheck.yaml`

Example `TelcoHealthcheck` CR for reference.

---

## Skills OCI Image

**Dockerfile:** `Dockerfile.skills`  
**Image:** `quay.io/javierpena/telco-anomaly-skills:latest`

Contains skill definition files under `skills/`. Each subdirectory is one skill. The image path(s) to load for each check type are configured in the corresponding AgenticRun config ConfigMap (`skills` key).

### Current skills

| Skill | Directory | Description |
|---|---|---|
| SR-IOV pod network health | `skills/check-sriov-pod-network-health/` | Diagnoses SR-IOV network issues on telco pods |

---

## Data Flow Summary

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 OPERATOR CONFIGURATION PATH
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

TelcoHealthcheck CR change / ManagedCluster change
        │
        ▼
Controller reconcile
  ├── Ensure AgenticRun config ConfigMaps exist (create-if-absent)
  ├── Resolve monitored clusters (ManagedCluster list + include/exclude)
  ├── Write thanos-ruler-custom-rules ConfigMap
  │       └─► Thanos Ruler reloads rules (config-reload sidecar)
  │
  └── Write alertmanager-config Secret
          └─► AlertManager reloads routing config


━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 ALERT-DRIVEN AGENTIC RUN PATH
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Spoke cluster
  node_exporter (network drop metrics)
        │ scrape
        ▼
  observability-addon metrics-collector
        │ remote_write (HTTPS)
        ▼
Hub: Thanos Receive → Thanos Store
        │
        ▼
Hub: Thanos Ruler evaluates thanos-ruler-custom-rules
  [instance:node_network_receive_drop_excluding_lo:rate1m > 1]
  Alert fires with labels: alertname, cluster, severity
        │
        ▼
Hub: AlertManager
  routes via telco-anomaly-webhook (continue: true)
        │ POST /webhook  (AlertManager JSON payload)
        ▼
Alert Receiver
  ├── Validate labels.cluster ∈ MonitoredClusters
  ├── Validate labels.alertname ∈ thanos-ruler-custom-rules
  ├── Look up AgenticRun config ConfigMap for alertname
  ├── Load request / skills / mcpServers from ConfigMap
  ├── Resolve ${VAR} placeholders (BuildVarMap → ExpandVariables)
  └── Create AgenticRun on spoke cluster (openshift-lightspeed)
              └─► OpenShift Lightspeed executes agentic investigation


━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 PERIODIC AGENTIC RUN PATH
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Controller reconcile (period elapsed)
  ├── Look up AgenticRun config ConfigMap for check type
  ├── Load request / skills / mcpServers from ConfigMap
  └── For each monitored cluster:
        ├── Resolve ${VAR} placeholders (BuildVarMap → ExpandVariables)
        └── Create AgenticRun on spoke cluster (openshift-lightspeed)
                    └─► OpenShift Lightspeed executes agentic investigation
```

---

## kube-compare-mcp Component

When `spec.periodicHealthChecks.rdsCompliance.enabled` is `true`, the controller deploys a [kube-compare-mcp](https://github.com/sakhoury/kube-compare-mcp) server in the operator namespace. This MCP server exposes the `kube-compare` tool over HTTP so that OpenShift Lightspeed `AgenticRun` jobs can call it to compare cluster state against Telco RDS reference configurations.

The controller manages the full lifecycle of this component: it is created when RDS compliance is enabled and removed when it is disabled (or when the `TelcoHealthcheck` CR is deleted). All resources are applied via server-side apply, making the operation idempotent.

### Resources managed

| Kind | Name | Scope |
|---|---|---|
| `Secret` | `kube-compare-registry-credentials` | `telco-healthcheck-system` |
| `ServiceAccount` | `kube-compare-mcp` | `telco-healthcheck-system` |
| `ClusterRole` | `kube-compare-mcp-reader` | Cluster |
| `ClusterRoleBinding` | `kube-compare-mcp-reader` | Cluster |
| `Deployment` | `telco-anomaly-kube-compare-mcp` | `telco-healthcheck-system` |
| `Service` | `telco-anomaly-kube-compare-mcp` | `telco-healthcheck-system` |
| `Route` | `telco-anomaly-kube-compare-mcp` | `telco-healthcheck-system` |

The registry credentials secret is copied from `pull-secret` in `openshift-config` (the standard cluster pull secret) and kept in sync on every reconcile.

### Embedded manifests

The RBAC and workload manifests are embedded in the controller binary via `//go:embed` directives in `internal/controller/kubecomparemcp.go`:

- `internal/controller/assets/kube-compare-rbac.yaml` — ServiceAccount, ClusterRole, ClusterRoleBinding
- `internal/controller/assets/kube-compare-mcp.yaml` — Deployment, Service, Route

To update the kube-compare-mcp deployment (e.g. new image tag), edit these files and rebuild the controller image.

---

## Deferred / In-Progress Work

| Phase | Item |
|---|---|
| Phase 8 | Real Prometheus expressions for `podNetwork` alert rules |
| Phase 9 | Real `request` prompts and skill OCI image paths in AgenticRun config ConfigMaps |
| ~~Phase 10~~ | ~~Wire the kube-compare-mcp MCP server URL into `telco-anomaly-rds-compliance-config`~~ — **Done**: `${KUBE_COMPARE_MCP_URL}` placeholder wired; resolved at runtime via Route lookup |
