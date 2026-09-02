# Future: Make TelcoHealthCheck Cluster-Scoped

## Motivation

`TelcoHealthCheck` is currently declared `Namespaced`. Since the operator already uses a `ClusterRole`/`ClusterRoleBinding`, watches all namespaces by default in `main.go`, and manages inherently cluster-scoped resources (ACM `ManagedCluster`), there is no technical reason to keep the CR namespace-scoped. Making it cluster-scoped simplifies deployment (one CR per cluster, no namespace needed) and aligns with the operator's actual access pattern.

---

## Changes Required

### 1. Kubebuilder marker — `api/v1alpha1/telcohealthcheck_types.go` (line 103)

Change:
```go
// +kubebuilder:resource:scope=Namespaced,shortName=thc
```
to:
```go
// +kubebuilder:resource:scope=Cluster,shortName=thc
```

Then run `make manifests` to regenerate `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml` (line 17 `scope: Namespaced` → `scope: Cluster`).

### 2. Remove stale `owner-namespace` label — `internal/controller/agenticrun.go` (line 80)

For a cluster-scoped CR `thc.Namespace` will always be `""`. Remove this label from the AgenticRun object:
```go
"telco-anomaly.io/owner-namespace": thc.Namespace,  // DELETE
```
Replace with `"telco-anomaly.io/owner-name": thc.Name` if a provenance label is still desired.

### 3. Fix `alertReceiverURL` fallback — `internal/controller/telcohealthcheck_controller.go` (lines 259–268)

The helper falls back to `thc.Namespace` when `OperatorNamespace` is empty:
```go
ns := r.OperatorNamespace
if ns == "" {
    ns = namespace  // thc.Namespace — will be "" for cluster-scoped CR
}
```
Remove the fallback branch. The `--operator-namespace` flag already defaults to `"telco-healthcheck-system"` in `main.go`, so `r.OperatorNamespace` is always non-empty in practice.

---

## What Does NOT Need to Change

- **RBAC** — already `ClusterRole`/`ClusterRoleBinding`; no namespace restriction on the TelcoHealthcheck rules.
- **Manager setup** (`cmd/controller/main.go`) — no `DefaultNamespaces` restriction; already watches cluster-wide.
- **`client.Get` with `req.NamespacedName`** — works correctly for cluster-scoped resources (namespace field is simply empty).
- **`client.List` calls** — no namespace filter in either the controller or the alert receiver handler; already namespace-agnostic.
- **`mapManagedClusterToTelcoHealthchecks`** — `ObjectKeyFromObject` for a cluster-scoped object returns `{Namespace:"", Name:"..."}`, which is correct.
- **Operator deployment manifests** (`config/manager/`, assets YAMLs, `operatorNamespace` constants) — these reference the operator's own deployment namespace (`telco-healthcheck-system`), not the CR's namespace; unaffected.

---

## Execution Order

1. Edit `api/v1alpha1/telcohealthcheck_types.go` line 103.
2. Run `make manifests` (regenerates CRD YAML and RBAC).
3. Edit `internal/controller/agenticrun.go` line 80 (remove/replace `owner-namespace` label).
4. Edit `internal/controller/telcohealthcheck_controller.go` lines 259–268 (remove `thc.Namespace` fallback in `alertReceiverURL`).
5. Run `make build` and `make test` to confirm nothing is broken.

---

## Verification

- `kubectl apply -f config/crd/bases/ran.openshift.io_telcohealthchecks.yaml` and confirm `kubectl get crd telcohealthchecks.ran.openshift.io -o jsonpath='{.spec.scope}'` outputs `Cluster`.
- Create a CR without a namespace (`kubectl apply -f <cr.yaml>`) and confirm it is accepted.
- `kubectl get thc` (no `-n` flag) lists the CR.
- Deploy the operator and verify the reconcile loop runs without errors in the logs.
- Run `make test` — all existing unit tests should pass.
