# Plan: Support the MultiCluster Observability Addon (MCOA)

> **Status:** Proposed design — not yet implemented. Scope, decisions, and file
> references below are the agreed baseline for a future implementation phase.

## 1. Motivation

ACM is introducing the **MultiCluster Observability Addon (MCOA)** as the new
form of MultiCluster Observability for metrics. MCOA replaces the classic
`metrics-collector` / endpoint-observability model with spoke-side **Prometheus
Agents**, and it changes *how* custom metrics and custom alerts are configured.
The telco anomaly detection operator currently assumes the **classic MCO**
model. To keep working on clusters that adopt MCOA, the operator must be able to
configure observability using *either* method.

This document plans:

1. A new `mcoAddon` boolean field on the `TelcoHealthcheck` CR.
2. Two parallel observability-configuration code paths (classic MCO and MCOA),
   selected by that field.
3. Documentation updates.

## 2. Classic MCO vs. MCOA — what actually differs

| Concern | Classic MCO (today) | MCOA (new) |
|---|---|---|
| Spoke collection | `metrics-collector` remote-writes **raw** metrics to hub Thanos Receive | Spoke **Prometheus Agent** federates + **downsamples** a curated subset (driven by `ScrapeConfig`s) |
| Custom metrics selection | `observability-metrics-custom-allowlist` ConfigMap (hub) | **`ScrapeConfig`** (`monitoring.rhobs/v1alpha1`) + **`PrometheusRule`**, referenced in the `ClusterManagementAddon` |
| Custom alert rules | `thanos-ruler-custom-rules` ConfigMap → evaluated **hub-side** by Thanos Ruler | **`PrometheusRule`** evaluated **spoke-side** by the spoke Prometheus |
| Alert delivery to hub AlertManager | Automatic (Thanos Ruler fires directly into hub AlertManager) | **Not automatic** — requires spoke `additionalAlertmanagerConfigs` pointing at the hub AlertManager, applied via an ACM **Policy** |
| Cluster identity on alerts | `cluster` label = ManagedCluster name | external label `managed_cluster` = `id.openshift.io` cluster **claim/ID** (not the name) |
| Cluster selection for config | Operator lists `ManagedCluster` + include/exclude | Config is attached to **Placements** referenced by the `ClusterManagementAddon` |
| Hub AlertManager + webhook receiver | Present | **Still present** — the forwarding target; the `alertmanager-config` receiver logic is unchanged |

Net effect: the `alertmanager-config` webhook receiver logic is essentially the
same under both models, but **alert-rule generation, metric-allowlist
generation, cluster selection, and cluster identity on incoming alerts all
change** under MCOA.

## 3. Design decisions (agreed)

1. **Field shape:** `spec.mcoAddon` is a plain `bool`, default `false`
   (classic MCO). `true` selects the MCOA path. Everything MCOA needs beyond
   the boolean (hub base domain, placement) is derived automatically — see below
   — so the user-facing switch stays a simple boolean as requested.
2. **Alert model under MCOA:** spoke-side `PrometheusRule` alerts **+ alert
   forwarding** to the hub AlertManager (the MCOA-native, documented model).
   We do **not** attempt hub-side Thanos Ruler evaluation under MCOA.
3. **Wiring automation under MCOA:** **full automation.** The operator patches
   the `ClusterManagementAddon` config references *and* creates/manages the
   spoke→hub alert-forwarding `Policy` + `PlacementBinding`.
4. **Cluster selection under MCOA:** the operator **creates/manages a
   `Placement`** derived from `spec.managedClusters` include/exclude, so MCOA
   cluster selection mirrors the classic path's behavior.
5. **Cluster identity mapping:** the operator resolves the MCOA
   `managed_cluster` cluster ID back to the ManagedCluster **name** so the alert
   receiver's cluster matching and AgenticRun targeting continue to work
   unchanged.

## 4. CR API change

File: `api/v1alpha1/telcohealthcheck_types.go`

Add to `TelcoHealthcheckSpec`:

```go
// MCOAddon selects how observability is configured.
//   false (default): classic MultiCluster Observability
//                    (thanos-ruler-custom-rules + observability-metrics-custom-allowlist).
//   true:            MultiCluster Observability Addon (MCOA)
//                    (ScrapeConfig + PrometheusRule + ClusterManagementAddon refs
//                     + spoke→hub alert-forwarding Policy).
// +optional
// +kubebuilder:default=false
MCOAddon bool `json:"mcoAddon,omitempty"`
```

Follow-ups: `make generate` (DeepCopy — no change needed for a scalar, but run
it) and `make manifests` (CRD YAML in `config/crd/bases/` and the sample in
`config/samples/`).

