# Adding a New Periodic Health Check

Periodic health checks create an `AgenticRun` on every monitored spoke cluster each time a configured interval elapses. This document covers adding a new check type as a packaged part of the operator (a _system periodic check_). The process requires a code change and an operator release.

---

## How periodic checks work

```
TelcoHealthcheck CR
  spec.periodicHealthChecks.period: 6h
  spec.periodicHealthChecks.<checkName>.enabled: true
        │
        ▼  controller reconcile
System-periodic ConfigMap (embedded asset in binary)
        │
        ▼  runPeriodicChecks — when period elapses
createAgenticRunsForClusters
        │  for each monitored spoke cluster
        ▼
AgenticRun created in openshift-lightspeed on spoke
```

On each reconcile the controller checks whether the check's period has elapsed. If it has, it reads the AgenticRun configuration from the corresponding ConfigMap (request prompt, skills, MCP servers), expands any `${VAR}` placeholders, and creates an `AgenticRun` on each spoke cluster.

The ConfigMap is managed by `reconcileSystemPeriodicConfigMaps`: when the spec boolean is `true` the ConfigMap is created or updated from the embedded asset; when `false` it is deleted. On CR deletion `cleanupSystemPeriodicConfigMaps` removes it unconditionally.

---

## Step 1 — Create the asset YAML file

Create `internal/controller/assets/periodic-<name>.yaml`. The file must be a valid `corev1.ConfigMap` manifest:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: telco-anomaly-<name>-config
  labels:
    app.kubernetes.io/managed-by: telco-anomaly-detection
    ran.openshift.io/system-managed-periodic: "true"
data:
  checkName: <name>
  request: |
    You are an experienced OpenShift administrator. <Investigation instructions
    for OpenShift Lightspeed. Describe what to check and what to report.>
  skills: "[]"
  mcpServers: "[]"
