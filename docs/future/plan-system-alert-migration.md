# Plan: Migrate system-defined alerts to ConfigMap-based approach

## Context

`docs/future/plan-user-defined-alerts.md` adds user-alert ConfigMaps alongside the existing
hardcoded system alert logic. This plan completes the migration: it replaces every hardcoded
alert rule, metric list, and AgenticRun config in Go code with the same ConfigMap mechanism,
using the label `ran.openshift.io/system-managed-alert: true`.

Content currently spread across three places — Go string templates in `alertrules.go`,
metric lists in `observabilitymetrics.go`, and pre-installed ConfigMaps in
`config/manager/agenticrun-configs.yaml` — consolidates into four YAML asset files embedded
in the controller binary via `//go:embed`. After this migration the operator has zero
hardcoded alert knowledge and the alert-rule/metrics reconciliation logic becomes fully generic.

**This plan must be implemented after `plan-user-defined-alerts.md`**, or both plans can be
combined into a single implementation pass, skipping the intermediate state.

---

## Decisions

- The four boolean spec fields (`hostNetwork`, `podNetwork`, `hostReservedCPU`,
  `ovsProcessCPU`) are **preserved** — they now control the lifecycle of the corresponding
  system-alert ConfigMap (create-or-update when `true`, delete when `false`) instead of
  branching Go code.
- System-alert content lives in YAML files under `internal/controller/assets/`, one per alert
  category, embedded with `//go:embed` (the same pattern used by `kubecomparemcp.go`).
- A new `alertGroupName` data field is added to the ConfigMap format. The controller uses it
  to preserve the existing Thanos group names (`telco-host-network`, etc.) on migration. For
  user-alert ConfigMaps, if the field is absent, the group name defaults to
  `telco-user-<alertname-lowercased>` (from `plan-user-defined-alerts.md`).
- **Behavior change**: `ensureAgenticRunConfigs` currently never overwrites existing ConfigMaps
  ("create-if-absent"). The new `reconcileSystemAlertConfigMaps` does a full create-or-update
  on every reconcile. Operators who customised the pre-installed AgenticRun ConfigMaps should
  migrate those customisations to user-alert ConfigMaps before upgrading.

---

## Step 1 — New YAML asset files

Create four files under `internal/controller/assets/`, following the same directory used by
`kube-compare-rbac.yaml` and `kube-compare-mcp.yaml`:

```
internal/controller/assets/alert-host-network.yaml
internal/controller/assets/alert-pod-network.yaml
internal/controller/assets/alert-host-reserved-cpu.yaml
internal/controller/assets/alert-ovs-process-cpu.yaml
```

**File format** (example — `alert-host-network.yaml`):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: telco-anomaly-host-network-config
  labels:
    app.kubernetes.io/managed-by: telco-anomaly-detection
    ran.openshift.io/system-managed-alert: "true"
data:
  alertName: TelcoHealthCheckHostNetwork
  alertGroupName: telco-host-network
  alertRule: |
    - alert: TelcoHealthCheckHostNetwork
      expr: |
        sum by (clusterID, cluster, instance, prometheus)
          (instance:node_network_receive_drop_excluding_lo:rate1m > 1)
        or sum by (clusterID, cluster, instance, prometheus)
          (instance:node_network_transmit_drop_excluding_lo:rate1m > 1)
      for: 1m
      labels:
        severity: warning
      annotations:
        cluster: '{{ $labels.cluster }}'
        node: '{{ $labels.instance }}'
  alertMetrics: ""
  request: |
    <content from config/manager/agenticrun-configs.yaml — telco-anomaly-host-network-config>
  skills: "[]"
  mcpServers: "[]"
```

**Content sources** (extract verbatim, nothing is invented):

| Asset file | `alertRule` source | `alertMetrics` source | `request`/`skills`/`mcpServers` source |
|---|---|---|---|
| `alert-host-network.yaml` | `hostNetworkRuleGroup()` in `alertrules.go` | none | `telco-anomaly-host-network-config` in `agenticrun-configs.yaml` |
| `alert-pod-network.yaml` | `podNetworkRuleGroup()` | 4 `container_network_*` metrics from `observabilitymetrics.go` | `telco-anomaly-pod-network-config` |
| `alert-host-reserved-cpu.yaml` | `hostReservedCPURuleGroup()` | `openshift:cpu_usage_cores:sum` | `telco-anomaly-host-reserved-cpu-config` |
| `alert-ovs-process-cpu.yaml` | `ovsProcessCPURuleGroup()` | 2 `ovs_*` metrics | `telco-anomaly-ovs-process-cpu-config` |

---

## Step 2 — New file: `internal/controller/systemalerts.go`

### Embed the asset files

```go
//go:embed assets/alert-host-network.yaml
var alertHostNetworkYAML []byte

