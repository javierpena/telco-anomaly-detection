# Telco Anomaly Detection Operator — Architecture

> **Maintenance note:** Keep this document in sync with code changes. See `AGENTS.md` for the reminder.

## Overview

This operator runs on an OpenShift hub cluster managed by Red Hat Advanced Cluster Management (ACM). It monitors telco workloads across multiple ACM spoke clusters by:

1. Configuring Thanos alert rules that fire when network conditions degrade.
2. Forwarding those alerts to an alert receiver webhook.
3. On each alert (or on a periodic schedule), creating an `AgenticRun` on the hub in `openshift-lightspeed` with `spec.targetCluster` naming the affected spoke.
4. Keeping a hub-side `TelcoHealthCheckRun` record for each run and synchronizing its hub AgenticRun condition and `AnalysisResult` conclusion.

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
│  openshift-lightspeed-managed namespace        │
│  ┌─────────────────────────────────────────┐   │
│  │ SAs/RBAC provisioned by lightspeed-hub │   │
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
│  openshift-lightspeed namespace                             │
│  AgenticRun (spec.targetCluster: spoke) + AnalysisResult    │
│  lightspeed-hub standing spoke-kubeconfig-{name} Secret     │
│                                                            │
│  telco-healthcheck-system namespace                        │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ Controller pod                                       │  │
│  │  - writes thanos-ruler-custom-rules ConfigMap        │  │
│  │  - writes alertmanager-config Secret                 │  │
│  │  - creates hub AgenticRuns (periodic)                │  │
│  │  - syncs hub run/result status to records           │  │
│  │  - manages kube-compare-mcp lifecycle                │  │
│  │                                                      │  │
│  │ Alert Receiver pod                                   │  │
│  │  - POST /webhook ← AlertManager                      │  │
│  │  - creates hub AgenticRuns (alert-driven)            │  │
│  │                                                      │  │
│  │ kube-compare-mcp pod (when rdsCompliance.enabled)    │  │
│  │  - MCP server exposing kube-compare tooling          │  │
│  │  - used by AgenticRuns for RDS compliance checks     │  │
│  │                                                      │  │
│  │ ConfigMaps (AgenticRun config)                       │  │
│  │  telco-anomaly-host-network-config                   │  │
│  │  telco-anomaly-pod-network-config                    │  │
│  │  telco-anomaly-rds-compliance-config                 │  │
│  │  telco-anomaly-low-latency-check-config              │  │
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

The receiver validates `labels.alertname` against the Thanos rules it wrote itself (by re-reading `thanos-ruler-custom-rules`) and `labels.cluster` against the `status.monitoredClusters` field of the singleton `TelcoHealthcheck` CR. Both checks must pass before an `AgenticRun` is created.

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
**Scope:** Cluster (singleton — canonical name `telco-healthcheck`)  
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
| `alerts.userAlerts` | `bool` | Enable discovery of user-defined alert ConfigMaps. See **User-defined alert ConfigMaps** below. |
| `alertManager.url` | URL (optional `alertManager` object) | Base URL of the external Alertmanager receiving action-required run alerts at `/api/v2/alerts`. When the object is absent, no run alerts are sent. |
| `alertManager.authType` | `none\|bearer\|basic` | Authentication method. Defaults to `none` when omitted. |
| `alertManager.credentialsSecret.name` | `string` | Secret name in the operator namespace. Required for `bearer` and `basic`; omitted for `none`. |
| `periodicHealthChecks.period` | `duration` | Default interval between periodic checks. Zero disables all periodic checks. |
| `periodicHealthChecks.minJitter` | `duration` (optional) | Minimum independent delay per monitored cluster before creating a periodic run; defaults to `30s`. |
| `periodicHealthChecks.maxJitter` | `duration` (optional) | Maximum independent delay per monitored cluster; defaults to `5m`. Set both bounds to `0s` to create synchronously. |
| `periodicHealthChecks.rdsCompliance.enabled` | `bool` | Activate the RDS compliance periodic check. |
| `periodicHealthChecks.rdsCompliance.period` | `duration` | Override interval for the RDS compliance check. |
| `periodicHealthChecks.rdsCompliance.minJitter` / `.maxJitter` | `duration` (optional) | Override either global jitter bound independently for RDS compliance. |
| `periodicHealthChecks.lowLatencyCheck.enabled` | `bool` | Activate low-latency readiness checks; disabled when omitted. |
| `periodicHealthChecks.lowLatencyCheck.period` | `duration` (optional) | Override the default interval for low-latency checks. |
| `periodicHealthChecks.lowLatencyCheck.minJitter` / `.maxJitter` | `duration` (optional) | Override either global jitter bound independently for low-latency checks. |
| `logLevel` | `info\|debug` | Controls log verbosity in both pods. Changes take effect on next reconcile without restart. |
| `purgeInterval` | `duration` (optional) | Maximum age of hub run records; when set, an hourly CronJob removes expired records. Must be a positive whole number of seconds. |

### Status fields

