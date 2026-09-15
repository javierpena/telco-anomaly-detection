# Plan: User-defined alert rules via labeled ConfigMaps

## Context

The operator currently supports four hardcoded alert categories (`hostNetwork`, `podNetwork`,
`hostReservedCPU`, `ovsProcessCPU`). Users need a way to inject custom Thanos alert rules,
MCO metrics, and AgenticRun prompts without modifying the operator code. This plan introduces
a `userAlerts` flag in the spec and a ConfigMap-based extension mechanism the controller and
alert receiver both consume.

---

## Decisions locked in

- **alertRule format**: the complete Prometheus rule object(s) including `- alert: <name>`.  
  The separate `alertName` field is a required index key; it **must** match the `alert:` name
  inside `alertRule`.
- **alertMetrics format**: newline-separated metric names (one per line).
- **Group name**: controller auto-derives it as `telco-user-<alertname-lowercased>`.
- **Controller watch**: a ConfigMap watch with a label predicate is added so any create/update/delete
  of a user-alert ConfigMap re-enqueues all `TelcoHealthcheck` CRs immediately.

---

## Step 1 — CRD: add `userAlerts` field

**File:** `api/v1alpha1/telcohealthcheck_types.go`

Add to `AlertsSpec`:

```go
// UserAlerts enables processing of user-defined alert ConfigMaps.
UserAlerts bool `json:"userAlerts"`
```

After editing, run:

```bash
make generate   # regenerates zz_generated.deepcopy.go
make manifests  # regenerates config/crd/bases/ and config/rbac/
```

---

## Step 2 — New helper: `internal/controller/useralerts.go`

Create this file with:

### Struct

```go
type UserAlertConfig struct {
    AlertName     string
    AlertRule     string   // complete rule YAML block including "- alert: name"
    AlertMetrics  []string // parsed metric names from alertMetrics (may be empty)
    ConfigMapName string
}
```

### Label constants

```go
const (
    userAlertManagedByLabel = "app.kubernetes.io/managed-by"
    userAlertManagedByValue = "telco-anomaly-detection"
    userAlertLabel          = "ran.openshift.io/user-managed-alert"
    userAlertLabelValue     = "true"
)
```

### Function `listUserAlertConfigs`

```go
func listUserAlertConfigs(ctx context.Context, c client.Client, namespace string) ([]UserAlertConfig, error)
```

- Lists `corev1.ConfigMap` in `namespace` filtered by both labels (use `client.MatchingLabels`).
- For each ConfigMap:
  - Validates `alertName` and `alertRule` are non-empty; logs a warning and skips if either is missing.
  - Parses `alertMetrics` by splitting on newlines, stripping whitespace and empty lines.
- Returns `[]UserAlertConfig`.

---

## Step 3 — Controller: extend Thanos alert rules reconciliation

**File:** `internal/controller/alertrules.go`

### Signature change for `buildCustomRulesYAML`

```go
func buildCustomRulesYAML(alerts AlertsSpec, userAlerts []UserAlertConfig) string
```

Add a new case after the four existing system groups: when `alerts.UserAlerts` is true, iterate
`userAlerts` and append:

```go
fmt.Sprintf("- name: telco-user-%s\n  rules:\n  %s\n",
    strings.ToLower(ua.AlertName),
    strings.ReplaceAll(strings.TrimSpace(ua.AlertRule), "\n", "\n  "))
```

(Indent the `alertRule` content two spaces so it nests under `rules:`.)

### Signature change for `reconcileAlertRules`

```go
func reconcileAlertRules(ctx context.Context, c client.Client, spec TelcoHealthcheckSpec, userAlerts []UserAlertConfig) error
```

Pass `userAlerts` through to `buildCustomRulesYAML`.

---

## Step 4 — Controller: extend MCO metrics allowlist

**File:** `internal/controller/observabilitymetrics.go`

### Signature change for `buildMetricsListYAML`

```go
func buildMetricsListYAML(alerts AlertsSpec, userAlerts []UserAlertConfig) string
```

After existing metric blocks, when `alerts.UserAlerts` is true, append each non-empty
`ua.AlertMetrics` entry as `  - <metric>\n`.

### Signature change for `reconcileObservabilityMetrics`

```go
func reconcileObservabilityMetrics(ctx context.Context, c client.Client, alerts AlertsSpec, userAlerts []UserAlertConfig) error
```

---

## Step 5 — Controller: main reconcile loop

**File:** `internal/controller/telcohealthcheck_controller.go`

In `Reconcile`, after resolving monitored clusters and before the existing reconcile calls:

```go
var userAlerts []UserAlertConfig
if thc.Spec.Alerts.UserAlerts {
    userAlerts, err = listUserAlertConfigs(ctx, r.Client, req.Namespace)
    if err != nil {
        return ctrl.Result{}, fmt.Errorf("listing user alert configs: %w", err)
    }
}
```

Pass `userAlerts` to the updated `reconcileAlertRules` and `reconcileObservabilityMetrics` calls.

---