//go:embed assets/alert-pod-network.yaml
var alertPodNetworkYAML []byte

//go:embed assets/alert-host-reserved-cpu.yaml
var alertHostReservedCPUYAML []byte

//go:embed assets/alert-ovs-process-cpu.yaml
var alertOVSProcessCPUYAML []byte
```

### Mapping: asset → spec boolean

```go
type systemAlertAsset struct {
    yaml    []byte
    enabled func(ranv1alpha1.AlertsSpec) bool
}

var systemAlertAssets = []systemAlertAsset{
    {yaml: alertHostNetworkYAML,     enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.HostNetwork }},
    {yaml: alertPodNetworkYAML,      enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.PodNetwork }},
    {yaml: alertHostReservedCPUYAML, enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.HostReservedCPU }},
    {yaml: alertOVSProcessCPUYAML,   enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.OVSProcessCPU }},
}
```

### Function `reconcileSystemAlertConfigMaps`

```go
func reconcileSystemAlertConfigMaps(ctx context.Context, c client.Client, namespace string, alerts ranv1alpha1.AlertsSpec) error
```

For each entry in `systemAlertAssets`:
1. Decode the embedded YAML into a `*corev1.ConfigMap` using `utilyaml.NewYAMLOrJSONDecoder`
   (already imported by `kubecomparemcp.go`).
2. Set `cm.Namespace = namespace`.
3. If `enabled(alerts)` is `true`: Get the existing ConfigMap; Create if NotFound, Update otherwise.
4. If `enabled(alerts)` is `false`: Get and Delete if it exists; ignore NotFound.

### Function `cleanupSystemAlertConfigMaps`

```go
func cleanupSystemAlertConfigMaps(ctx context.Context, c client.Client, namespace string) error
```

Deletes all four system-alert ConfigMaps unconditionally (ignores NotFound). Called from
`cleanupResources` on CR deletion.

---

## Step 3 — Extend `UserAlertConfig` with `GroupName`

**File:** `internal/controller/useralerts.go` (created by the user-alerts plan)

Add `GroupName string` field. Update `listUserAlertConfigs` to populate it from the
`alertGroupName` data key; leave it empty when absent (the rendering code applies the default).

---

## Step 4 — Trim `internal/controller/agenticrunconfig.go`

Remove the four alert ConfigMap names from `agenticRunConfigMapNames`, leaving only
`"telco-anomaly-rds-compliance-config"`. The function continues to manage only the RDS
compliance ConfigMap with its existing create-if-absent semantics.

---

## Step 5 — Trim `config/manager/agenticrun-configs.yaml`

Remove the four alert ConfigMap definitions (host-network, pod-network, host-reserved-cpu,
ovs-process-cpu). Keep only `telco-anomaly-rds-compliance-config`. The removed ConfigMaps are
now managed by `reconcileSystemAlertConfigMaps`; pre-installing them via kustomize is no
longer needed or desirable (kustomize-installed ConfigMaps lack the
`ran.openshift.io/system-managed-alert` label and would not be picked up by generic listing).

---

## Step 6 — Rewrite `internal/controller/alertrules.go` to be fully generic

Remove: `hostNetworkRuleGroup`, `podNetworkRuleGroup`, `hostReservedCPURuleGroup`,
`ovsProcessCPURuleGroup`, and the per-alert branching in `buildCustomRulesYAML`.

New `buildCustomRulesYAML(allAlerts []UserAlertConfig) string`:

Iterates all provided configs. For each entry uses `GroupName` when set, otherwise derives
`telco-user-<alertname-lowercased>`. Returns `"groups: []\n"` for an empty slice.

New `reconcileAlertRules(ctx, c, namespace, userAlertsEnabled bool) error`:

- Lists ConfigMaps with `ran.openshift.io/system-managed-alert: "true"` in `namespace` (always).
- When `userAlertsEnabled`: also lists ConfigMaps with `ran.openshift.io/user-managed-alert: "true"`.
- Converts both lists to `[]UserAlertConfig` using a shared `parseAlertConfigMap` helper
  (extracted from the internals of `listUserAlertConfigs`).
- Passes the unified slice to `buildCustomRulesYAML` and applies the result to
  `thanos-ruler-custom-rules` in `open-cluster-management-observability`.

`cleanupAlertRules` is unchanged.

---

## Step 7 — Rewrite `internal/controller/observabilitymetrics.go` to be fully generic

Remove per-alert metric blocks from `buildMetricsListYAML`. New signature:

```go
func buildMetricsListYAML(allAlerts []UserAlertConfig) string
```

Iterates all configs, deduplicates metric names, returns `"names: []\n"` if no metrics.

New `reconcileObservabilityMetrics(ctx, c, namespace, userAlertsEnabled bool) error`:

Same list-and-unify pattern as the updated `reconcileAlertRules`.

`cleanupObservabilityMetrics` is unchanged.

---

## Step 8 — Update the reconcile loop

**File:** `internal/controller/telcohealthcheck_controller.go`

Replace the current `ensureAgenticRunConfigs` call (line 92) with two calls in order
(system ConfigMaps must exist before the rules listing in the next step reads them):

```go
if err := reconcileSystemAlertConfigMaps(ctx, r.Client, r.OperatorNamespace, thc.Spec.Alerts); err != nil {
    logger.Error(err, "failed to reconcile system alert ConfigMaps")
    return ctrl.Result{}, err
}
if err := ensureAgenticRunConfigs(ctx, r.Client, r.OperatorNamespace); err != nil {
    logger.Error(err, "failed to ensure RDS compliance config ConfigMap")
    return ctrl.Result{}, err
}
```

Update `reconcileAlertRules` and `reconcileObservabilityMetrics` calls to the new signature
`(ctx, r.Client, r.OperatorNamespace, thc.Spec.Alerts.UserAlerts)`.

---

## Step 9 — Add system-alert cleanup to `cleanupResources`

**File:** `internal/controller/telcohealthcheck_controller.go`

Add to `cleanupResources`:

```go
if err := cleanupSystemAlertConfigMaps(ctx, r.Client, r.OperatorNamespace); err != nil {
    logger.Error(err, "failed to cleanup system alert ConfigMaps")
    if firstErr == nil { firstErr = err }
}
```

---

## Step 10 — Alert receiver: remove the static map

**File:** `internal/alertreceiver/handler.go`

Remove the `alertConfigMaps` package-level variable (lines 37–42).

Update `resolveAlertConfigMap` (introduced in the user-alerts plan) to search both
system-alert and user-alert ConfigMaps in a single pass — no static-map fast path:

```go
func resolveAlertConfigMap(ctx context.Context, c client.Client, alertName, namespace string) (string, error) {
    var cms corev1.ConfigMapList
    if err := c.List(ctx, &cms, client.InNamespace(namespace),
        client.MatchingLabels{"app.kubernetes.io/managed-by": "telco-anomaly-detection"},
    ); err != nil {
        return "", err
    }
    for _, cm := range cms.Items {
        isSystem := cm.Labels["ran.openshift.io/system-managed-alert"] == "true"
        isUser   := cm.Labels["ran.openshift.io/user-managed-alert"]   == "true"
        if (isSystem || isUser) && cm.Data["alertName"] == alertName {
            return cm.Name, nil
        }
    }
    return "", fmt.Errorf("no AgenticRun config found for alert %q", alertName)
}
```

The Thanos-validation gate (checking `thanos-ruler-custom-rules`) already ensures only alerts
whose rules exist can reach this function.

---

## Step 11 — Update documentation

### `docs/adding-a-new-alert.md` — complete rewrite

The document currently describes the old five-file manual process. After this migration it
should describe two workflows:

**Adding a new system alert** (operator developer workflow — requires a code change and release):

1. Create a new asset YAML file in `internal/controller/assets/alert-<name>.yaml` with all
   six data fields (`alertName`, `alertGroupName`, `alertRule`, `alertMetrics`,
   `request`, `skills`, `mcpServers`).
2. Add a `//go:embed` variable and a `systemAlertAsset` entry in `systemalerts.go`.
3. Add a `bool` field to `AlertsSpec` in `api/v1alpha1/telcohealthcheck_types.go`.
4. Run `make generate && make manifests`.
5. Add the field to the sample CR and to `docs/architecture.md`.

