# Plan: Per-cluster jitter for periodic health check AgenticRun creation

**Status:** Proposed (not yet implemented).

## Motivation

When a periodic health check period elapses, the controller currently creates
`AgenticRun` resources on **all** monitored spoke clusters within a single
reconcile loop, one after another with no delay. On large fleets this produces
a burst of simultaneous AgenticRun objects, which can overwhelm OpenShift
Lightspeed's scheduling capacity on each spoke.

Adding a random per-cluster delay (jitter) spreads the AgenticRun creation
across a configurable window, preventing the thundering-herd effect without
changing the check semantics or the overall period.

## Confirmed decisions

- **Delay range:** `[minJitter, maxJitter]` — both bounds are configurable via
  `spec.periodicHealthChecks.minJitter` (default `30s`) and
  `spec.periodicHealthChecks.maxJitter` (default `5m`).
- **Delay model:** each cluster draws its own independent random delay uniformly
  from `[minJitter, maxJitter]`. No sequential staggering.
- **Status timestamp** (`lastRDSComplianceRunTime`): set at **trigger time**
  (when the reconciler decides the check is due), before goroutines sleep.
  Simple and requires no goroutine-to-status coordination.
- **Goroutine context:** goroutines use the manager's root context (stored on
  the reconciler) so they are canceled on graceful pod shutdown.
- **Zero disables jitter:** setting both `minJitter: 0s` and `maxJitter: 0s`
  disables jitter and restores the current synchronous behavior.

---

## Workstream A — API change

**File:** `api/v1alpha1/telcohealthcheck_types.go`

**Global defaults** — add `MinJitter` and `MaxJitter` to `PeriodicHealthChecksSpec`:

```go
// +kubebuilder:validation:XValidation:rule="!(has(self.minJitter) && has(self.maxJitter) && duration(self.minJitter.duration) > duration(self.maxJitter.duration))",message="minJitter must be less than or equal to maxJitter"
type PeriodicHealthChecksSpec struct {
    Period        metav1.Duration   `json:"period"`
    RDSCompliance RDSComplianceSpec `json:"rdsCompliance,omitempty"`
    // MinJitter is the lower bound of the random per-cluster delay before AgenticRun
    // creation. Must be <= MaxJitter. Defaults to 30s when omitted.
    // +optional
    MinJitter *metav1.Duration `json:"minJitter,omitempty"`
    // MaxJitter is the upper bound of the random per-cluster delay. Each cluster draws
    // an independent delay from [MinJitter, MaxJitter]. Set both to 0 to disable jitter.
    // Defaults to 5m when omitted.
    // +optional
    MaxJitter *metav1.Duration `json:"maxJitter,omitempty"`
}
```

**Per-check overrides** — add the same pair to `RDSComplianceSpec`, mirroring the existing `Period` override pattern:

```go
// +kubebuilder:validation:XValidation:rule="!(has(self.minJitter) && has(self.maxJitter) && duration(self.minJitter.duration) > duration(self.maxJitter.duration))",message="minJitter must be less than or equal to maxJitter"
type RDSComplianceSpec struct {
    Period  *metav1.Duration `json:"period,omitempty"`
    Enabled bool             `json:"enabled"`
    // MinJitter overrides the global minJitter for this check only.
    // +optional
    MinJitter *metav1.Duration `json:"minJitter,omitempty"`
    // MaxJitter overrides the global maxJitter for this check only.
    // +optional
    MaxJitter *metav1.Duration `json:"maxJitter,omitempty"`
}
```

When a future sub-check type is added, apply the same two fields to its spec struct.

Run `make manifests` and `make generate` afterward.

---

## Workstream B — Jitter implementation in `createAgenticRunsForClusters`

**File:** `internal/controller/agenticrun.go`

1. Add `minJitter, maxJitter time.Duration` parameters to `createAgenticRunsForClusters`.

2. Before the cluster loop, load the shared AgenticRun config (same as today).

3. In the cluster loop, compute a per-cluster delay drawn uniformly from `[minJitter, maxJitter]`:
   ```go
   delay := time.Duration(0)
   if maxJitter > 0 {
       window := maxJitter - minJitter
       delay = minJitter + time.Duration(rand.Int63n(int64(window)+1))
   }
   ```

4. When `maxJitter == 0`: run the existing synchronous create (backward compatible).

   When `maxJitter > 0`: launch a goroutine per cluster that:
   - Waits for `delay` (or exits early if the context is canceled):
     ```go
     select {
     case <-time.After(delay):
     case <-ctx.Done():
         return
     }
     ```
   - Then fetches the kubeconfig, builds the spoke client, expands variables, and creates the AgenticRun — the same steps as today but inside the goroutine.

5. Return immediately after launching all goroutines (the function no longer
   waits for AgenticRun creation to complete when jitter is enabled).

The shared config (`cfg`) is loaded once before the loop and captured by value
in each goroutine closure to avoid data races.

---

## Workstream C — Reconciler wiring

**File:** `internal/controller/telcohealthcheck_controller.go`