| Field | Description |
|---|---|
| `monitoredClusters` | Resolved list of cluster names currently being monitored. |
| `lastPeriodicRunTime` | Timestamp of the last generic periodic check (reserved; not yet written). |
| `lastRDSComplianceRunTime` | Timestamp of the last RDS compliance check. |
| `lastLowLatencyCheckRunTime` | Timestamp of the last low-latency check schedule claim. |
| `conditions` | Standard `metav1.Condition` array for reconciliation state. |

### Sample CR

```yaml
apiVersion: ran.openshift.io/v1alpha1
kind: TelcoHealthcheck
metadata:
  name: telco-healthcheck  # cluster-scoped singleton; must use this exact name
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
    lowLatencyCheck:
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

### Prometheus metrics

The controller-runtime metrics server serves `GET /metrics` on
`--metrics-bind-address` (default `:8080`). The
`telco-anomaly-controller-metrics` ClusterIP Service in
`telco-healthcheck-system` selects the controller pod and exposes its named
`metrics` port (8080/TCP) for in-cluster scraping. Scrape discovery is
configured separately; no ServiceMonitor is installed by this project.

The controller registers these application gauges alongside the built-in
controller-runtime metrics:

| Metric | Meaning |
|---|---|
| `telco_healthcheck_managed_clusters` | Length of `status.monitoredClusters` on the canonical `TelcoHealthcheck` singleton (zero when absent). |
| `telco_healthcheck_runs` | Number of hub-side `TelcoHealthCheckRun` records in the configured operator namespace. |
| `telco_healthcheck_runs_action_required` | Number of those records with `status.agenticRunActionRequired` exactly `"True"`. |
| `telco_healthcheck_runs_by_phase{phase="..."}` | Number of those records for each current `status.agenticRunStatus.phase`. Missing or empty phases use `"Unknown"`. |
| `telco_healthcheck_runs_by_type{type="..."}` | Number of those records for each current `status.agenticRunStatus.type` (the latest AgenticRun condition type). Missing or empty types use `"Unknown"`. |

The collector reads the hub API on each scrape, so run deletion, status
changes, and purges affect the next successful scrape. It emits only phases
and types present in the current records, with no per-cluster or per-run labels. A
missing singleton reports zero managed clusters; other API read failures fail
the scrape rather than exposing partial or misleading values. Run counts
include all records in the operator namespace, even for clusters that are no
longer monitored.

### Validating webhook (singleton enforcement)

The controller binary also runs a validating admission webhook on port `9443`. It is registered at path `/validate-ran-openshift-io-v1alpha1-telcohealthcheck` and rejects any CREATE request whose `metadata.name` is not `telco-healthcheck`.

TLS is provided by the OpenShift service CA operator: the Service `telco-anomaly-webhook` in `telco-healthcheck-system` carries the annotation `service.beta.openshift.io/serving-cert-secret-name: telco-anomaly-webhook-cert`, which causes the service CA to issue and auto-rotate a serving cert stored in the `telco-anomaly-webhook-cert` Secret. The `ValidatingWebhookConfiguration` carries `service.beta.openshift.io/inject-cabundle: "true"` so the CA bundle is injected automatically.

The webhook uses `failurePolicy: Fail`, meaning CR creation is blocked while the controller pod is starting. The Makefile deploy order (`config/webhook/` before `config/manager/`) ensures the webhook Service and the cert Secret exist before the pod starts.

A dedicated readiness check (`webhook-ca-bundle`) polls the `ValidatingWebhookConfiguration` via the API reader on every probe and returns not-ready until the CA bundle is non-empty. This keeps the pod in `0/1 Ready` during the injection window, making it safe to wait for `1/1 Ready` before creating the `TelcoHealthcheck` CR.

### Reconcile loop

Triggered by changes to the singleton `TelcoHealthcheck` CR or `ManagedCluster` resources (ManagedCluster events enqueue the canonical CR `telco-healthcheck`). Steps in order:

1. **Fetch** the `TelcoHealthcheck` CR; skip if not found.
2. **Deletion path** — if `DeletionTimestamp` is set, run cleanup then remove the finalizer.
3. **Finalizer** — ensure `ran.openshift.io/telcohealthcheck-finalizer` is registered; return early if just added (triggers a new reconcile).
4. **Log level sync** — read `spec.logLevel` from the CR; set the logger's atomic level to `debug` or `info` accordingly.
4a. **Resolve monitored clusters** (`getMonitoredClusters`) — list `ManagedCluster` resources and apply include/exclude rules. The separate run-record reconciler watches hub AgenticRuns and AnalysisResults independently.
4b. **Recover pending hub records** — check stale hub-origin pending records against hub AgenticRuns. Ignore legacy spoke-era records. Recovery precedes managed-resource reconciliation so unrelated resource errors cannot stop it.
5. **Reconcile system-alert ConfigMaps** (`reconcileSystemAlertConfigMaps`) — for each of the four system alerts (`hostNetwork`, `podNetwork`, `hostReservedCPU`, `ovsProcessCPU`), creates or updates the corresponding ConfigMap in the operator namespace from the embedded asset file when the spec boolean is `true`, and deletes it when `false`. Must run before the alert-rule listing step. Returns an error (requeueing) if any create/update/delete fails.
5a. **Reconcile system-periodic ConfigMaps** (`reconcileSystemPeriodicConfigMaps`) — for each periodic check type (`rdsCompliance`, `lowLatencyCheck`), creates or updates the corresponding ConfigMap in the operator namespace from the embedded asset file when the spec boolean is `true`, and deletes it when `false`. Mirrors the behaviour of step 5 for system-alert ConfigMaps. Returns an error (requeueing) if any create/update/delete fails.
5b. **Reconcile kube-compare-mcp** (`reconcileKubeCompareMCP`) — when `rdsCompliance.enabled` is true, creates the registry credentials secret and applies the kube-compare-mcp ServiceAccount, ClusterRole, ClusterRoleBinding, Deployment, Service, and Route via server-side apply. When false, removes all of those resources. Returns an error that stops the reconcile if any step fails.
5c. **Reconcile purge CronJob** — create/update `telco-healthcheck-purge` when `spec.purgeInterval` is set; remove it otherwise.
7. **Reconcile Thanos alert rules** (`reconcileAlertRules`) — lists system-alert ConfigMaps (always) and user-alert ConfigMaps (when `userAlerts` is true) in the operator namespace, builds a unified Prometheus YAML from their `alertRule` fields, and creates or updates `thanos-ruler-custom-rules` in `open-cluster-management-observability`. Group names come from `alertGroupName` (system alerts) or default to `telco-user-<alertname-lowercased>` (user alerts). Non-fatal if this fails.
8. **Reconcile AlertManager receiver** (`reconcileAlertManagerReceiver`) — read the `alertmanager-config` Secret in `open-cluster-management-observability`, upsert a webhook receiver entry pointing to the alert receiver service URL. Non-fatal if this fails.
8a. **Reconcile MCO custom metrics allowlist** (`reconcileObservabilityMetrics`) — same listing pattern as step 7; independently deduplicates metric names from `alertMetrics` and `alertUserWorkloadMetrics`, writing them to `metrics_list.yaml` and `uwl_metrics_list.yaml` respectively in `observability-metrics-custom-allowlist` in `open-cluster-management-observability`. Both keys are always written, using `names: []` when empty. Non-fatal if this fails.
9. **Periodic checks** (`runPeriodicChecks`) — `configuredPeriodicChecks` binds each check's name, enablement, period/jitter overrides, and own status timestamp into a descriptor. One loop handles all enabled checks in registration order: when due, validate its AgenticRun config and jitter window, then persist the due-time claim before scheduling distinct hub AgenticRuns targeting monitored spokes. Add future checks to the descriptor list instead of duplicating the scheduling block. The status update uses the API resource version as an optimistic lock: a queued reconcile holding stale status stops before scheduling another run. The earliest due check determines the next requeue; remaining status is persisted at the end of reconciliation. Each target receives an independently sampled, inclusive delay within its effective jitter window. Delayed tasks use the manager shutdown context so they stop on pod shutdown; `0s`/`0s` retains synchronous creation. A fixed positive window delays every cluster by that amount. The status timestamp records the scheduling decision, not the completion time. Delayed tasks exist only in memory: a shutdown before creation can skip that cluster for this interval, and a jitter window longer than the period can leave intervals overlapping.
10. **Persist status** — write status changes not already included in the periodic schedule claim back to the API server.
11. **Requeue** — return `ctrl.Result{RequeueAfter: <time-until-next-check>}`.

### Cleanup (on deletion)

`cleanupResources` attempts each cleanup step even if an earlier one fails:
1. Remove the AlertManager webhook receiver entry from `alertmanager-config`.
2. Delete the `thanos-ruler-custom-rules` ConfigMap.
3. Remove all kube-compare-mcp resources (`cleanupKubeCompareMCP`) — idempotent, ignores not-found.
4. Delete the `observability-metrics-custom-allowlist` ConfigMap (`cleanupObservabilityMetrics`) — idempotent, ignores not-found.
5. Delete all four system-alert ConfigMaps (`cleanupSystemAlertConfigMaps`) — idempotent, ignores not-found.
6. Delete all system-periodic ConfigMaps (`cleanupSystemPeriodicConfigMaps`) — idempotent, ignores not-found.
7. Remove the purge CronJob. Hub run records carry an owner reference to the cluster-scoped singleton and are garbage-collected when it is deleted.

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
  │    Get singleton TelcoHealthcheck CR (name: telco-healthcheck)
  │    status.monitoredClusters → set of valid cluster names
  │    Side-effect: update log level from spec.logLevel
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
            ├─ resolveAlertConfigMap(alertName)
            │    List all ConfigMaps in telco-healthcheck-system with
            │    label app.kubernetes.io/managed-by: telco-anomaly-detection
            │    Match by data["alertName"] where CM also carries either:
            │      ran.openshift.io/system-managed-alert: "true"  (system alerts)
            │      ran.openshift.io/user-managed-alert: "true"    (user alerts)
            │    Not found → log warning, skip
            │
            ├─ LoadRunConfig(HubClient, configMapName, "telco-healthcheck-system")
            │    Parse request / skills / mcpServers from ConfigMap data
            │
             ├─ Set spec.targetCluster to the validated cluster name
            │
             └─ Create AgenticRun in openshift-lightspeed on the hub
```

