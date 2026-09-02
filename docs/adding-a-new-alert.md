# Adding a New Alert to the Alert Receiver

This document describes every change required to introduce a new alert that the alert receiver will act on. Each step is mandatory; skipping any one of them causes the alert to be silently dropped.

## Overview of the pipeline

```
TelcoHealthcheck CR (spec.alerts.<flag>: true)
        │
        ▼  controller reconcile
Thanos Ruler ConfigMap  ──►  alert fires  ──►  AlertManager  ──►  POST /webhook
        │                                                               │
        │  (alert name validation)                                      │
        ▼                                                               ▼
alertConfigMaps lookup (handler.go)  ──►  AgenticRun config ConfigMap
        │
        ▼
AgenticRun created on spoke cluster
```

A new alert touches five distinct places: the API type, the Thanos rule, the metrics allowlist, the alert-to-ConfigMap routing table, and the AgenticRun config ConfigMap.

---

## Step 1 — Add a spec field to the API type

Add a boolean field to `AlertsSpec` in `api/v1alpha1/telcohealthcheck_types.go`:

```go
// AlertsSpec controls which categories of alerts are forwarded to the alert receiver.
type AlertsSpec struct {
    HostNetwork     bool `json:"hostNetwork"`
    PodNetwork      bool `json:"podNetwork"`
    HostReservedCPU bool `json:"hostReservedCPU"`
    MyNewAlert      bool `json:"myNewAlert"`   // add this
}
```

After editing the type, regenerate the CRD YAML and DeepCopy methods:

```bash
make generate   # regenerates zz_generated.deepcopy.go
make manifests  # regenerates config/crd/bases/ran.openshift.io_telcohealthchecks.yaml
```

---

## Step 2 — Write the Thanos alert rule

Add a rule-group function and wire it into `buildCustomRulesYAML` in `internal/controller/alertrules.go`.

**2a. Add the rule group function:**

```go
func myNewAlertRuleGroup() string {
    return `  - name: telco-my-new-alert
    rules:
      - alert: TelcoHealthCheckMyNewAlert
        expr: <prometheus_expression>
        for: 1m
        labels:
          severity: warning
        annotations:
          cluster: '{{ $labels.cluster }}'
`
}
```

The `alert:` value (`TelcoHealthCheckMyNewAlert`) is the alert name that AlertManager will put in `labels.alertname`. It must be unique within the rule file and match exactly what you register in Step 4.

**2b. Call the function from `buildCustomRulesYAML`:**

```go
func buildCustomRulesYAML(alerts ranv1alpha1.AlertsSpec) string {
    if !alerts.HostNetwork && !alerts.PodNetwork && !alerts.HostReservedCPU && !alerts.MyNewAlert {
        return "groups: []\n"
    }
    content := "groups:\n"
    // existing blocks …
    if alerts.MyNewAlert {
        content += myNewAlertRuleGroup()
    }
    return content
}
```

The controller writes this ConfigMap to `thanos-ruler-custom-rules` in `open-cluster-management-observability` on every reconcile. Thanos Ruler reloads the rules automatically via its config-reload sidecar.

---

## Step 3 — Extend the MCO metrics allowlist (if needed)

If the alert expression references metrics that are not already scraped by the MCO observability addon, add them to `buildMetricsListYAML` in `internal/controller/observabilitymetrics.go`:

```go
func buildMetricsListYAML(alerts ranv1alpha1.AlertsSpec) string {
    var names []string
    // existing blocks …
    if alerts.MyNewAlert {
        names = append(names, "my_new_metric_total")
    }
    // …
}
```

The `observability-metrics-custom-allowlist` ConfigMap is propagated by MCO to every spoke cluster's `metrics-collector`, extending the set of metrics forwarded to the hub's Thanos Receive. Metrics already included in MCO's default recording rules (such as `instance:node_network_*` aggregations) do not need to be listed here.

---

## Step 4 — Register the alert name in the receiver routing table

In `internal/alertreceiver/handler.go`, add an entry to the `alertConfigMaps` map:

```go
var alertConfigMaps = map[string]string{
    "TelcoHealthCheckHostNetwork":     "telco-anomaly-host-network-config",
    "TelcoHealthCheckPodNetwork":      "telco-anomaly-pod-network-config",
    "TelcoHealthCheckHostReservedCPU": "telco-anomaly-host-reserved-cpu-config",
    "TelcoHealthCheckMyNewAlert":      "telco-anomaly-my-new-alert-config",  // add this
}
```