> Note on a possible struct: full automation needs a hub base domain and a
> placement. Both are derived automatically (base domain from the cluster
> `Ingress` config; placement generated from include/exclude), so a bare
> `bool` is sufficient and matches the requested UX. If future needs arise
> (e.g. referencing an externally-owned Placement), `mcoAddon` can be promoted
> to a struct without breaking the boolean semantics via a webhook/defaulting
> shim — called out here but **out of scope**.

## 5. Controller dispatch

File: `internal/controller/telcohealthcheck_controller.go`

The reconcile loop keeps its current step order. Steps **7 (alert rules)**,
**8 (alertmanager receiver)**, and **8a (metrics allowlist)** become
mode-aware. Introduce a single branch:

```
if thc.Spec.MCOAddon {
    reconcileObservabilityMCOA(...)   // new
} else {
    reconcileObservabilityClassic(...) // existing steps 7 + 8a
}
reconcileAlertManagerReceiver(...)     // shared by both modes (step 8)
```

Rationale for keeping `reconcileAlertManagerReceiver` shared: the hub
AlertManager and the webhook receiver exist in both models; only *how alerts
arrive* at it differs.

**Mode-switch cleanup:** when `mcoAddon` flips, the reconcile must remove the
resources belonging to the *other* mode so both configurations never coexist.
This is handled by always calling the inactive mode's cleanup before/append to
the active reconcile (see §10).

## 6. Classic MCO path (existing, unchanged)

No behavioral change. Existing functions remain:

- `reconcileAlertRules` — `internal/controller/alertrules.go`
  (writes `thanos-ruler-custom-rules`).
- `reconcileObservabilityMetrics` — `internal/controller/observabilitymetrics.go`
  (writes `observability-metrics-custom-allowlist`).
- `reconcileAlertManagerReceiver` — `internal/controller/alertmanager.go`.

They are gated behind `!spec.mcoAddon`.

## 7. MCOA path (new)

New file(s): `internal/controller/mcoa.go` (plus `mcoa_test.go`), and a few
helpers below. All new resources live in `open-cluster-management-observability`
on the hub unless noted.

### 7.1 Metrics → ScrapeConfig

Replace the allowlist ConfigMap with a `ScrapeConfig`
(`monitoring.rhobs/v1alpha1`) built from the same source of truth already used
by `buildMetricsListYAML`: the deduplicated `alertMetrics` across system-alert
and user-alert ConfigMaps (same listing pattern as `reconcileAlertRules`).

- Kind/Group: `ScrapeConfig` / `monitoring.rhobs/v1alpha1`.
- Name: e.g. `telco-anomaly-scrapeconfig`.
- Required label: `app.kubernetes.io/component: platform-metrics-collector`.
- `spec.jobName`, `spec.metricsPath: /federate`, and
  `spec.params.match[]` = `{__name__="<metric>"}` for each metric (and for any
  recording-rule output names we depend on — see §7.2).
- Namespace-scoping (`spec.managedNamespaces`) maps naturally to
  `match[]` label matchers, e.g. `{__name__="<m>",namespace=~"ns1|ns2"}`,
  finally addressing the "namespace-scoped collection" limitation noted in
  `docs/architecture.md`.

Created via the unstructured client (no Go types vendored for `monitoring.rhobs`).

### 7.2 Alerts → PrometheusRule (spoke-side)

Build a `PrometheusRule` from the same system/user alert ConfigMaps used today
by `reconcileAlertRules`, reusing the existing `alertRule` / `alertGroupName`
fields (the rule bodies are Prometheus rule YAML already).

- Kind/Group: `PrometheusRule`. Use `monitoring.coreos.com/v1` for rules
  federated from in-cluster CMO; use `monitoring.rhobs` when federating from COO
  (per the MCOA doc). Default to `monitoring.coreos.com/v1`; make the group a
  constant so it is easy to change.
- Required label: `app.kubernetes.io/component: platform-metrics-collector`.
- Name: e.g. `telco-anomaly-prometheusrule`.
- **Recording-rule dependency:** today's alert expressions reference MCO default
  recording rules (e.g.
  `instance:node_network_receive_drop_excluding_lo:rate1m`). Under MCOA these
  are *not* guaranteed to be collected. For each such dependency we either
  (a) add the recording rule to this same `PrometheusRule` so it is evaluated on
  the spoke, or (b) rewrite the alert `expr` to compute from raw metrics. This
  must be audited per system alert (`assets/alert-*.yaml`) during
  implementation. Track as a sub-task; it is the main correctness risk.