---

## AgenticRun Resources

**Group / Version / Kind:** `agentic.openshift.io/v1alpha1 / AgenticRun`  
**Target namespace (on hub):** `openshift-lightspeed`

The Go client library for `AgenticRun` is not yet published, so all objects are created via `unstructured.Unstructured`.

### Naming

| Trigger | Name pattern |
|---|---|
| Alert | `telco-alert-<alertName>-<unixNano>` |
| Periodic check | `telco-health-<checkType>-<unixNano>-<cluster-prefix>-<cluster-hash>` (truncated to 253 characters) |

Each hub create is preceded by a `TelcoHealthCheckRun` record with its
identity, a pending annotation, a hub-origin annotation and `status.agenticRunStatus.phase: Pending`.
Successful hub creation changes the phase to `Created` and removes the
annotation. A definitive API rejection changes the phase to `Failed`; the
record is retained. If creation returns an ambiguous error (for example a
timeout), it stays `Pending` while the controller checks the named hub run
after a grace period. Recovery sets `Created` if the run exists or `Failed`
if it does not. It never creates a second run. Both binaries use
`internal/healthcheckrun/` for this flow.

`Created` means the Kubernetes API accepted the object, not that the
investigation succeeded. The agentic operator reads its standing kubeconfig
later, during spoke-access setup; failed or missing credentials surface during
its reconciliation. Drain all pending spoke-era records with the old deployment
before upgrading, then keep completed records as historical snapshots; only
records marked `ran.openshift.io/hub-agentic-run: "true"` are synchronized or
recovered by the new controller.

