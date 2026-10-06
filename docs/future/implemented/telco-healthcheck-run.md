# TelcoHealthCheckRun CRD

> **Implemented.** `docs/architecture.md` describes the running design. Key
> refinements from this original proposal: persist a pending hub record before
> spoke creation and recover it by run name; derive `phase` from the latest
> `AnalysisResult.status.conditions[*].reason` (the CRD has no phase or summary
> fields); keep a reconnecting list/watch per spoke instead of ending it after
> one result; use the existing `telco-anomaly-operator` ServiceAccount for purge.
> Run records now expose Pending/Created/Failed creation phases and mirror
> `AnalysisResult.status.actionRequired` into the hub record's **status**.

## Background

The operator creates `AgenticRun` resources on spoke clusters but leaves no audit trail on the hub. `TelcoHealthCheckRun` fills this gap — a hub-side record that captures every AgenticRun creation event and actively tracks its outcome by monitoring the corresponding `AnalysisResult` resource on the spoke cluster. It is purely observational (empty spec, status-only) and is garbage-collected when the `TelcoHealthcheck` singleton is deleted.

## API

**Group/Version**: `ran.openshift.io/v1alpha1`  
**Kind**: `TelcoHealthCheckRun`  
**Scope**: Namespaced (always created in `telco-healthcheck-system`)  
**Short name**: `thcr`

### Type definition