The alert receiver validates `labels.alertname` against the `thanos-ruler-custom-rules` ConfigMap at runtime, so the alert name here must match the `alert:` value from Step 2 exactly (case-sensitive).

When an alert arrives and its name is found in `alertConfigMaps`, the receiver loads the named ConfigMap to build the AgenticRun. If the name is absent, the alert is skipped with a warning log.

---

## Step 5 — Create the AgenticRun config ConfigMap

Add a new ConfigMap entry to `config/manager/agenticrun-configs.yaml`:

```yaml
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: telco-anomaly-my-new-alert-config
  namespace: telco-healthcheck-system
  labels:
    app.kubernetes.io/managed-by: telco-anomaly-detection
data:
  request: |
    You are an experienced OpenShift administrator. Alert TelcoHealthCheckMyNewAlert
    has been raised for this cluster. <investigation instructions for Lightspeed>
  skills: "[]"
  mcpServers: "[]"
```

**ConfigMap data keys:**

| Key | Format | Effect when absent or empty |
|---|---|---|
| `request` | Plain string | AgenticRun is skipped (error logged) |
| `skills` | JSON array of `{image, paths[]}` | `spec.tools.skills` omitted |
| `mcpServers` | JSON array of `{name, url, timeoutSeconds?}` | `spec.tools.mcpServers` omitted |

The `request` field supports `${VAR}` placeholders. Standard variables resolved at AgenticRun creation time:

| Variable | Value |
|---|---|
| `${OPERATOR_NAMESPACE}` | `telco-healthcheck-system` |
| `${CLUSTER_NAME}` | The target spoke cluster name |
| `${KUBE_COMPARE_MCP_URL}` | URL of the kube-compare-mcp Route (when RDS compliance is enabled) |

The controller creates this ConfigMap on first deployment (`ensureAgenticRunConfigs`) and **never overwrites** it after that, so operators can edit the ConfigMap in-cluster without losing changes on the next reconcile.

---

## Step 6 — Update the sample CR and architecture doc

- Add `myNewAlert: false` to the `alerts` block in `config/samples/ran_v1alpha1_telcohealthcheck.yaml`.
- Add a row for the new alert to the tables in `docs/architecture.md` (Thanos Alert Rules, MCO Custom Metrics Allowlist, and the AgenticRun ConfigMap table).

---

## Checklist summary

| # | File | What to change |
|---|---|---|
| 1 | `api/v1alpha1/telcohealthcheck_types.go` | Add `bool` field to `AlertsSpec` |
| 1 | Run `make generate && make manifests` | Regenerate DeepCopy and CRD |
| 2 | `internal/controller/alertrules.go` | Add rule-group function; add branch in `buildCustomRulesYAML` |
| 3 | `internal/controller/observabilitymetrics.go` | Add metrics to `buildMetricsListYAML` (if raw metrics needed) |
| 4 | `internal/alertreceiver/handler.go` | Add alert name → ConfigMap entry in `alertConfigMaps` |
| 5 | `config/manager/agenticrun-configs.yaml` | Add the new ConfigMap |
| 6 | `config/samples/ran_v1alpha1_telcohealthcheck.yaml` | Add the new field to the sample CR |
| 6 | `docs/architecture.md` | Update alert rules, metrics, and ConfigMap tables |

---

## Validation

After deploying the changes:

1. Set `spec.alerts.myNewAlert: true` in a `TelcoHealthcheck` CR and confirm that:
   - The `thanos-ruler-custom-rules` ConfigMap in `open-cluster-management-observability` contains the new rule group.
   - The `observability-metrics-custom-allowlist` ConfigMap (same namespace) lists any newly required metrics.
2. Trigger the alert manually using `amtool` or by injecting a test metric above the threshold, and confirm:
   - The alert receiver logs `matched alert – creating AgenticRun` with the new alert name.
   - An `AgenticRun` appears in `openshift-lightspeed` on the target spoke cluster.
3. If the alert receiver logs `no AgenticRun ConfigMap configured for alert, skipping`, Step 4 was missed or the alert name does not match.
4. If the alert receiver logs `AgenticRun config has empty request field, skipping`, the ConfigMap from Step 5 has an empty `request` key.