### TelcoHealthCheckRun audit records

`ran.openshift.io/v1alpha1` `TelcoHealthCheckRun` (`thcr`) is namespaced in
`telco-healthcheck-system`. Its spec is empty. Status contains `clusterName`,
`agenticRunName`, `triggeredBy` (`alert` or `periodicHealthCheck`), `trigger`,
`agenticRunStatus.{type,phase,summary}`, and optional `agenticRunActionRequired`.
The latter follows `AnalysisResult.status.actionRequired` (`"True"` or
`"False"`) while the latest AgenticRun condition type is `Analyzed`. As soon
as a non-empty type other than `Analyzed` is observed, it becomes `"False"`.
It is omitted while the type is empty, or while `Analyzed` has no analysis
actionRequired value. Returning to `Analyzed` reads the current AnalysisResult
value, even if no new result event arrives. The record timestamp gives its age.
All records use their hub AgenticRun name; periodic run names are unique per
target cluster.
Records carry the singleton owner reference and a hashed run/cluster label
for correlation.

`oc get thcr -n telco-healthcheck-system` displays Type immediately before
Phase, with Cluster, AgenticRun, Action Required, and Age. Type and Phase are
populated from the type and reason of the same latest AgenticRun condition.
Type shows `<none>` until a condition is observed; Action Required shows
`<none>` until a type is observed, or until analysis supplies a value when
the type is `Analyzed`.

The run-record controller watches hub `AgenticRun` and `AnalysisResult` objects
in `openshift-lightspeed` and reconciles the records on run, result, and record
events, including cache replay after restart. It uses the run's `spec.targetCluster`
and run name to look up the hashed run/cluster label and verifies the record's
identity and hub-origin annotation. For results it resolves `.spec.agenticRunName`
back to the hub run; the **last** entry in
`AgenticRun.status.steps.analysis.results` identifies the current result. The hub `phase` is
the verbatim `reason`, and `type` is the verbatim `type`, of the AgenticRun
condition with the latest valid `lastTransitionTime`; phase is not taken from
the `status.phase` lifecycle field. Until such a condition is available it
retains the creation phase. AnalysisResult updates do not change type or phase:
`summary` comes from the top-level diagnosis, first option diagnosis/summary,
or failure reason. AnalysisResult events update `agenticRunActionRequired` only
while the current type is `Analyzed`; other types force `"False"`. On return to
`Analyzed`, reconciliation reads the current result without waiting for another
event. Status patches use optimistic locking and are recomputed on conflict, so
a late result event cannot restore `"True"` after the type moves away from
`Analyzed`. Missing runs (for example, after TTL expiry) leave the last status
intact. Removing a cluster leaves its historical records intact as well.

### External action-required alerts

When `spec.alertManager` is set, the controller sends `TelcoActionRequired`
alerts to the external Alertmanager's `/api/v2/alerts` API under
`spec.alertManager.url`. `authType` defaults to `none`; with `bearer`, the
controller reads the `token` key of `credentialsSecret.name` in the operator
namespace and sends `Authorization: Bearer <token>`. With `basic`, it reads
the `username` and `password` keys and uses HTTP Basic authentication.
Credentials are read for each delivery, so Secret rotation takes effect
without restarting; changes to the referenced Secret also requeue run
records for delivery. Authenticated endpoints must use HTTPS and redirects
are not followed. Missing or invalid credentials leave notifications queued
for retry; credential values are not stored in run records or logs.