```go
// api/v1alpha1/telcohealthcheckrun_types.go

// TriggerType identifies what caused an AgenticRun to be created.
// +kubebuilder:validation:Enum=alert;periodicHealthCheck
type TriggerType string

const (
    TriggerTypeAlert               TriggerType = "alert"
    TriggerTypePeriodicHealthCheck TriggerType = "periodicHealthCheck"
)

// AgenticRunStatus mirrors the relevant fields from the AnalysisResult resource
// on the spoke cluster associated with the AgenticRun.
type AgenticRunStatus struct {
    // Phase reflects the current phase of the AgenticRun/AnalysisResult
    // (e.g., Pending, Running, Succeeded, Failed).
    // +optional
    Phase string `json:"phase,omitempty"`
    // Summary contains the human-readable conclusion extracted from the AnalysisResult.
    // +optional
    Summary string `json:"summary,omitempty"`
}

// TelcoHealthCheckRunSpec is empty; the resource is managed by the operator.
type TelcoHealthCheckRunSpec struct{}

type TelcoHealthCheckRunStatus struct {
    // AgenticRunName is the name of the AgenticRun created on the spoke cluster.
    AgenticRunName string      `json:"agenticRunName"`
    // ClusterName is the ACM-managed cluster the AgenticRun was created for.
    ClusterName    string      `json:"clusterName"`
    // TriggeredBy indicates whether this run was started by an alert or a periodic health check.
    TriggeredBy    TriggerType `json:"triggeredBy"`
    // Trigger is the name of the alert or periodic health check type that caused the creation.
    Trigger        string      `json:"trigger"`
    // AgenticRunStatus mirrors the phase and summary from the AnalysisResult on the spoke cluster.
    // Populated asynchronously after the AgenticRun is created; absent until the first status sync.
    // +optional
    AgenticRunStatus *AgenticRunStatus `json:"agenticRunStatus,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=thcr
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.status.clusterName`
// +kubebuilder:printcolumn:name="Triggered By",type=string,JSONPath=`.status.triggeredBy`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.status.trigger`
// +kubebuilder:printcolumn:name="AgenticRun",type=string,JSONPath=`.status.agenticRunName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.agenticRunStatus.phase`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type TelcoHealthCheckRun struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   TelcoHealthCheckRunSpec   `json:"spec,omitempty"`
    Status TelcoHealthCheckRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TelcoHealthCheckRunList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata,omitempty"`
    Items           []TelcoHealthCheckRun `json:"items"`
}
```

The creation date of each run is available via `metadata.creationTimestamp` — a standard Kubernetes field automatically set on every object.

## Lifecycle

Each `TelcoHealthCheckRun` carries an owner reference to the `TelcoHealthcheck` singleton:

```go
metav1.OwnerReference{
    APIVersion: "ran.openshift.io/v1alpha1",
    Kind:       "TelcoHealthcheck",
    Name:       "telco-healthcheck",
    UID:        <singleton UID>,
}
```

Kubernetes garbage-collects all `TelcoHealthCheckRun` resources in `telco-healthcheck-system` when the singleton is deleted. Additionally, the operator reconciles a `CronJob` that periodically removes `TelcoHealthCheckRun` resources older than `spec.purgeInterval` (see [Purge](#purge)).

## Creation sites

Two code paths create AgenticRun resources and must also create a paired `TelcoHealthCheckRun`.

### Controller — periodic health checks

**File**: `internal/controller/agenticrun.go`, function `createAgenticRunsForClusters`

| Field | Value |
|---|---|
| `triggeredBy` | `periodicHealthCheck` |
| `trigger` | `checkType` parameter (e.g., `rds-compliance`) |
| `clusterName` | current cluster in the loop |
| `agenticRunName` | AgenticRun name without cluster suffix |

**Naming note**: `createAgenticRunsForClusters` computes a single `runName` (e.g., `telco-health-rds-compliance-1748000000000000000`) and reuses it for every cluster in the monitored list. Since all `TelcoHealthCheckRun` resources land in the same namespace (`telco-healthcheck-system`), the names must be unique — use `{runName}-{clusterName}` (e.g., `telco-health-rds-compliance-1748000000000000000-cluster1`). The original AgenticRun name is recorded in `status.agenticRunName` for accurate correlation.

### Alert receiver — alert-triggered runs

**File**: `internal/alertreceiver/handler.go`, function `createAgenticRunOnCluster`

| Field | Value |
|---|---|
| `triggeredBy` | `alert` |
| `trigger` | `alertName` parameter (e.g., `HostNetworkLatency`) |
| `clusterName` | `clusterName` parameter |
| `agenticRunName` | `runName` (identical to the AgenticRun name; no suffix needed) |

**Naming note**: The alert receiver processes one cluster per invocation, so the `runName` (e.g., `telco-alert-hostnetworklatency-1748000000000000000`) is unique within the namespace. The `TelcoHealthCheckRun` name matches the AgenticRun name exactly.

**Owner reference**: The alert receiver must fetch the `TelcoHealthcheck` singleton via `h.HubClient` to obtain its UID. If that fetch fails, log a warning and skip `TelcoHealthCheckRun` creation — the AgenticRun was already created successfully.

## AgenticRun status monitoring

After a `TelcoHealthCheckRun` is created, the controller actively monitors the corresponding `AnalysisResult` resource on the spoke cluster and reflects its status back into `TelcoHealthCheckRunStatus.AgenticRunStatus`.

### AnalysisResult resource

`AnalysisResult` is a custom resource created by Lightspeed in the `openshift-lightspeed` namespace on the spoke cluster as the outcome of an `AgenticRun`. The relevant fields are:

| AnalysisResult field | Mapped to |
|---|---|
| `.status.phase` (or equivalent) | `AgenticRunStatus.Phase` |
| `.status.summary` (or equivalent) | `AgenticRunStatus.Summary` |

The exact field paths must be confirmed against the Lightspeed API when implementation begins; adjust accordingly.

### Watch-based sync

The controller establishes a dynamic **watch** on `AnalysisResult` resources for each monitored spoke cluster rather than polling. This avoids unnecessary reconcile churn and reacts immediately to completion events.

**Architecture:**

```
Controller manager (hub)
  └─ SpokeWatchManager
       ├─ Spoke A: Watch(AnalysisResult, openshift-lightspeed) ──► event handler
       ├─ Spoke B: Watch(AnalysisResult, openshift-lightspeed) ──► event handler
       └─ ...