## Step 6 — Controller: ConfigMap watch in `SetupWithManager`

**File:** `internal/controller/telcohealthcheck_controller.go`

Add a new `Watches` call (pattern mirrors the existing `ManagedCluster` watch):

```go
Watches(
    &corev1.ConfigMap{},
    handler.EnqueueRequestsFromMapFunc(r.mapUserAlertCMToTHC),
    builder.WithPredicates(predicate.NewPredicateFuncs(isUserAlertConfigMap)),
)
```

Add helpers in `telcohealthcheck_controller.go` (or `useralerts.go`):

```go
// isUserAlertConfigMap returns true when the object carries both required labels.
func isUserAlertConfigMap(obj client.Object) bool { ... }

// mapUserAlertCMToTHC lists all TelcoHealthcheck CRs and returns a reconcile.Request for each.
func (r *TelcoHealthcheckReconciler) mapUserAlertCMToTHC(ctx context.Context, _ client.Object) []reconcile.Request { ... }
```

---

## Step 7 — Alert receiver: dynamic lookup for user-defined alerts

**File:** `internal/alertreceiver/handler.go`

### Alert name validation — no change needed

`getDefinedAlertNames` already parses the live `thanos-ruler-custom-rules` ConfigMap. Because the
controller writes user alert rules there, user-defined alert names are already validated for free.

### AgenticRun config lookup — extend `createAgenticRunOnCluster`

Current code does:
```go
configMapName := alertConfigMaps[alertName]  // static map
```

Replace with a helper:

```go
configMapName, err := resolveAlertConfigMap(ctx, h.Client, alertName, operatorNamespace)
```

```go
func resolveAlertConfigMap(ctx context.Context, c client.Client, alertName, namespace string) (string, error) {
    // 1. Check static map first.
    if name, ok := alertConfigMaps[alertName]; ok {
        return name, nil
    }
    // 2. Fall back to user-alert ConfigMaps.
    var cms corev1.ConfigMapList
    if err := c.List(ctx, &cms, client.InNamespace(namespace), client.MatchingLabels{
        userAlertManagedByLabel: userAlertManagedByValue,
        userAlertLabel:          userAlertLabelValue,
    }); err != nil {
        return "", err
    }
    for _, cm := range cms.Items {
        if cm.Data["alertName"] == alertName {
            return cm.Name, nil
        }
    }
    return "", fmt.Errorf("no AgenticRun config found for alert %q", alertName)
}
```

The label constants can be imported from the controller package or duplicated as package-level
constants in the alert receiver. The `agenticrun.LoadRunConfig` function already ignores unknown
keys (`alertName`, `alertRule`, `alertMetrics`), so no changes there.

---

## Step 8 — Documentation

- **`docs/architecture.md`**: document the user-alert ConfigMap format (fields, labels, group naming),
  how the controller discovers and includes them, and how the receiver resolves them dynamically.
- **`steps.md`**: mark this capability as implemented.

---

## Files changed summary

| File | Change |
|---|---|
| `api/v1alpha1/telcohealthcheck_types.go` | Add `UserAlerts bool` to `AlertsSpec` |
| `internal/controller/useralerts.go` | **New** — `UserAlertConfig` struct + `listUserAlertConfigs` |
| `internal/controller/alertrules.go` | Extend `buildCustomRulesYAML` and `reconcileAlertRules` |
| `internal/controller/observabilitymetrics.go` | Extend `buildMetricsListYAML` and `reconcileObservabilityMetrics` |
| `internal/controller/telcohealthcheck_controller.go` | Call `listUserAlertConfigs`, add ConfigMap watch |
| `internal/alertreceiver/handler.go` | Add `resolveAlertConfigMap` dynamic lookup |
| `config/crd/bases/*.yaml` | Regenerated by `make manifests` |
| `docs/architecture.md` | Document new mechanism |
| `steps.md` | Update phase tracker |

---

## Verification

1. **Unit tests**:
   - `TestListUserAlertConfigs` — mock ConfigMap list with valid and invalid entries, assert parsing.
   - `TestBuildCustomRulesYAML_UserAlerts` — assert group YAML is correctly generated and indented.
   - `TestBuildMetricsListYAML_UserAlerts` — assert user metrics appear in output.
   - `TestResolveAlertConfigMap` — assert static map takes precedence; user ConfigMap matched by `alertName`.

2. **Integration (manual)**:
   - Create a user-alert ConfigMap in `telco-healthcheck-system` with the two required labels.
   - Set `spec.alerts.userAlerts: true` in the `TelcoHealthcheck` CR.
   - Verify `thanos-ruler-custom-rules` contains the new rule group.
   - Verify `observability-metrics-custom-allowlist` contains any declared metrics.
   - Simulate a webhook POST with the user-defined `alertname`; verify an `AgenticRun` is created
     on the spoke cluster using the user-provided `request`/`skills`/`mcpServers`.
   - Modify the user-alert ConfigMap without touching the CR; verify reconciliation fires
     (check controller logs for a new reconcile event within seconds).