In the same status patch that changes `agenticRunActionRequired`, the run-record
reconciler appends a durable `status.alertNotifications` event: firing when the
value becomes `"True"`, resolved when it moves from `"True"` to `"False"` or is removed.
Each event captures the transition time and summary. A dedicated run-record
controller replays queued events after restarts, retries failed requests, and
acknowledges successful delivery in run status. Replays use the same `startsAt`
and labels so retries are idempotent. A record that remains `"True"` is renewed
every minute with `endsAt` five minutes in the future; resolution sends the
original `startsAt` and the transition's `endsAt`. Alertmanager derives
`firing` and `resolved` from these timestamps (the alerts API has no writable
`status` field). Removing `spec.alertManager` stops delivery and clears pending
events; enabling it again notifies any records still requiring action.

Each alert has labels `alertname=TelcoActionRequired`, `severity=warning`,
`cluster`, and `agentic_run`; the latter two distinguish simultaneous runs.
Annotations are `cluster`, `summary`, and `agentic_run`. If the hub's
`openshift-console/console` Route has a valid `spec.host`, `url` links to
`https://<hub-console-host>/lightspeed/runs/openshift-lightspeed/<AgenticRun name>`.
An unavailable or invalid Route omits `url`. The target spoke's console claim
is not used. This outgoing
Alertmanager is separate from the ACM Alertmanager webhook receiver used to
trigger incoming health checks.

When `purgeInterval` is set, an hourly `batch/v1` CronJob uses the existing
`telco-anomaly-operator` ServiceAccount to remove records past that age.
The embedded asset is `internal/controller/assets/purge-cronjob.yaml`.
Removing the setting deletes the CronJob; deleting the singleton garbage-
collects the CronJob and its run records.

### Labels applied

| Label | Value |
|---|---|
| `app.kubernetes.io/managed-by` | `telco-anomaly-detection` |
| `telco-anomaly.io/trigger-type` | `alert` or (periodic path uses `healthcheck-ref` instead) |
| `telco-anomaly.io/trigger-alert` | Alert name (alert path only) |
| `telco-anomaly.io/trigger-cluster` | Cluster name (alert path only) |
| `telco-anomaly.io/healthcheck-ref` | TelcoHealthcheck CR name (periodic path) |

### Spec fields populated

```yaml
spec:
  request: "<from ConfigMap>"
  targetCluster: "<ACM ManagedCluster / SpokeCluster name>"
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

| Trigger type | ConfigMap name | Lifecycle |
|---|---|---|
| `TelcoHealthCheckHostNetwork` alert | `telco-anomaly-host-network-config` | Managed by `reconcileSystemAlertConfigMaps` from embedded asset |
| `TelcoHealthCheckPodNetwork` alert | `telco-anomaly-pod-network-config` | Managed by `reconcileSystemAlertConfigMaps` from embedded asset |
| `TelcoHealthCheckHostReservedCPU` alert | `telco-anomaly-host-reserved-cpu-config` | Managed by `reconcileSystemAlertConfigMaps` from embedded asset |
| `TelcoHealthCheckOVSProcessCPU` alert | `telco-anomaly-ovs-process-cpu-config` | Managed by `reconcileSystemAlertConfigMaps` from embedded asset |
| `rds-compliance` periodic check | `telco-anomaly-rds-compliance-config` | Managed by `reconcileSystemPeriodicConfigMaps` from embedded asset |
| `low-latency-check` periodic check | `telco-anomaly-low-latency-check-config` | Managed by `reconcileSystemPeriodicConfigMaps` from embedded asset |

The four alert-type ConfigMaps carry label `ran.openshift.io/system-managed-alert: "true"`. The controller creates or updates them from the embedded asset YAML on every reconcile (when the corresponding spec boolean is true) and deletes them when disabled.

The periodic-check ConfigMaps (`telco-anomaly-rds-compliance-config` and `telco-anomaly-low-latency-check-config`) carry label `ran.openshift.io/system-managed-periodic: "true"` and follow the same create-or-update lifecycle, controlled by `spec.periodicHealthChecks.rdsCompliance.enabled` and `spec.periodicHealthChecks.lowLatencyCheck.enabled` respectively.

The `low-latency-check` ConfigMap configures the skill path `/skills/check-low-latency-configuration` from `quay.io/javierpena/telco-anomaly-skills:latest` and `mcpServers: []`.

**ConfigMap data keys (all ConfigMaps):**

| Key | Format | Maps to |
|---|---|---|
| `alertName` | String | Used by `resolveAlertConfigMap` to match incoming alert names |
| `alertGroupName` | String | Prometheus rule group name (system alerts only) |
| `alertRule` | YAML block scalar | The `- alert: ...` rule body included in Thanos rule groups |
| `alertMetrics` | JSON array string | Platform metric names added to `metrics_list.yaml` in the MCO allowlist |
| `alertUserWorkloadMetrics` | JSON array string (optional) | User-workload metric names added to `uwl_metrics_list.yaml` in the MCO allowlist |
| `request` | Plain string | `spec.request` in the AgenticRun |
| `skills` | JSON array of `{image, paths[]}` | `spec.tools.skills` |
| `mcpServers` | JSON array of `{name, url, timeoutSeconds?}` | `spec.tools.mcpServers` |

For optional AgenticRun tool fields, an absent key, empty string, or empty JSON array (`[]`) causes the corresponding field to be omitted from the AgenticRun spec. If `request` is empty the AgenticRun is skipped with an error log.

Both metric fields apply to system-managed and user-managed alerts. An omitted metric field, empty string, or `[]` contributes no metrics. `alertUserWorkloadMetrics` must otherwise be a JSON array of strings; JSON `null` and non-string entries are invalid. The shared parser (`parseAlertConfigMap`) rejects alert definitions with invalid metric-field data. Rule and allowlist listing log and skip these definitions, excluding both their Thanos rules and all their metrics.

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

The ConfigMap is created or updated on every reconcile. `reconcileAlertRules` lists alert ConfigMaps in the operator namespace with labels `ran.openshift.io/system-managed-alert: "true"` (always) and `ran.openshift.io/user-managed-alert: "true"` (when `spec.alerts.userAlerts` is true). It builds Prometheus rule groups from their `alertRule` fields using `alertGroupName` (or `telco-user-<alertname-lowercased>` for user alerts). The Thanos Ruler config-reload sidecar picks up changes automatically.

### Current alert rules

| Alert name | Spec flag | Group name | Asset file |
|---|---|---|---|
| `TelcoHealthCheckHostNetwork` | `spec.alerts.hostNetwork` | `telco-host-network` | `assets/alert-host-network.yaml` |
| `TelcoHealthCheckPodNetwork` | `spec.alerts.podNetwork` | `telco-pod-network` | `assets/alert-pod-network.yaml` |
| `TelcoHealthCheckHostReservedCPU` | `spec.alerts.hostReservedCPU` | `telco-host-reserved-cpu` | `assets/alert-host-reserved-cpu.yaml` |
| `TelcoHealthCheckOVSProcessCPU` | `spec.alerts.ovsProcessCPU` | `telco-ovs-process-cpu` | `assets/alert-ovs-process-cpu.yaml` |
| User-defined (any name) | `spec.alerts.userAlerts: true` | `alertGroupName` or `telco-user-<name>` | User ConfigMap |

### System-alert asset files

The four system alerts are defined in YAML files under `internal/controller/assets/`, embedded in the controller binary via `//go:embed` in `internal/controller/systemalerts.go`. Each file is a complete ConfigMap definition and can include the optional `alertUserWorkloadMetrics` field. The controller creates or updates the ConfigMap in the operator namespace on every reconcile when the spec boolean is enabled, and deletes it when disabled.