- **Cluster label injection:** so incoming alerts stay compatible with the alert
  receiver, inject a `cluster` label equal to the ManagedCluster name where
  feasible via rule/relabel config; where only the `managed_cluster` ID is
  available, the receiver-side mapping (§7.6) covers it.

### 7.3 Placement (cluster selection)

New helper (e.g. `internal/controller/placement.go`). Create/manage a
`Placement` (`cluster.open-cluster-management.io/v1beta1`) derived from
`spec.managedClusters`:

- Include → `predicates` selecting the listed ManagedCluster names.
- Exclude → predicate excluding them (all others selected).
- Namespace: a namespace with a `ManagedClusterSetBinding` (e.g.
  `open-cluster-management-global-set` for the `global` set, per the MCOA doc).
- Name: e.g. `telco-anomaly-placement`.

Owned and reconciled by the operator; deleted on cleanup / mode switch.

### 7.4 ClusterManagementAddon config references

New helper (e.g. `internal/controller/clustermanagementaddon.go`). Patch the
`ClusterManagementAddon` named `multicluster-observability-addon` so our
`ScrapeConfig` and `PrometheusRule` are referenced in the placement's `configs`
list (JSON-patch `add` as shown in the MCOA doc), scoped to our managed
Placement (§7.3).

- Must be idempotent: check for an existing matching config entry before adding;
  remove our entries on cleanup.
- Groups/resources: `{group: monitoring.rhobs, resource: scrapeconfigs}` and
  `{group: monitoring.coreos.com, resource: prometheusrules}` with our
  names/namespace.

### 7.5 Alert-forwarding Policy (spoke → hub AlertManager)

New helper (e.g. `internal/controller/alertforwarding.go`). Create/manage the
ACM `Policy` + `PlacementBinding` that injects `additionalAlertmanagerConfigs`
into the spoke `cluster-monitoring-config` (and, if user-workload alerts are
ever needed, `user-workload-monitoring-config`), pointing at the hub
AlertManager route — exactly the "Alert forwarding in MCOA" policy from the doc.

- **Hub base domain** (the doc's `INPUT_BASE_DOMAIN`): auto-detect from the hub
  cluster's `ingresses.config.openshift.io/cluster` (`.spec.domain`, strip the
  `apps.` prefix as needed). No new spec field.
- `remediationAction: enforce` (operator-managed; the doc's inform→enforce dance
  is a human workflow we bypass since we own the resource).
- Bind to our managed Placement (§7.3).
- Delete on cleanup / mode switch.

> This is the largest new surface. It requires new RBAC (§8) for
> `policy.open-cluster-management.io` Policy/PlacementBinding and for reading the
> cluster `Ingress` config.

### 7.6 Cluster ID → name mapping (alert receiver)

Under MCOA, forwarded alerts carry `managed_cluster` = `id.openshift.io` cluster
ID, not the ManagedCluster name. Plan:

- The controller already lists `ManagedCluster`s; each exposes the
  `id.openshift.io` value via `status.clusterClaims`. Build an **ID→name map**.
- Publish it so the alert receiver can consume it. Preferred: add a status field
  (e.g. `status.clusterIDMap` map[clusterID]name, or a slice of
  `{id,name}` pairs) written by the controller. Alternative: have the receiver
  resolve directly from `ManagedCluster` clusterClaims (it already has
  `managedclusters` list RBAC), avoiding a status change.
- **Alert receiver changes** (`internal/alertreceiver/handler.go`): when an
  alert lacks `labels.cluster` but has `labels.managed_cluster`, translate the
  ID to the name, then run the existing `monitoredClusters` validation and
  kubeconfig lookup unchanged. If `cluster` is present (classic path or injected
  per §7.2), behavior is unchanged.
- The `getDefinedAlertNames` check currently reads `thanos-ruler-custom-rules`.
  Under MCOA that ConfigMap does not exist; the receiver must instead derive
  valid alert names from the generated `PrometheusRule` (or, simpler, from the
  alert ConfigMaps in the operator namespace it already can list). Make the
  source mode-aware or unify on the operator-namespace ConfigMaps.

### 7.7 AlertManager receiver (shared)

`reconcileAlertManagerReceiver` is unchanged and runs in both modes. Under MCOA
it is what makes forwarded spoke alerts reach our webhook.

## 8. Scheme, RBAC, and detection

- **Scheme registration** (`cmd/controller/main.go`): the new kinds are created
  via the unstructured client, so no compiled type registration is strictly
  required, but register GVKs where helpful for watches.
