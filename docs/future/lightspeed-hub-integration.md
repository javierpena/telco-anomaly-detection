# lightspeed-hub Integration: Remove Direct Spoke Cluster Connections

## Context and upstream contract

The operator currently reads `<name>-admin-kubeconfig` Secrets on the hub, creates `AgenticRun` resources on spokes, and uses `SpokeWatchManager` to relay spoke AgenticRun/AnalysisResult status into hub `TelcoHealthCheckRun` records. `lightspeed-hub` and the `lightspeed-agentic-operator` are intended to move AgenticRuns to `openshift-lightspeed` **on the hub**: `spec.targetCluster` names the ACM spoke, while lightspeed-hub provisions a standing `spoke-kubeconfig-{name}` Secret for the agentic operator. Our controller and alert receiver should neither read that Secret nor connect directly to spokes.

Before implementing the status migration, verify against the versions to be deployed:

- The hub AgenticRun CRD accepts `spec.targetCluster` (its validation, required/optional status, and namespace), and the agentic operator's expected `SpokeCluster`/standing-Secret readiness contract. Document the exact supported versions.
- Where `AnalysisResult` objects are created for hub AgenticRuns, their API version, the field linking them to an AgenticRun, and the status fields used below. Hub-local AnalysisResult watches only work if results also exist on the hub. If they do not, agree on a different hub-visible status source before removing the spoke watcher.
- Whether a missing/not-ready standing Secret rejects the AgenticRun CREATE request or permits creation followed by a failed status. Record the observed behavior; a successful Kubernetes CREATE must not be reported as a successful investigation.

**Operational prerequisite:** install lightspeed-hub and a compatible lightspeed-agentic-operator on the hub, create a `SpokeCluster` for each monitored ACM ManagedCluster, and wait for readiness. Do not add a startup dependency on these resources to the telco operator. Surface a missing dependency through creation failure or the eventual hub run status, as appropriate to the verified contract.

## Implementation plan

### 1. AgenticRun model and builder (`internal/agenticrun/`)

- Add `TargetCluster string` with the appropriate JSON tag to `AgenticRunSpec` in `types.go`; change the `Namespace` comment to say **on the hub**. Keep its value `openshift-lightspeed`.
- Add a `clusterName` argument to `BuildObject()` in `build.go` and write `spec.targetCluster`. Update all callers and builder tests; cover both the target cluster and the existing request, analysis/execution, labels, skills, and MCP server fields.
- `BuildVarMap()` **already** accepts only hub client, operator namespace, and cluster name; it requires no spoke-connectivity change. Preserve `${CLUSTER_NAME}`, alert-only `${NODE_NAME}`, and the hub-local `${KUBE_COMPARE_MCP_URL}` Route lookup.

### 2. Periodic creation (`internal/controller/agenticrun.go`)

- Remove `getClusterKubeconfig()` and `buildSpokeClient()` calls; create each built AgenticRun with the hub client in `openshift-lightspeed`. Keep per-cluster jitter and the pending-record-before-create, confirm/fail flow.
- **Generate a distinct AgenticRun name per target cluster.** Currently one interval shares a name across spokes; on the hub the second CREATE would collide. Choose a DNS-valid, at-most-253-character deterministic per-cluster suffix (hash/truncate long names as needed). Derive each `TelcoHealthCheckRun` name and its `status.agenticRunName` from that actual AgenticRun name, and update `healthcheckrun.Name()` usage and tests. Preserve the existing alert naming scheme unless a collision concern is found there.
- Keep `healthcheckrun.Begin()` setting `status.clusterName` before creation. Require the hub status sync to compare it with `spec.targetCluster`; on mismatch, log and refuse to copy status rather than silently changing the audit identity. Existing `healthcheckrun.Key(cluster, runName)` labels can still be used for correlation if both sides use the new name consistently.
- Preserve handling of definitive versus ambiguous CREATE failures: do not create a second run when the response might have been lost.

### 3. Alert receiver (`internal/alertreceiver/handler.go`)

