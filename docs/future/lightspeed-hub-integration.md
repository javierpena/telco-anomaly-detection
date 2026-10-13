# lightspeed-hub Integration: Remove Direct Spoke Cluster Connections

## Context

The operator currently maintains its own spoke cluster connections: it fetches per-cluster `<name>-admin-kubeconfig` Secrets, builds dynamic clients, creates `AgenticRun` resources in `openshift-lightspeed` on each spoke, and runs a `SpokeWatchManager` goroutine pool to watch AgenticRun/AnalysisResult objects on spoke clusters and relay status back to hub-side `TelcoHealthCheckRun` records.

The `lightspeed-agentic-operator` (with `lightspeed-hub` as a companion operator) introduces `AgenticRun.spec.targetCluster`, which lets AgenticRuns be created on the **hub** cluster while the agentic-operator transparently handles spoke connectivity via a standing kubeconfig Secret (`spoke-kubeconfig-{name}`) provisioned by `lightspeed-hub`. This removes all need for the telco-anomaly-detection operator to manage its own spoke credentials or connections.

---

## Current Architecture (to be removed)

| Component | Spoke interaction |
|---|---|
| `internal/controller/agenticrun.go` | `createAgenticRunForCluster()` fetches kubeconfig Secret, builds spoke `controller-runtime` client, calls `spokeClient.Create()` |
| `internal/controller/managedcluster.go` | `getClusterKubeconfig()`, `buildSpokeClient()` utilities |
| `internal/controller/spokewatchmanager.go` | Per-cluster goroutines watching AgenticRun + AnalysisResult on spokes via dynamic client; updates TelcoHealthCheckRun status on hub |
| `internal/alertreceiver/handler.go` | `fetchKubeconfig()`, builds spoke dynamic client, calls `spokeClient.Resource().Create()` |
| RBAC | Get/list Secrets in spoke namespaces (`<clusterName>-admin-kubeconfig`) |

---

## New Architecture

AgenticRuns are created on the **hub** cluster in the `openshift-lightspeed` namespace with `spec.targetCluster` set to the ACM ManagedCluster name. The `lightspeed-agentic-operator` reads the standing Secret `spoke-kubeconfig-{targetCluster}` (provisioned by `lightspeed-hub`) and handles all spoke interaction internally, including ephemeral RBAC provisioning on the spoke and sandbox pod kubeconfig injection.

Status relay (currently done by `SpokeWatchManager`) is replaced by a standard controller-runtime `Watch` on AgenticRun and AnalysisResult resources in `openshift-lightspeed` on the hub cluster. When those objects change, the TelcoHealthCheckRun reconciler is enqueued and reads `spec.targetCluster` from the linked AgenticRun to confirm `status.clusterName`.

**Prerequisite (ops concern):** `lightspeed-hub` must be installed and a `SpokeCluster` CR must exist for each ACM ManagedCluster the operator monitors. The operator does not need to validate this at startup — `AgenticRun` creation will fail naturally if the standing kubeconfig Secret is absent.

---

## Implementation Plan

### 1. `internal/agenticrun/types.go`

- Add `TargetCluster string` to `AgenticRunSpec`.
- Update the comment on the `Namespace` constant: AgenticRuns are now created in `openshift-lightspeed` **on the hub cluster** (not on spokes). The constant value itself is unchanged.

### 2. `internal/agenticrun/build.go`

- Update `BuildObject()` signature to accept `clusterName string` and set `spec.targetCluster = clusterName` in the returned unstructured object.
- `BuildVarMap()` currently receives spoke-connectivity context (kubeconfig bytes, spoke URL); remove any spoke-specific arguments. `CLUSTER_NAME` stays (it appears in LLM prompts). `KUBE_COMPARE_MCP_URL` stays (it is a hub-local MCP server URL).
- AgenticRun namespace stays `openshift-lightspeed`.

### 3. `internal/controller/agenticrun.go`

- `createAgenticRunForCluster(clusterName string)`: remove the `getClusterKubeconfig()` + `buildSpokeClient()` calls. Pass `clusterName` to `agenticrun.BuildObject()` so it becomes `spec.targetCluster`. Create the AgenticRun with `r.Client` (hub in-cluster client) in `openshift-lightspeed` on the hub. Run name format and namespace are unchanged.
- Remove the now-unused `buildSpokeClient()` helper.
- The per-cluster jitter/goroutine logic for rate-limiting is not spoke-specific; keep it.

### 4. `internal/controller/managedcluster.go`

- Remove `getClusterKubeconfig()`.
- Remove `buildSpokeClient()`.
- All remaining code (cluster list resolution via ACM ManagedCluster CRs, include/exclude logic) is unaffected.

### 5. `internal/controller/spokewatchmanager.go` — remove entirely