- **RBAC** (`config/rbac/role.yaml`, regenerated via `make manifests` from
  kubebuilder markers on the reconcilers):
  - `monitoring.rhobs` `scrapeconfigs`, `prometheusrules`:
    get/list/watch/create/update/patch/delete.
  - `monitoring.coreos.com` `prometheusrules`: same verbs (if that group is
    chosen for rules).
  - `cluster.open-cluster-management.io` `placements`: full CRUD.
  - `addon.open-cluster-management.io` `clustermanagementaddons`:
    get/list/watch/update/patch.
  - `policy.open-cluster-management.io` `policies`, `placementbindings`:
    full CRUD.
  - `config.openshift.io` `ingresses`: get (base domain detection).
  - Alert receiver: reuse existing `managedclusters` list RBAC (for ID→name if
    not using status).
- **No runtime detection of MCOA vs classic** — the `mcoAddon` field is
  authoritative. (Optional future nicety: surface a condition if the field
  disagrees with the detected cluster state; out of scope.)

## 9. Assets

- The existing `assets/alert-*.yaml` ConfigMaps stay the single source of truth
  for both modes (their `alertRule`, `alertGroupName`, `alertMetrics` fields feed
  both the classic ConfigMaps and the MCOA ScrapeConfig/PrometheusRule).
- Add any spoke-side recording rules identified in §7.2 either into the asset
  `alertRule` blocks or a new companion field, so the MCOA `PrometheusRule`
  carries its dependencies.

## 10. Cleanup and mode switching

`cleanupResources` (deletion path) must remove **both** modes' resources
idempotently (ignore not-found):

- Classic: existing four steps (alertmanager receiver entry,
  `thanos-ruler-custom-rules`, `observability-metrics-custom-allowlist`,
  system-alert ConfigMaps) + kube-compare-mcp.
- MCOA (new): remove our `ScrapeConfig`, `PrometheusRule`, the
  `ClusterManagementAddon` config references, the `Placement`, and the
  alert-forwarding `Policy` + `PlacementBinding`.

On a **mode switch** during normal reconcile (not deletion), the active branch
must call the *inactive* mode's cleanup so stale config from the previous mode is
torn down.

## 11. Documentation updates

- `docs/architecture.md`:
  - New "MCOA mode" section paralleling "ACM Multicluster Observability
    Integration", documenting the two paths and the dispatch.
  - Update the CRD spec-fields table with `mcoAddon`.
  - Update the "MCO Custom Metrics Allowlist", "Thanos Alert Rules", and data-flow
    diagrams to show the MCOA variant (ScrapeConfig/PrometheusRule/CMAO/Policy).
  - Update the namespaces and RBAC tables.
- `config/samples/ran_v1alpha1_telcohealthcheck.yaml`: add a commented
  `mcoAddon: false` with a note.
- `AGENTS.md` / `CLAUDE.md`: mention the dual observability paths in the
  architecture summary.
- `steps.md`: add the phase entry for MCOA support.

## 12. Testing

- Unit tests mirroring existing `*_test.go` for each new reconciler
  (`mcoa_test.go`, `placement_test.go`, `clustermanagementaddon_test.go`,
  `alertforwarding_test.go`) using the fake client / unstructured objects.
- Table-driven tests for the `mcoAddon` dispatch: assert classic resources are
  created and MCOA resources absent when `false`, and vice-versa; assert
  mode-switch cleanup removes the other mode's resources.
- Alert receiver tests for the `managed_cluster` ID→name translation and the
  MCOA alert-name source.
- Keep `make test`, `make lint`, `make vet` green.

## 13. Risks / open items

1. **Recording-rule dependencies (§7.2)** are the main correctness risk — each
   system alert must be audited to ensure its `expr` resolves on the spoke under
   MCOA. Some may need rewriting.
2. **Hub Thanos Ruler under MCOA** is not documented as removed; we deliberately
   choose the documented spoke-side + forwarding model rather than relying on
   undocumented hub-side behavior.
3. **`ClusterManagementAddon` name/namespace** (`multicluster-observability-addon`)
   and the placement-set namespace are assumed from the doc; confirm against the
   target environment before implementation.
4. **Base-domain detection** heuristics (apps domain vs cluster domain) should be
   validated on a real hub.
5. **No Go types** for `monitoring.rhobs` / OCM addon / policy APIs are vendored;
   everything is unstructured — verify field names against the live CRDs.

## 14. Suggested implementation phasing

1. API field + dispatch scaffolding + docs stub (no behavior change when
   `false`).
2. MCOA metrics: `ScrapeConfig` + CMAO reference.
3. MCOA alerts: `PrometheusRule` (with recording-rule audit) + CMAO reference.
4. `Placement` management.
5. Alert-forwarding `Policy`/`PlacementBinding` + base-domain detection.
6. Alert receiver ID→name mapping + MCOA alert-name source.
7. Cleanup/mode-switch + full docs + tests.