No changes to `alertrules.go`, `observabilitymetrics.go`, `handler.go`, or
`agenticrun-configs.yaml` are required.

**Adding a user-defined alert** (operator workflow — no code change, no release):

Describe the ConfigMap format (`alertName`, `alertGroupName`, `alertRule`, `alertMetrics`,
`request`, `skills`, `mcpServers`) and the required labels
(`app.kubernetes.io/managed-by: telco-anomaly-detection`,
`ran.openshift.io/user-managed-alert: "true"`), and enable `spec.alerts.userAlerts: true`.

Include the updated pipeline diagram showing the unified ConfigMap-based flow (no
`alertConfigMaps` static map, no hardcoded rule helpers).

### `docs/architecture.md`

Update the sections that describe:
- Thanos alert rule generation (now driven by listing system/user ConfigMaps, not hardcoded Go).
- MCO metrics allowlist (same).
- AgenticRun config lookup (dynamic, label-based, replaces the static map).
- The `internal/controller/assets/` directory and the `//go:embed` pattern.
- The system-alert ConfigMap lifecycle (created/updated/deleted by the controller based on
  spec booleans).
- The `alertGroupName` field and its defaulting rule.

---

## Files changed summary

| File | Change |
|---|---|
| `internal/controller/assets/alert-host-network.yaml` | **New** — embedded asset |
| `internal/controller/assets/alert-pod-network.yaml` | **New** — embedded asset |
| `internal/controller/assets/alert-host-reserved-cpu.yaml` | **New** — embedded asset |
| `internal/controller/assets/alert-ovs-process-cpu.yaml` | **New** — embedded asset |
| `internal/controller/systemalerts.go` | **New** — embed directives, asset mapping, `reconcileSystemAlertConfigMaps`, `cleanupSystemAlertConfigMaps` |
| `internal/controller/useralerts.go` | Add `GroupName` field; populate from `alertGroupName` key |
| `internal/controller/agenticrunconfig.go` | Shrink `agenticRunConfigMapNames` to RDS-compliance only |
| `internal/controller/alertrules.go` | Remove four hardcoded helpers; generic `buildCustomRulesYAML` + updated `reconcileAlertRules` |
| `internal/controller/observabilitymetrics.go` | Remove per-alert metric blocks; generic functions |
| `internal/controller/telcohealthcheck_controller.go` | Add `reconcileSystemAlertConfigMaps` call; updated signatures; add cleanup |
| `internal/alertreceiver/handler.go` | Remove `alertConfigMaps` static map; unified dynamic lookup |
| `config/manager/agenticrun-configs.yaml` | Remove four alert ConfigMap definitions; keep RDS only |
| `docs/adding-a-new-alert.md` | Complete rewrite for asset-based system alerts + user alert workflow |
| `docs/architecture.md` | Update alert rule/metrics/lookup sections; document asset pattern |
| `steps.md` | Update phase tracker |