```

**Data field notes:**

| Key | Format | Notes |
|---|---|---|
| `checkName` | String | Must match the key used in `checkTypeConfigMaps` (step 4) and the CRD field name |
| `request` | String | Lightspeed prompt; supports `${CLUSTER_NAME}`, `${OPERATOR_NAMESPACE}`, `${KUBE_COMPARE_MCP_URL}` |
| `skills` | JSON array string | OCI skills image config; `"[]"` if none needed |
| `mcpServers` | JSON array string | MCP server config; `"[]"` if none needed |

Variable substitution is performed by `agenticrun.ExpandVariables` at AgenticRun creation time. `${KUBE_COMPARE_MCP_URL}` is only populated when the `telco-anomaly-kube-compare-mcp` Route exists in the operator namespace.

---

## Step 2 — Wire the asset in `periodicchecks.go`

In `internal/controller/periodicchecks.go`:

1. Add an embed variable below the existing ones:
   ```go
   //go:embed assets/periodic-<name>.yaml
   var periodic<Name>YAML []byte
   ```

2. Add an entry to `systemPeriodicAssets`:
   ```go
   {yaml: periodic<Name>YAML, enabled: func(p ranv1alpha1.PeriodicHealthChecksSpec) bool { return p.<Name>.Enabled }},
   ```

---

## Step 3 — Add a spec struct and field to the API type

In `api/v1alpha1/telcohealthcheck_types.go`:

1. Add a configuration struct (modelled after `RDSComplianceSpec`):
   ```go
   type <Name>Spec struct {
       // Period overrides the default check interval.
       // +optional
       Period *metav1.Duration `json:"period,omitempty"`
       // Enabled activates the check when true.
       Enabled bool `json:"enabled"`
   }
   ```

2. Add a field to `PeriodicHealthChecksSpec`:
   ```go
   type PeriodicHealthChecksSpec struct {
       Period        metav1.Duration `json:"period"`
       RDSCompliance RDSComplianceSpec `json:"rdsCompliance,omitempty"`
       <Name>        <Name>Spec        `json:"<camelName>,omitempty"`
   }
   ```

3. Add a status field to `TelcoHealthcheckStatus` to track the last run time:
   ```go
   // Last<Name>RunTime records when the last <name> check was triggered.
   // +optional
   Last<Name>RunTime *metav1.Time `json:"last<Name>RunTime,omitempty"`
   ```

Then regenerate:

```bash
make generate   # regenerates zz_generated.deepcopy.go
make manifests  # regenerates config/crd/bases/ and config/rbac/
```

---

## Step 4 — Register the check type in `agenticrun.go`

In `internal/controller/agenticrun.go`, add an entry to `checkTypeConfigMaps`:

```go
var checkTypeConfigMaps = map[string]string{
    "rds-compliance": "telco-anomaly-rds-compliance-config",
    "<name>":         "telco-anomaly-<name>-config",   // add this line
}
```

The key must match the `checkName` value in the asset YAML (step 1). The value must match the ConfigMap `metadata.name` in the same asset.

---

## Step 5 — Add the check to `runPeriodicChecks`

In `internal/controller/telcohealthcheck_controller.go`, inside `runPeriodicChecks`, add a block modelled after the existing RDS compliance block:

```go
check := thc.Spec.PeriodicHealthChecks.<Name>
if check.Enabled {
    checkPeriod := period
    if check.Period != nil {
        checkPeriod = check.Period.Duration
    }

    if shouldRunCheck(thc.Status.Last<Name>RunTime, checkPeriod) {
        logger.Info("running <name> health checks")
        if err := createAgenticRunsForClusters(ctx, r.Client, thc, monitoredClusters, "<name>"); err != nil {
            logger.Error(err, "error during <name> checks")
        } else {
            thc.Status.Last<Name>RunTime = &now
        }
    } else if thc.Status.Last<Name>RunTime != nil {
        untilNext := checkPeriod - time.Since(thc.Status.Last<Name>RunTime.Time)
        if untilNext > 0 && untilNext < requeueAfter {
            requeueAfter = untilNext
        }
    }
}
```

---

## Step 6 — Update the sample CR and architecture doc

- Add the new field to `config/samples/ran_v1alpha1_telcohealthcheck.yaml` under `periodicHealthChecks`:
  ```yaml
  periodicHealthChecks:
    period: 6h
    rdsCompliance:
      enabled: false
    <camelName>:
      enabled: false
  ```

- Update `docs/architecture.md`: add a row for the new check in the AgenticRun config table.

---

## What you do NOT need to change

- `agenticrun/config.go`, `agenticrun/build.go`, or `agenticrun/expand.go` — fully generic
- `alertrules.go`, `observabilitymetrics.go`, or `handler.go` — unrelated to periodic checks

---

## Checklist

| # | File | What to change |
|---|---|---|
| 1 | `internal/controller/assets/periodic-<name>.yaml` | New asset file |
| 2 | `internal/controller/periodicchecks.go` | Add embed var + asset entry |
| 3 | `api/v1alpha1/telcohealthcheck_types.go` | Add spec struct, `PeriodicHealthChecksSpec` field, status field |
| 3 | Run `make generate && make manifests` | Regenerate DeepCopy and CRD |
| 4 | `internal/controller/agenticrun.go` | Add entry to `checkTypeConfigMaps` |
| 5 | `internal/controller/telcohealthcheck_controller.go` | Add check block in `runPeriodicChecks` |
| 6 | `config/samples/ran_v1alpha1_telcohealthcheck.yaml` | Add field to sample CR |
| 6 | `docs/architecture.md` | Update AgenticRun config table |

---

## Validation

After building and deploying the operator:

1. Set `spec.periodicHealthChecks.<camelName>.enabled: true` and `spec.periodicHealthChecks.period: 1m` in the `TelcoHealthcheck` CR.
2. Confirm the controller creates `telco-anomaly-<name>-config` in `telco-healthcheck-system`.
3. Wait one minute and verify an `AgenticRun` appears in `openshift-lightspeed` on each monitored spoke.
4. Confirm `status.last<Name>RunTime` is updated in the CR.
5. Set `enabled: false` and confirm the controller deletes `telco-anomaly-<name>-config`.