### User-defined alert ConfigMaps

When `spec.alerts.userAlerts: true`, the controller includes ConfigMaps in the operator namespace that carry both of the following labels:

```
app.kubernetes.io/managed-by: telco-anomaly-detection
ran.openshift.io/user-managed-alert: "true"
```

The same data fields apply, including optional `alertUserWorkloadMetrics` (see ConfigMap data keys table above). `alertGroupName` defaults to `telco-user-<alertname-lowercased>` when absent.

**Controller watch:** the controller watches ConfigMaps with both required user-alert labels (cluster-wide). Any create, update, or delete of a matching ConfigMap immediately re-enqueues the singleton `TelcoHealthcheck` CR (`telco-healthcheck`).

**Alert receiver:** `resolveAlertConfigMap` lists all ConfigMaps in the operator namespace with label `app.kubernetes.io/managed-by: telco-anomaly-detection` and matches by `data["alertName"]`, accepting both system-alert and user-alert labels. No static routing table is used.

**Example:**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-custom-alert-config
  namespace: telco-healthcheck-system
  labels:
    app.kubernetes.io/managed-by: telco-anomaly-detection
    ran.openshift.io/user-managed-alert: "true"
data:
  alertName: MyCustomAlert
  alertRule: |
    - alert: MyCustomAlert
      expr: my_custom_metric_total > 100 or my_application_metric_total > 100
      for: 5m
      labels:
        severity: warning
      annotations:
        cluster: '{{ $labels.cluster }}'
  alertMetrics: '["my_custom_metric_total"]'
  alertUserWorkloadMetrics: '["my_application_metric_total"]'
  request: "Investigate MyCustomAlert on cluster ${CLUSTER_NAME}."
  skills: "[]"
  mcpServers: "[]"