1. Add a `ctx context.Context` field to `TelcoHealthcheckReconciler` for the
   manager's root context (set once in `SetupWithManager`, used in goroutines):
   ```go
   type TelcoHealthcheckReconciler struct {
       // ... existing fields ...
       ctx context.Context  // manager root context; canceled on shutdown
   }
   ```

2. In `SetupWithManager`, store the manager context:
   ```go
   r.ctx = mgr.GetContext()
   ```

3. In `runPeriodicChecks`, resolve the effective jitter for each sub-check using
   the same override-or-global pattern already used for `period`:
   ```go
   // Global defaults
   globalMinJitter := 30 * time.Second
   if thc.Spec.PeriodicHealthChecks.MinJitter != nil {
       globalMinJitter = thc.Spec.PeriodicHealthChecks.MinJitter.Duration
   }
   globalMaxJitter := 5 * time.Minute
   if thc.Spec.PeriodicHealthChecks.MaxJitter != nil {
       globalMaxJitter = thc.Spec.PeriodicHealthChecks.MaxJitter.Duration
   }

   // Per-check effective values (RDS compliance example)
   rdsMinJitter := globalMinJitter
   if rds.MinJitter != nil {
       rdsMinJitter = rds.MinJitter.Duration
   }
   rdsMaxJitter := globalMaxJitter
   if rds.MaxJitter != nil {
       rdsMaxJitter = rds.MaxJitter.Duration
   }
   ```
   Pass the effective `rdsMinJitter`, `rdsMaxJitter`, and `r.ctx` to `createAgenticRunsForClusters`.

**File:** `cmd/controller/main.go`

No changes needed — `mgr.GetContext()` is available from the manager without
additional wiring.

---

## Workstream D — Tests

**File:** `internal/controller/agenticrun_test.go` (or similar)

1. Test `createAgenticRunsForClusters` with `minJitter == 0, maxJitter == 0`: verify
   synchronous behavior is unchanged.
2. Test with `minJitter = 0, maxJitter = 1ms`: verify goroutines complete and
   AgenticRuns are created, and delay is within [0, 1ms].
3. Test with `minJitter = 1ms, maxJitter = 2ms`: verify delay is always >= 1ms.
4. Test context cancellation: cancel the context before the delay elapses
   (`minJitter = 1h, maxJitter = 2h`) and verify the goroutine exits without
   creating the AgenticRun.

**File:** `internal/controller/telcohealthcheck_controller_test.go`

5. Update `createAgenticRunsForClusters` call sites to pass the new `minJitter`
   and `maxJitter` parameters (use `0, 0` in existing tests to keep synchronous behavior).

---

## Workstream E — Docs & manifests

1. `docs/architecture.md` — add `minJitter` and `maxJitter` to the spec fields
   table and note the goroutine-based jitter in the periodic checks section of
   the reconcile loop.
2. `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml` — regenerated by
   `make manifests`.
3. `steps.md` — add a Phase 15 entry for this feature.
4. Sample CR in `config/samples/ran_v1alpha1_telcohealthcheck.yaml` and
   `README.md` — optionally add a commented `maxJitter` example.

---

## Implementation order

1. A — API type + `make manifests && make generate`
2. B — jitter goroutine logic in `agenticrun.go`
3. C — reconciler wiring (context field, maxJitter read, pass-through)
4. D — tests
5. E — docs

---

## Verification

```bash
make generate && make manifests && make build && make vet && make test
```

Manual smoke test on a cluster:

```yaml
# Global jitter window; RDS compliance overrides with a wider range
periodicHealthChecks:
  period: 2m
  minJitter: 5s
  maxJitter: 30s
  rdsCompliance:
    enabled: true
    minJitter: 10s   # overrides global for this check
    maxJitter: 2m
```

Apply the CR, wait for the period to elapse, then:
```bash
kubectl logs -n telco-healthcheck-system deployment/telco-anomaly-controller | grep -i jitter
# Observe "creating AgenticRun" log lines arriving spread between 5s and 30s after trigger
```

To verify disable:
```yaml
periodicHealthChecks:
  minJitter: 0s
  maxJitter: 0s
```
All AgenticRuns should appear within a single reconcile (current behavior).

## Risks / open items

- **Goroutine leak on tight reconcile loops:** if the period is very short and
  the jitter window is long, new goroutines may be launched before old ones
  finish. The `ctx.Done()` guard limits this, but very high cluster counts with
  long jitter and short periods could accumulate goroutines. A bounded worker
  pool could be considered if this becomes a problem in practice.
- **`rand.Int63n` seeding:** Go's `math/rand` global source is automatically
  seeded since Go 1.20. No explicit seed is needed.
- **Validation:** The CEL rule on `PeriodicHealthChecksSpec` enforces
  `minJitter <= maxJitter` at admission time. The controller also guards
  defensively at runtime (if `minJitter >= maxJitter`, falls back to synchronous
  behavior and logs a warning).
- **Per-check jitter:** `MinJitter`/`MaxJitter` are global; they apply equally
  to all periodic sub-checks (currently only RDS compliance). If future
  sub-checks need independent jitter control, per-check fields can be added to
  each sub-check spec (e.g. `RDSComplianceSpec.MinJitter`/`MaxJitter`) and the
  global fields demoted to defaults.