- The receiver runs in a **separate pod** and already has its own hub in-cluster client. Remove `fetchKubeconfig()`, the pre-fetched kubeconfig map in `getMonitoredClusters()`/`processAlerts()`, `NewSpokeClient`, and related client builders. Keep monitored-cluster and alert-name validation.
- Resolve the alert ConfigMap, load/expand its config, and call the new `BuildObject()` with the validated cluster name. Use `h.HubClient.Create()` in the hub's `openshift-lightspeed` namespace after `healthcheckrun.Begin()`. Keep existing confirm/fail and per-alert error isolation. Update handler and server tests.

### 4. Pending-record recovery and legacy records (`internal/controller/runrecovery.go`)

- Replace kubeconfig fetch/spoke client/Get with a hub Get of the named AgenticRun in `openshift-lightspeed`. Check `spec.targetCluster` against the record's `status.clusterName` before confirming it; handle a mismatch explicitly rather than pairing the wrong run. Preserve the grace period, definitive absence, ambiguous read-error retry, and incomplete-record behavior.
- Define an upgrade policy for records created **before** this migration: old spoke runs are not visible through a hub Get. In particular, do not turn an old pending spoke record into a false `Failed` hub-create record. Document and test the chosen cutover (for example, drain pending records before upgrading and leave completed legacy records as historical snapshots). No direct spoke connection should remain in the new steady state.

### 5. Replace `SpokeWatchManager` with hub-local status reconciliation

- Remove `internal/controller/spokewatchmanager.go`, its manager runnable, `SpokeWatches` field, sync/stop/ready checks, and the `<clusterName>-admin-kubeconfig` Secret watch from `telcohealthcheck_controller.go`. Keep the watch for pending `TelcoHealthCheckRun` records that triggers recovery, or move recovery to a run-record controller with equivalent retries.
- Add a run-record **status reconciler** (or extend the existing `runAlertReconciler`) that actually reconciles `TelcoHealthCheckRun` keys. Do not enqueue run-record keys onto the `TelcoHealthcheck` singleton reconciler. Register controller-runtime hub watches for `AgenticRun` and, subject to the upstream contract, `AnalysisResult` in `openshift-lightspeed`. Configure unstructured GVK/list types and cache scope as required by controller-runtime; limit event handling to this namespace and this operator's runs.
- Map an AgenticRun event by `(spec.targetCluster, metadata.name)` to the record's existing `ran.openshift.io/run-key` label, then verify `status.clusterName` and `status.agenticRunName` before enqueuing/updating. Map an AnalysisResult via its verified AgenticRun reference: read the corresponding hub AgenticRun to obtain `spec.targetCluster`, then resolve the same record. Do not match on run name alone. Handle objects arriving before their record, replay after restart, watch deletions, API errors, and retries without losing final status.
- Reconcile the **current** AgenticRun and result state together, not solely the last watch event, to avoid ordering races. Preserve the current `TelcoHealthCheckRun` semantics: `agenticRunStatus.type` is the type of the latest condition with a valid transition timestamp; `.phase` is that condition's **reason**, not AgenticRun `status.phase`. Keep `Pending`/`Created`/`Failed` until a condition is available. Keep the summary fallbacks (`status.diagnosis.summary`, options, failure reason), `Analyzed`-only `actionRequired`, resetting to `False` on another non-empty type, and re-reading the result when returning to `Analyzed`.
- Patch record status with optimistic-lock/retry semantics, and call `queueActionAlert()` in the **same patch** as any change to action-required so external Alertmanager firing/resolution delivery remains durable. Coordinate with the existing `runAlertReconciler` to avoid dropping its notification queue or introducing status-patch loops. Retain historical records when clusters cease being monitored.

### 6. Remaining cluster discovery and console links

- Remove `getClusterKubeconfig()` from `internal/controller/managedcluster.go` and all old spoke-client helpers. Keep ManagedCluster list resolution and include/exclude logic.
- Review `runAlertReconciler.agenticRunURL()`: it currently builds a **spoke** console URL from a ManagedCluster claim. Determine the correct hub console deep-link for a hub-resident AgenticRun; update the URL or omit the annotation when a valid hub URL is unavailable. Update link tests and docs.