```

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
**Keys:** `metrics_list.yaml`, `uwl_metrics_list.yaml`

**Labels:** `app.kubernetes.io/managed-by: telco-anomaly-detection`

Applied on the hub cluster, this ConfigMap is picked up by MCO and propagated to the `metrics-collector` on every managed cluster, extending the set of metrics forwarded to the hub's Thanos Receive.

The ConfigMap content is computed by `buildMetricsListYAML` from the unified list of system-alert and user-alert ConfigMaps (same listing pattern as `reconcileAlertRules`). User-alert ConfigMaps contribute only when `spec.alerts.userAlerts` is true. Alert ownership is independent of metric source: either alert type can declare platform metrics, user-workload metrics, or both.

| Alert-definition field | Metric source | Destination key |
|---|---|---|
| `alertMetrics` | Platform monitoring | `metrics_list.yaml` |
| `alertUserWorkloadMetrics` (optional) | User-workload monitoring | `uwl_metrics_list.yaml` |

Metric names are deduplicated independently within each list across all alert configs, preserving first-seen order. A metric explicitly declared in both fields appears in both lists. Both keys are always managed, each written with `names: []` when no alerts provide metrics for that source. Reconciliation replaces both lists, clearing stale entries when metrics or alerts are removed or user alerts are disabled; unrelated ConfigMap data keys are preserved. On CR deletion the ConfigMap is removed.

For example, the alert definition above produces:

```yaml
data:
  metrics_list.yaml: |
    names:
      - my_custom_metric_total
  uwl_metrics_list.yaml: |
    names:
      - my_application_metric_total