```

**`SpokeWatchManager`** (`internal/controller/spokewatchmanager.go`):

- Holds a `map[clusterName]context.CancelFunc` for active watches.
- Called from the main reconcile loop with the current set of monitored clusters.
- Starts a new goroutine + watch for clusters that are newly monitored; cancels watches for clusters that are removed.
- Each goroutine calls the spoke's dynamic client `Watch()` on the `AnalysisResult` GVR and feeds events to a shared handler.

**Event handler logic (per event):**

1. Extract `agenticRunName` and `phase`/`summary` from the `AnalysisResult` object.
2. List all `TelcoHealthCheckRun` resources in `telco-healthcheck-system` where `status.agenticRunName == agenticRunName` and `status.clusterName == clusterName`.
3. For each matching `TelcoHealthCheckRun`, patch `status.agenticRunStatus` with the new phase and summary.
4. Stop watching once the phase reaches a terminal state (`Succeeded` or `Failed`) — no further updates are expected for that AgenticRun.

**Watch lifecycle:**

- Watches start when the `TelcoHealthcheck` reconcile loop first includes a cluster.
- Watches are cancelled (via context) when a cluster is removed from `monitoredClusters` or when the operator shuts down.
- If a watch drops (network error, spoke unreachable), the goroutine logs the error and exits; the `SpokeWatchManager` does not auto-restart — the next `TelcoHealthcheck` reconcile will re-establish it when it rebuilds the monitored cluster list.

**RBAC on spoke:** The controller's spoke `kubeconfig` secret already grants access to `openshift-lightspeed`. A new RBAC entry must grant `get;list;watch` on the `AnalysisResult` resource to that kubeconfig's principal.

## Purge

### `spec.purgeInterval` on `TelcoHealthcheck`

Add a new optional field to `TelcoHealthcheckSpec`:

```go
// PurgeInterval defines the maximum age of TelcoHealthCheckRun resources.
// The controller reconciles a CronJob that deletes all TelcoHealthCheckRun
// resources older than this duration. When omitted, no purge CronJob is created.
// +optional
PurgeInterval *metav1.Duration `json:"purgeInterval,omitempty"`
```

Example CR:

```yaml
spec:
  purgeInterval: 168h   # keep runs for 7 days
```

### Purge CronJob

When `spec.purgeInterval` is set, the controller reconciles a `CronJob` named `telco-healthcheck-purge` in `telco-healthcheck-system`. When `spec.purgeInterval` is unset or removed, the controller deletes the CronJob if it exists.

**CronJob schedule**: fixed at `0 * * * *` (every hour). The Job uses `purgeInterval` only as the age threshold for deletion, not as the schedule interval. Running hourly ensures resources are cleaned up within one hour of expiry regardless of the configured threshold.

**ServiceAccount**: the CronJob runs under the operator's existing `ServiceAccount` (`telco-healthcheck-controller-manager` in `telco-healthcheck-system`). No additional `ServiceAccount`, `Role`, or `RoleBinding` is required — the operator's SA already has the necessary permissions on `telcohealthcheckruns`.

**Container**: a standard `oc` image (e.g. `registry.redhat.io/openshift4/ose-cli-rhel9:v4.22`). The purge script computes a cutoff timestamp from `PURGE_INTERVAL_SECONDS` and deletes matching resources:

```bash
#!/bin/bash
set -euo pipefail
CUTOFF=$(date -u -d "-${PURGE_INTERVAL_SECONDS} seconds" +%Y-%m-%dT%H:%M:%SZ)
kubectl get thcr -n telco-healthcheck-system -o json \
  | jq -r --arg cutoff "$CUTOFF" \
      '.items[] | select(.metadata.creationTimestamp < $cutoff) | .metadata.name' \
  | xargs -r kubectl delete thcr -n telco-healthcheck-system
```

### YAML asset

The CronJob is defined as a static YAML asset at `internal/controller/assets/purge-cronjob.yaml`, embedded via `//go:embed` — consistent with the pattern used for other managed resources (alert rules, RBAC, kube-compare MCP). The asset holds `PURGE_INTERVAL_SECONDS` as an empty string placeholder; the reconcile function patches the value after decoding:

```yaml
# internal/controller/assets/purge-cronjob.yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: telco-healthcheck-purge
  namespace: telco-healthcheck-system
  labels:
    app.kubernetes.io/managed-by: telco-anomaly-detection
spec:
  schedule: "0 * * * *"
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: telco-healthcheck-controller-manager
          restartPolicy: OnFailure
          containers:
            - name: purge
              image: registry.redhat.io/openshift4/ose-cli-rhel9:v4.22
              command:
                - /bin/bash
                - -c
                - |
                  set -euo pipefail
                  CUTOFF=$(date -u -d "-${PURGE_INTERVAL_SECONDS} seconds" +%Y-%m-%dT%H:%M:%SZ)
                  kubectl get thcr -n telco-healthcheck-system -o json \
                    | jq -r --arg cutoff "$CUTOFF" \
                        '.items[] | select(.metadata.creationTimestamp < $cutoff) | .metadata.name' \
                    | xargs -r kubectl delete thcr -n telco-healthcheck-system
              env:
                - name: PURGE_INTERVAL_SECONDS
                  value: ""
```

The `reconcilePurgeCronJob` function in `internal/controller/telcohealthcheck_controller.go`:

1. Decodes the YAML asset into a `batchv1.CronJob`.
2. Patches the `PURGE_INTERVAL_SECONDS` env var value with the integer seconds derived from `spec.purgeInterval`.
3. Sets the singleton owner reference on `metadata.ownerReferences`.
4. Applies the object with `c.Patch(..., client.Apply, client.ForceOwnership, client.FieldOwner(...))`.

When `spec.purgeInterval` is absent, the function deletes the CronJob (ignoring not-found errors).

The CronJob's singleton owner reference means Kubernetes will garbage-collect it automatically when the `TelcoHealthcheck` CR is deleted.

## Implementation steps

1. **Create** `api/v1alpha1/telcohealthcheckrun_types.go` with the type definition above (including `AgenticRunStatus`).

2. **Add `PurgeInterval`** to `api/v1alpha1/telcohealthcheck_types.go`.

3. **Run code generation**:
   ```bash
   make generate   # regenerates zz_generated.deepcopy.go
   make manifests  # regenerates config/crd/bases/ and config/rbac/role.yaml
   ```
   Both binaries already call `ranv1alpha1.AddToScheme(scheme)`, so no additional scheme registration is needed.

4. **Add RBAC markers** to `internal/controller/telcohealthcheck_controller.go`:
   ```go
   // +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthcheckruns,verbs=get;list;watch;create;update;patch;delete
   // +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthcheckruns/status,verbs=get;update;patch
   // +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
   ```

5. **Create helper** `internal/controller/telcohealthcheckrun.go`:
   ```go
   func createTelcoHealthCheckRun(
       ctx context.Context,
       c client.Client,
       namespace, name, agenticRunName, clusterName string,
       triggeredBy ranv1alpha1.TriggerType,
       trigger string,
       ownerRef metav1.OwnerReference,
   ) error
   ```
   Steps: build object with owner ref → `c.Create` → set status fields → `c.Status().Update`.

6. **Create** `internal/controller/assets/purge-cronjob.yaml` with the content shown in the [YAML asset](#yaml-asset) section.

7. **Add** `reconcilePurgeCronJob` to `internal/controller/telcohealthcheck_controller.go` following the decode-patch-apply pattern established by `kubecomparemcp.go`.

8. **Create** `internal/controller/spokewatchmanager.go` implementing `SpokeWatchManager` as described in [AgenticRun status monitoring](#agenticrun-status-monitoring).

9. **Wire `SpokeWatchManager`** into `internal/controller/telcohealthcheck_controller.go`:
   - Instantiate once in `SetupWithManager`.
   - Call `manager.Sync(monitoredClusters, spokeClients)` at the end of each reconcile loop.

10. **Add analogous inline helper** in `internal/alertreceiver/handler.go` (following the existing pattern of duplicating helpers between binaries rather than sharing across the binary boundary).

11. **Wire up** both creation sites to call their respective helpers after a successful `spokeClient.Create(ctx, run)`.

12. **Update** `docs/architecture.md` to document the new resource, the spoke watch mechanism, and the purge CronJob.

## Verification

```bash
make generate && make manifests   # confirm types compile and CRDs are generated
make test                         # run all existing tests + new unit tests

# After triggering a periodic check or alert:
kubectl get thcr -n telco-healthcheck-system
kubectl get thcr -n telco-healthcheck-system -o yaml

# Verify agenticRunStatus is populated after the AgenticRun completes on the spoke:
kubectl get thcr <name> -n telco-healthcheck-system \
  -o jsonpath='{.status.agenticRunStatus}'

# Verify purge CronJob is created when purgeInterval is set:
kubectl get cronjob telco-healthcheck-purge -n telco-healthcheck-system
```

New unit tests should cover:
- Controller path: `TelcoHealthCheckRun` is created with correct status after `AgenticRun` creation; name includes cluster suffix when multiple clusters are present.
- Alert receiver path: `TelcoHealthCheckRun` is created with `triggeredBy=alert` and the correct `trigger` and `clusterName`.
- `SpokeWatchManager`: starting/stopping watches as the monitored cluster list changes; `agenticRunStatus` is patched correctly when an `AnalysisResult` event arrives.
- Purge reconcile: `CronJob` is created with the correct `PURGE_INTERVAL_SECONDS` value when `purgeInterval` is set; `CronJob` is deleted when `purgeInterval` is removed.