---

## Verification

1. **Unit tests**:
   - `TestReconcileSystemAlertConfigMaps` — toggle each boolean; assert ConfigMap created,
     updated to latest asset content, and deleted correctly.
   - `TestBuildCustomRulesYAML_Generic` — pass a mix of system (with `GroupName`) and user
     (without `GroupName`) configs; assert group name derivation and YAML indentation.
   - `TestBuildMetricsListYAML_Generic` — assert deduplication and empty-list case.
   - `TestResolveAlertConfigMap_Unified` — system-label match; user-label match; neither returns error.

2. **Integration (manual)**:
   - Deploy with `hostNetwork: true`, `podNetwork: false`. Verify only the host-network system
     ConfigMap exists in `telco-healthcheck-system` with label `system-managed-alert: "true"`;
     only its rule group appears in `thanos-ruler-custom-rules`.
   - Toggle `podNetwork: true`. Verify the pod-network ConfigMap is created and its rule added.
   - Simulate a webhook POST for `TelcoHealthCheckHostNetwork`. Verify `resolveAlertConfigMap`
     finds the system-alert ConfigMap and an `AgenticRun` is created on the spoke cluster.
   - Delete the `TelcoHealthcheck` CR. Verify all four system ConfigMaps are deleted; RDS
     compliance ConfigMap persists.
   - Confirm Thanos group names are unchanged (`telco-host-network`, etc.) — no alert-routing
     rule breakage on upgrade.