```

### Current platform metric groups

| Metric | Comes from |
|---|---|
| `container_network_receive_errors_total` | `assets/alert-pod-network.yaml` (`spec.alerts.podNetwork`) |
| `container_network_receive_packets_dropped_total` | `assets/alert-pod-network.yaml` |
| `container_network_transmit_errors_total` | `assets/alert-pod-network.yaml` |
| `container_network_transmit_packets_dropped_total` | `assets/alert-pod-network.yaml` |
| `openshift:cpu_usage_cores:sum` | `assets/alert-host-reserved-cpu.yaml` (`spec.alerts.hostReservedCPU`) |
| `ovs_db_process_cpu_seconds_total` | `assets/alert-ovs-process-cpu.yaml` (`spec.alerts.ovsProcessCPU`) |
| `ovs_vswitchd_process_cpu_seconds_total` | `assets/alert-ovs-process-cpu.yaml` |

### Future enhancement: namespace-scoped collection

The current implementation collects these metrics across all namespaces on every spoke cluster. The MCO `names:` field does not support label or namespace selectors — it accepts only bare metric names.

Namespace-scoped collection is possible via the `recording_rules:` section of `metrics_list.yaml`, where the `expr:` field accepts full PromQL including `{namespace=~"ns1|ns2"}` label matchers. However, recording rules create a **new derived metric** on the spoke (named by the `record:` field) that is distinct from the original metric name. Any Thanos queries or alert expressions on the hub would need to reference the derived name.

A potential implementation, taking advantage of the now-required `spec.managedNamespaces` field:
- When `spec.managedNamespaces` is non-empty: replace `names:` with `recording_rules:`, one entry per metric, with `expr: <metric>{namespace=~"<joined-list>"}` and a `record:` name following the Prometheus convention (e.g. `container_network_receive_errors_total:managed_namespaces`).
- When `spec.managedNamespaces` is empty: `spec.alerts.podNetwork` would have nothing to scope to, so the metric list would remain empty (`names: []`) regardless of alert flags — consistent with the field's semantics that an empty namespace list means no monitoring is active.

---

## Spoke Cluster Access

The telco controller and receiver connect only to the hub. Install
lightspeed-operator, lightspeed-agentic-operator (CRD supporting
`spec.targetCluster`) and lightspeed-hub on the hub. Create `HubConfig/cluster`
with `spec.clusterRegistryMode: secret` (the checked-out hub revision does not
support `mce`). For each monitored ACM ManagedCluster, create a cluster-scoped
`SpokeCluster` with the same name, its `spec.apiServer`, and a
`spec.credentialSource.secret` referencing a kubeconfig supplied to lightspeed-hub.
Verify `Connected` and `Provisioned` and the resulting
`openshift-lightspeed/spoke-kubeconfig-<name>` Secret. lightspeed-hub and the
agentic operator, not the telco components, use that Secret to access spokes.
`Ready=True` also requires the hub's alert adapter prerequisites. No dependency
on `SpokeCluster` is added to telco-operator startup; failures after CREATE are
observable through hub AgenticRun status.

---

## Namespaces

| Namespace | Cluster | Purpose |
|---|---|---|
| `telco-healthcheck-system` | Hub | Operator pods, ServiceAccount, AgenticRun config ConfigMaps, TelcoHealthCheckRun records, optional purge CronJob |
| `open-cluster-management-observability` | Hub | MCO stack: Thanos Receive/Ruler/Store/Querier, AlertManager, `thanos-ruler-custom-rules` ConfigMap, `alertmanager-config` Secret, `observability-metrics-custom-allowlist` ConfigMap |
| `openshift-lightspeed` | Hub | AgenticRuns, AnalysisResults and lightspeed-hub standing kubeconfig Secrets (telco operator cannot read these Secrets) |
| `open-cluster-management-addon-observability` | Each spoke | MCO observability-addon and `metrics-collector` pods |
| `openshift-lightspeed-managed` | Each spoke | SAs and RBAC provisioned by lightspeed-hub/agentic operator |

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

### `config/manager/metrics-service.yaml`

ClusterIP Service `telco-anomaly-controller-metrics` in
`telco-healthcheck-system`, selecting controller pods and forwarding port
8080/TCP to their named `metrics` container port for `GET /metrics`.

### `config/manager/alertreceiver.yaml`

`Deployment` (`telco-anomaly-alert-receiver`) and `Service` (`telco-anomaly-alert-receiver`, port 8080) for the webhook receiver.

- Image: `quay.io/javierpena/telco-anomaly-alert-receiver:latest`
- Args: `--bind-address=:8080`
- Liveness/readiness: `GET /healthz`
- Resources: limits 200m CPU / 128Mi RAM; requests 50m CPU / 32Mi RAM

All AgenticRun config ConfigMaps (`telco-anomaly-host-network-config`, `telco-anomaly-pod-network-config`, `telco-anomaly-host-reserved-cpu-config`, `telco-anomaly-ovs-process-cpu-config`, `telco-anomaly-rds-compliance-config`, `telco-anomaly-low-latency-check-config`) are managed at runtime from embedded asset files in `internal/controller/assets/`. There is no separate deploy-time YAML for these ConfigMaps. The controller creates or updates each one on every reconcile based on the corresponding spec boolean; content comes from the embedded asset and is always kept in sync with the binary.

### `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml`

The CRD manifest for `TelcoHealthcheck`. Regenerated via `make manifests`.

### `config/crd/bases/ran.openshift.io_telcohealthcheckruns.yaml`

The namespaced `TelcoHealthCheckRun` CRD, also regenerated via `make manifests`.

### `config/rbac/`

| File | Resource |
|---|---|
| `serviceaccount.yaml` | `ServiceAccount` `telco-anomaly-operator` in `telco-healthcheck-system` |
| `role.yaml` | `ClusterRole` `telco-anomaly-operator` with the permissions below |
| `rolebinding.yaml` | `ClusterRoleBinding` binding the role to the ServiceAccount |
| `namespaced.yaml` | Roles and RoleBindings for hub run/results and Secrets in the three required namespaces |

**ClusterRole permissions summary:**

| Resource | Verbs |
|---|---|
| `telcohealthchecks` (ran.openshift.io) | get, list, watch, update, patch |
| `telcohealthchecks/status` | get, update, patch |
| `telcohealthchecks/finalizers` | update |
| `telcohealthcheckruns` and `/status` | get, list, watch, create, update, patch, delete (status: get, update, patch) |
| `cronjobs` (batch) | get, list, watch, create, update, patch, delete |
| `managedclusters` (cluster.open-cluster-management.io) | get, list, watch |
| `configmaps` | get, list, watch, create, update, patch, delete |
| `leases` (coordination.k8s.io) | get, list, watch, create, update, patch, delete |
| `events` | create, patch |
| `validatingwebhookconfigurations` (admissionregistration.k8s.io) | get |
| `serviceaccounts` | get, list, watch, create, update, patch, delete |
| `services` | get, list, watch, create, update, patch, delete |
| `deployments` (apps) | get, list, watch, create, update, patch, delete |
| `routes` (route.openshift.io) | get, list, watch, create, update, patch, delete |
| `clusterroles`, `clusterrolebindings` (rbac.authorization.k8s.io) | get, list, watch, create, update, patch, delete, escalate, bind |

**Namespaced permissions:** `openshift-lightspeed`: AgenticRuns
create/get/list/watch; AnalysisResults get/list/watch. `telco-healthcheck-system`:
Secrets create/delete/get/list/watch/update/patch (registry credentials and
outbound Alertmanager credentials). `open-cluster-management-observability`:
Secrets get/list/watch/update/patch (Alertmanager config). `openshift-config`:
Secrets get/list/watch (pull secret). The controller Secret cache is scoped to
these three namespaces; its run and result caches are scoped to
`openshift-lightspeed`. List/watch are required to start the corresponding
informers; the receiver uses the same ServiceAccount but an uncached client.

### `config/samples/ran_v1alpha1_telcohealthcheck.yaml`

Example `TelcoHealthcheck` CR for reference.

---

## Skills OCI Image

**Dockerfile:** `Dockerfile.skills`  
**Image:** `quay.io/javierpena/telco-anomaly-skills:latest`

Contains skill definition files under `skills/`. Each subdirectory is one skill. The image path(s) to load for each check type are configured in the corresponding AgenticRun config ConfigMap (`skills` key).

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
  ├── Reconcile system-periodic ConfigMaps (create/update from embedded asset)
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
  └── Create hub AgenticRun (openshift-lightspeed, targetCluster: spoke)
              └─► Hub Lightspeed executes agentic investigation against spoke


━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 PERIODIC AGENTIC RUN PATH
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Controller reconcile (period elapsed)
  ├── Look up AgenticRun config ConfigMap for check type
  ├── Load request / skills / mcpServers from ConfigMap
  └── For each monitored cluster:
        ├── Resolve ${VAR} placeholders (BuildVarMap → ExpandVariables)
        └── Create unique hub AgenticRun (openshift-lightspeed, targetCluster: spoke)
                    └─► Hub Lightspeed executes agentic investigation against spoke
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
