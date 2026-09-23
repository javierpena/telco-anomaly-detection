# Adding a New Alert

There are two workflows depending on whether the alert is a **system alert** (part of the operator release) or a **user-defined alert** (added at runtime without a code change).

---

## Overview of the pipeline

```
TelcoHealthcheck CR (spec.alerts.<flag>: true)
        │
        ▼  controller reconcile
System-alert ConfigMaps (embedded assets) ──► reconcileAlertRules ──► Thanos Ruler ConfigMap
        │                                                                       │
        │                                                        alert fires ──►│
        │                                                                       ▼
        │                                                              AlertManager
        │                                                                       │
        │                                                              POST /webhook
        │                                                                       │
        └─── resolveAlertConfigMap (label-based lookup) ────────────────────────┘
                        │
                        ▼
                AgenticRun config (from the same CM)
                        │
                        ▼
                AgenticRun created on spoke cluster
```

---

## Workflow 1: Adding a system alert (operator developer — requires a release)

A system alert is packaged with the operator. Its full definition (rule, metrics, AgenticRun prompt) lives in a single YAML asset file embedded in the controller binary.

### Step 1 — Create the asset YAML file

Create `internal/controller/assets/alert-<name>.yaml` with all seven data fields:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: telco-anomaly-<name>-config
  labels:
    app.kubernetes.io/managed-by: telco-anomaly-detection
    ran.openshift.io/system-managed-alert: "true"
data:
  alertName: TelcoHealthCheck<Name>
  alertGroupName: telco-<name>
  alertRule: |
    - alert: TelcoHealthCheck<Name>
      expr: <prometheus_expression>
      for: 1m
      labels:
        severity: warning
      annotations:
        cluster: '{{ $labels.cluster }}'
  alertMetrics: '["metric_one_total","metric_two_total"]'
  request: |
    You are an experienced OpenShift administrator. Alert TelcoHealthCheck<Name>
    has been raised for this cluster. <investigation instructions for Lightspeed>
  skills: "[]"
  mcpServers: "[]"
```

**Data field notes:**

| Key | Format | Notes |
|---|---|---|
| `alertName` | String | Must match the `alert:` value in `alertRule`; used by the alert receiver to locate this CM |
| `alertGroupName` | String | Prometheus rule group name; must be unique across all system alerts |
| `alertRule` | YAML block scalar | The `- alert: ...` block (without the `groups:` wrapper) |
| `alertMetrics` | JSON array string | Metrics to add to the MCO allowlist; `"[]"` if none needed |
| `request` | String | Lightspeed prompt; supports `${CLUSTER_NAME}`, `${NODE_NAME}`, `${KUBE_COMPARE_MCP_URL}` |
| `skills` | JSON array string | OCI skills image config; `"[]"` if none |
| `mcpServers` | JSON array string | MCP server config; `"[]"` if none |

### Step 2 — Wire the asset in `systemalerts.go`

In `internal/controller/systemalerts.go`:

1. Add an embed variable:
   ```go
   //go:embed assets/alert-<name>.yaml
   var alert<Name>YAML []byte
   ```

2. Add an entry to `systemAlertAssets`:
   ```go
   {yaml: alert<Name>YAML, enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.<Name> }},
   ```

### Step 3 — Add a spec field to the API type

In `api/v1alpha1/telcohealthcheck_types.go`, add a boolean to `AlertsSpec`:

```go
type AlertsSpec struct {
    HostNetwork     bool `json:"hostNetwork"`
    // … existing fields …
    MyNewAlert      bool `json:"myNewAlert"`
}
```

Then regenerate generated code:

```bash
make generate   # regenerates zz_generated.deepcopy.go
make manifests  # regenerates config/crd/bases/ and config/rbac/
```

### Step 4 — Update the sample CR and architecture doc

- Add `myNewAlert: false` to the `alerts` block in `config/samples/ran_v1alpha1_telcohealthcheck.yaml`.
- Update `docs/architecture.md`: add a row for the new alert in the system alert tables.

### What you do NOT need to change

After this migration, adding a system alert requires **no changes** to:
- `alertrules.go` or `observabilitymetrics.go` (fully generic)
- `handler.go` (label-based lookup)
- `agenticrun-configs.yaml` (the asset file is the config)

### Checklist — system alert

| # | File | What to change |
|---|---|---|
| 1 | `internal/controller/assets/alert-<name>.yaml` | New asset file |
| 2 | `internal/controller/systemalerts.go` | Add embed var + asset entry |
| 3 | `api/v1alpha1/telcohealthcheck_types.go` | Add `bool` field to `AlertsSpec` |
| 3 | Run `make generate && make manifests` | Regenerate DeepCopy and CRD |
| 4 | `config/samples/ran_v1alpha1_telcohealthcheck.yaml` | Add field to sample CR |
| 4 | `docs/architecture.md` | Update alert tables |

---

## Workflow 2: Adding a user-defined alert (operator administrator — no code change, no release)

User-defined alerts are defined at runtime via a labeled ConfigMap in the operator namespace (`telco-healthcheck-system`). No operator restart is needed; the next reconcile picks them up automatically.

### Required labels

```yaml
labels:
  app.kubernetes.io/managed-by: telco-anomaly-detection
  ran.openshift.io/user-managed-alert: "true"
```

### Required data fields

```yaml
data:
  alertName: MyCustomAlert
  alertGroupName: telco-user-mycustomalert   # optional; defaults to telco-user-<alertname-lowercased>
  alertRule: |
    - alert: MyCustomAlert
      expr: <prometheus_expression>
      for: 1m
      labels:
        severity: warning
      annotations:
        cluster: '{{ $labels.cluster }}'
  alertMetrics: '["my_custom_metric_total"]'  # JSON array; "[]" if none
  request: |
    You are an experienced OpenShift administrator. Alert MyCustomAlert has been raised.
    <investigation instructions for Lightspeed>
  skills: "[]"
  mcpServers: "[]"
```

The `alertRule` block is the Prometheus alert rule body — the `- alert: ...` item. The controller wraps it in a rule group using `alertGroupName` (or the default) as the group name.

`alertMetrics` is a JSON array of raw metric names that must be forwarded from spoke clusters to the hub via MCO. Omit metrics that are already covered by MCO's built-in recording rules.

`request`, `skills`, and `mcpServers` are read by the alert receiver when building the `AgenticRun`. If `request` is empty, the AgenticRun is skipped with an error log.

### Enable user-defined alerts in the CR

```yaml
spec:
  alerts:
    userAlerts: true
```

Without `userAlerts: true`, user-alert ConfigMaps are ignored entirely.

### Validation

After creating or updating a user-alert ConfigMap:

1. Wait for the next reconcile (or trigger one by touching the `TelcoHealthcheck` CR).
2. Confirm the `thanos-ruler-custom-rules` ConfigMap in `open-cluster-management-observability` contains the new rule group.
3. Confirm the `observability-metrics-custom-allowlist` ConfigMap lists any new metrics.
4. Fire the alert and verify the alert receiver logs `matched alert – creating AgenticRun`.
5. Check that an `AgenticRun` appears in `openshift-lightspeed` on the target spoke cluster.
