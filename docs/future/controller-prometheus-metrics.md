# Plan: Controller Prometheus metrics for managed clusters and run records

**Status:** Implemented. See `docs/architecture.md` for the deployed behavior.

## Motivation and existing infrastructure

The controller already serves controller-runtime Prometheus metrics at
`/metrics` on `:8080` (`cmd/controller/main.go`), and its Deployment already
declares a `metrics` container port (`config/manager/manager.yaml`). It does
not yet expose application-level counts or provide a Service for scraping that
port. Hub-side `TelcoHealthCheckRun` objects are namespaced and created in the
operator namespace (by default `telco-healthcheck-system`). The existing
controller ClusterRole already permits reading `TelcoHealthcheck` and listing
`TelcoHealthCheckRun` objects.

## Confirmed decisions

- Expose the existing HTTP metrics listener with an in-cluster Service; do not
  add a ServiceMonitor. Scrape discovery and configuration are handled outside
  this change.
- Records without `status.agenticRunStatus.phase` (nil status or empty phase)
  count in the `phase="Unknown"` series. Phase counts therefore sum to the
  total number of run records.
- Report gauges representing the objects' **current state**, not counters of
  historical create/update events. The series for a phase must disappear when
  no current records have that phase.

## Metric contract

All metrics are gauges with the `telco_healthcheck_` prefix. Only the phase
metric has a label; do not add run names or cluster names as labels.

| Metric | Value |
|---|---|
| `telco_healthcheck_managed_clusters` | Length of `status.monitoredClusters` on the canonical cluster-scoped `TelcoHealthcheck` named `telco-healthcheck`; zero if the CR does not exist. This measures clusters selected for monitoring, not every ACM `ManagedCluster`. |
| `telco_healthcheck_runs` | Number of current `TelcoHealthCheckRun` objects in the configured operator namespace. |
| `telco_healthcheck_runs_action_required` | Number of those records whose `status.agenticRunActionRequired` is exactly `"True"`; absent and `"False"` do not count. |
| `telco_healthcheck_runs_by_phase{phase="<value>"}` | Number of those records with the given `status.agenticRunStatus.phase`, using `"Unknown"` when the phase is missing or empty. Generate one series for each phase found in the current list; no series for phases absent from that list. |

The run counts include all current records in the operator namespace, even if
their cluster is no longer monitored; they represent hub audit records, not
the current spoke selection. The phase label uses the phase value as stored in
the object (including condition reasons), rather than a hard-coded phase enum.

## Implementation steps

### 1. Add a collector for current-state gauges

**Files:** new Go file(s) under `internal/controller/` (or a dedicated
`internal/metrics/` package if that better keeps the collector isolated).

1. Define fixed Prometheus gauge descriptors for the four metric families.
   Use a custom `prometheus.Collector` so the phase family can emit only labels
   present in the current list on each scrape. Avoid a long-lived `GaugeVec`
   that retains obsolete phase labels.
2. On collection, get the canonical `TelcoHealthcheck` and list
   `TelcoHealthCheckRun` records with `client.InNamespace(operatorNamespace)`
   using the manager's hub client/reader. Aggregate total, action-required,
   and phase counts from that list, then emit all gauges from the successfully
   gathered data. Missing canonical CR means zero managed clusters; other read
   or list errors must cause a scrape error rather than a false zero or a
   partially updated snapshot. Bound collection by an appropriate timeout so
   a failing API read cannot stall scraping indefinitely.
3. Keep collector state request-local: concurrent scrapes must not race or
   reuse old counts. Empty run lists still emit the two zero-valued run gauges;
   the phase family has no samples when there are no runs.

### 2. Register with the existing controller-runtime endpoint

**File:** `cmd/controller/main.go`.

After constructing the manager, instantiate and register the collector with
controller-runtime's metrics registry, passing its hub reader/client and the
configured `operatorNamespace`. Reuse the existing `--metrics-bind-address`
and `/metrics` endpoint; no second HTTP server is needed. Register only once
at startup and handle registration errors consistently with other startup
failures.

### 3. Add a metrics Service

**File:** new Service manifest in `config/manager/`.

Create a ClusterIP Service in `telco-healthcheck-system` selecting the
controller Deployment's `app: telco-anomaly-controller` pods. Name its port
`metrics` and target the existing named `metrics` container port (8080/TCP).
`make deploy` already applies `config/manager/`; no ServiceMonitor, route,
CRD, or RBAC change is required for this plan.

### 4. Tests and validation

**Files:** collector unit tests near its implementation; deployment-manifest
validation where practical.

- Gather against an isolated Prometheus registry backed by a fake Kubernetes
  client. Assert the exact names, gauge values, and labels with multiple run
  phases, a missing phase, and action-required values `"True"`, `"False"`, and
  empty. Confirm the phase sample counts sum to the total.
- Verify namespace isolation, a missing singleton, and zero runs. Remove the
  last record for one phase and gather again to ensure its series disappears.
- Simulate read/list failures and check that the scrape fails rather than
  returning misleading counts; exercise concurrent collection if needed by
  the chosen collector design.
- Verify the Service's selector and named target port match
  `config/manager/manager.yaml`. On a deployed hub, scrape through a
  port-forward to the metrics Service and compare the values with the current
  singleton and run records, including a phase change or record deletion.

Run `make test`, `make vet`, and `make build` after implementation.

### 5. Documentation

**File:** `docs/architecture.md`.

Document the application metric names and definitions, collection scope,
dynamic `phase` label and `Unknown` behavior, `/metrics` listener, and new
in-cluster Service. Describe that scrapes reflect the current hub objects and
that scrape failures surface read/list errors. No generated manifests are
expected to change.

## Implementation order

1. Collector and registry wiring.
2. Service manifest.
3. Collector tests and deployment/scrape validation.
4. Architecture documentation and final verification.