### 7. RBAC and cache configuration (`config/rbac/`, RBAC markers, manager setup)

- Remove permissions used **only** to read `<clusterName>-admin-kubeconfig` Secrets and delete that Secret watch. Do **not** delete all Secret permissions: the controller still reads/updates `open-cluster-management-observability/alertmanager-config`; kube-compare-mcp reads `openshift-config/pull-secret` and manages `telco-healthcheck-system/kube-compare-registry-credentials`; optional outbound Alertmanager delivery reads a credentials Secret in the operator namespace and watches it for rotation.
- Scope those remaining Secret permissions by namespace and operation where practical; consider `resourceNames` for named Get/Update operations. The current generated ClusterRole/ClusterRoleBinding grants cluster-wide Secret create/delete/get/list/patch/update/watch. A namespace Role/RoleBinding and/or narrowly bound ClusterRole may be needed, in addition to edits to the generating kubebuilder markers. **Plan the controller-runtime Secret cache/watch alongside RBAC**: a namespace-limited binding alone does not authorize an all-namespaces informer. Use appropriately scoped cache selectors or uncached reads for point Gets, and keep the credentials-rotation watch working. Check both controller and receiver ServiceAccount usage before narrowing their shared binding.
- Add hub `agenticruns.agentic.openshift.io` create/get/list/watch for creation, recovery, and status watch; add hub `analysisresults.agentic.openshift.io` get/list/watch if the upstream contract confirms they are hub-resident. Limit these permissions to `openshift-lightspeed` where feasible and account for the manager cache's actual list/watch scope. Update RBAC markers and any hand-maintained Role/RoleBinding manifests, run `make manifests`, and inspect generated output for unwanted broad Secret permissions or overwritten scoping.

### 8. Documentation and tests

- Update `docs/architecture.md` and `README.md`: hub-resident runs/results, `spec.targetCluster`, lightspeed-hub/SpokeCluster prerequisites, unique run names, status mapping, pending recovery, console URL, namespaces, and scoped Secret permissions. Remove obsolete spoke kubeconfig and SpokeWatchManager descriptions. Add a phase in `steps.md`.
- Update spoke-client-dependent tests in `internal/controller/` and `internal/alertreceiver/`. Cover two clusters in the same periodic interval; alert-driven hub creation; ambiguous create and hub recovery; missing/mismatched target cluster; result/condition event ordering and replay; action-required firing and resolution; and Secret-cache/RBAC behavior where integration tests can exercise it.

## Verification

1. Confirm the upstream CRD/result/Secret behavior and version compatibility above. Install lightspeed-hub and the agentic operator on the hub, create a `SpokeCluster` for at least two ACM spokes, and wait for readiness.
2. Apply manifests with `make deploy`. Verify the controller and receiver can read the specific Secrets they need and cannot read unrelated spoke admin kubeconfigs; confirm watches start without RBAC errors.
3. Create the singleton `TelcoHealthcheck` with both spokes included. Set a short `periodicHealthChecks.period`, **enable** `rdsCompliance` or `lowLatencyCheck`, and use `0s`/`0s` jitter if immediate verification is needed.
4. Verify **two differently named** hub AgenticRuns in `openshift-lightspeed`, each with the correct `spec.targetCluster`, and two corresponding hub `TelcoHealthCheckRun` records with matching identities. Verify no operator requests to spoke API servers or spoke admin-kubeconfig Secrets.
5. Follow condition changes and AnalysisResult updates through record type, phase, summary, and action-required fields. Check external Alertmanager firing/resolution if configured; restart the controller and confirm reconciliation recovers current state.
6. Trigger an alert through the webhook and verify the same hub-local create/status flow. Exercise missing/not-ready lightspeed-hub prerequisites and verify the recorded behavior matches the upstream contract.
7. Run `make test`, `make lint`, and `make vet`; inspect generated RBAC and documentation changes.