Replace with a hub-local watch registered in `SetupWithManager()`. The watch enqueues TelcoHealthCheckRun reconciliation when AgenticRun objects change:

```go
.Watches(
    &unstructured.Unstructured{Object: map[string]interface{}{
        "apiVersion": "agentic.openshift.io/v1alpha1",
        "kind":       "AgenticRun",
    }},
    handler.EnqueueRequestsFromMapFunc(agenticRunToTelcoHealthCheckRun),
    builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
)
```

`agenticRunToTelcoHealthCheckRun` is a new mapper that, given an AgenticRun in `openshift-lightspeed`, finds the `TelcoHealthCheckRun` in `telco-healthcheck-system` whose `status.agenticRunName` matches the AgenticRun name, and enqueues it for reconciliation.

The TelcoHealthCheckRun reconciler (or a dedicated sub-reconciler) then reads:

- `spec.targetCluster` from the AgenticRun → confirms/sets `status.clusterName`
- AgenticRun `status.phase` → updates `status.agenticRunStatus.phase`
- AnalysisResult `status.diagnosis.summary` and `status.actionRequired` → updates corresponding TelcoHealthCheckRun fields

An analogous watch on `AnalysisResult` (also in `openshift-lightspeed`) is added for the summary and actionRequired fields.

The `agenticRunURL()` helper (builds console deep-links from ManagedCluster claims) does not require spoke access; it can be kept as-is since it reads ManagedCluster objects on the hub.

### 6. `internal/alertreceiver/handler.go`

- Remove `fetchKubeconfig()` and the spoke dynamic client creation block inside `createAgenticRunOnCluster()`.
- The alert receiver runs inside the operator pod and already has in-cluster access. Use the hub in-cluster client to create AgenticRuns in `openshift-lightspeed` with `spec.targetCluster = clusterName`.
- Remove the kubeconfig map that was pre-fetched per-cluster in `processAlerts()`.
- `resolveAlertConfigMap()`, `LoadRunConfig()`, `BuildVarMap()`, `ExpandVariables()`, `BuildObject()` calls remain; only the final create target changes from spoke to hub.

### 7. `TelcoHealthCheckRun.status.clusterName` derivation

`status.clusterName` is currently set eagerly in `healthcheckrun.Begin()` from the cluster-name loop variable. This is preserved: the caller still passes `clusterName`, which is the same value set as `spec.targetCluster` in the AgenticRun. The derivation requirement is satisfied because the hub watch handler reads `spec.targetCluster` when syncing AgenticRun status back to TelcoHealthCheckRun, ensuring the two are always consistent. No changes to `healthcheckrun.Begin()` are required.

### 8. RBAC (`config/rbac/`)

- **Remove:** `get`/`list` on Secrets in spoke namespaces (the `<clusterName>-admin-kubeconfig` permission).
- **Add (if not already present):** `create`/`get`/`list`/`watch` on `agenticruns` in the `openshift-lightspeed` namespace.
- **Add:** `get`/`list`/`watch` on `analysisresults` in `openshift-lightspeed`.
- Adjust kubebuilder RBAC markers accordingly and re-run `make manifests`.

### 9. Documentation

- **`docs/architecture.md`**: major update — replace all spoke-client, kubeconfig Secret, and SpokeWatchManager sections. Add a lightspeed-hub prerequisite section explaining SpokeCluster CRs and the standing kubeconfig Secret mechanism. Update the AgenticRun creation flow to show hub-local creation with `spec.targetCluster`. Update the RBAC section.
- **`README.md`**: update prerequisites to include `lightspeed-hub` installation and `SpokeCluster` CR creation per managed cluster; remove setup steps about spoke kubeconfig Secrets.
- **`steps.md`**: add a new phase entry for this work.

---

## Verification

1. Install `lightspeed-hub` and `lightspeed-agentic-operator` on the hub cluster. Create a `SpokeCluster` CR for at least one spoke and wait until it reaches `Ready=True`.
2. Apply the updated operator manifests (`make deploy`).
3. Create the singleton `TelcoHealthcheck` CR with `managedClusters.include` containing the spoke name.
4. Trigger a periodic check (set a short `periodicHealthChecks.period`); confirm an `AgenticRun` with `spec.targetCluster = <spoke-name>` appears in `openshift-lightspeed` on the **hub**.
5. Confirm no kubeconfig Secrets are fetched from spoke namespaces (check operator pod logs and audit log).
6. Confirm a `TelcoHealthCheckRun` record appears in `telco-healthcheck-system` with `status.clusterName = <spoke-name>`.
7. Confirm `status.agenticRunStatus.phase` updates as the AgenticRun progresses (via hub-local watch).
8. Trigger an alert via the webhook and verify the same flow for alert-driven AgenticRuns.
9. Run `make test` and `make lint`.
