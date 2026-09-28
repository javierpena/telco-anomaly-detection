# TelcoHealthCheckRun CRD

## Background

The operator creates `AgenticRun` resources on spoke clusters but leaves no audit trail on the hub. `TelcoHealthCheckRun` fills this gap — a lightweight, namespaced hub-side record that captures every AgenticRun creation event. It is purely observational (empty spec, status-only) and is garbage-collected when the `TelcoHealthcheck` singleton is deleted.

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
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=thcr
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.status.clusterName`
// +kubebuilder:printcolumn:name="Triggered By",type=string,JSONPath=`.status.triggeredBy`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.status.trigger`
// +kubebuilder:printcolumn:name="AgenticRun",type=string,JSONPath=`.status.agenticRunName`
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

Kubernetes garbage-collects all `TelcoHealthCheckRun` resources in `telco-healthcheck-system` when the singleton is deleted. Outside of that event, resources persist indefinitely and serve as an audit log.

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

## Implementation steps

1. **Create** `api/v1alpha1/telcohealthcheckrun_types.go` with the type definition above.

2. **Run code generation**:
   ```bash
   make generate   # regenerates zz_generated.deepcopy.go
   make manifests  # regenerates config/crd/bases/ and config/rbac/role.yaml
   ```
   Both binaries already call `ranv1alpha1.AddToScheme(scheme)`, so no additional scheme registration is needed.

3. **Add RBAC markers** to `internal/controller/telcohealthcheck_controller.go`:
   ```go
   // +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthcheckruns,verbs=get;list;watch;create;update;patch
   // +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthcheckruns/status,verbs=get;update;patch
   ```

4. **Create helper** `internal/controller/telcohealthcheckrun.go`:
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

5. **Add analogous inline helper** in `internal/alertreceiver/handler.go` (following the existing pattern of duplicating helpers between binaries rather than sharing across the binary boundary).

6. **Wire up** both creation sites to call their respective helpers after a successful `spokeClient.Create(ctx, run)`.

7. **Update** `docs/architecture.md` to document the new resource.

## Verification

```bash
make generate && make manifests   # confirm type compiles and CRD is generated
make test                         # run all existing tests + new unit tests

# After triggering a periodic check or alert:
kubectl get thcr -n telco-healthcheck-system
kubectl get thcr -n telco-healthcheck-system -o yaml
```

New unit tests should cover:
- Controller path: `TelcoHealthCheckRun` is created with correct status after `AgenticRun` creation; name includes cluster suffix when multiple clusters are present.
- Alert receiver path: `TelcoHealthCheckRun` is created with `triggeredBy=alert` and the correct `trigger` and `clusterName`.
