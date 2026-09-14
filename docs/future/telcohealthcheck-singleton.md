# Plan: Convert `TelcoHealthcheck` to a cluster-scoped singleton

**Status:** Proposed (not yet implemented).

**Supersedes:** [`cluster-scoped-crd.md`](cluster-scoped-crd.md) — that doc only covers the
plain Namespaced→Cluster step. This plan extends it with a validating webhook, a canonical
name, single-CR simplification, and OpenShift service-CA TLS.

## Motivation

`TelcoHealthcheck` is declared `Namespaced`, but the operator already uses a
`ClusterRole`/`ClusterRoleBinding`, watches cluster-wide in `main.go`, and manages
inherently cluster-scoped resources (ACM `ManagedCluster`). Nothing about the design
requires a per-namespace instance. Making it a **cluster-scoped singleton** (one CR,
a fixed well-known name) removes the ambiguity of multiple CRs contending over the same
shared Thanos ConfigMap / AlertManager Secret and matches the "configured via a single
resource" intent already stated in the README.

## Confirmed decisions

- **Scope:** `Namespaced` → `Cluster`.
- **Singleton enforcement:** validating admission webhook that guards **create only**.
- **Canonical name:** fixed, well-known CR name `telco-healthcheck` (shared constant).
- **Multi-CR code paths:** simplify to single-CR (controller + alert receiver).
- **Webhook TLS:** OpenShift **service CA** (auto-issued + auto-rotated serving cert).
  No external cert generation, no committed cert secret.

## Current state (references)

- CR marker: `api/v1alpha1/telcohealthcheck_types.go:105` — `+kubebuilder:resource:scope=Namespaced,shortName=thc`.
- CRD YAML: `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml:17` — `scope: Namespaced`.
- Controller multi-CR paths:
  - log-level sync via `List` + `IsDebugLevel`: `telcohealthcheck_controller.go:80-89`.
  - enqueue-all on `ManagedCluster` events: `mapManagedClusterToTelcoHealthchecks` at `telcohealthcheck_controller.go:285-304`.
  - `alertReceiverURL` falls back to `thc.Namespace`: `telcohealthcheck_controller.go:259-269`, called at `:125`.
  - `IsDebugLevel` (exported, becomes dead code): `api/v1alpha1/telcohealthcheck_types.go:16-24`.
- Alert receiver multi-CR path: `getMonitoredClusters` (List + union): `internal/alertreceiver/handler.go:201-234`.
- Stale label: `telco-anomaly.io/owner-namespace` = `thc.Namespace`: `internal/controller/agenticrun.go:80`.
- RBAC already grants `get;list` on `telcohealthchecks` to the controller SA: `config/rbac/role.yaml:61-70`.
- Manager Deployment (webhook host): `config/manager/manager.yaml`.
- Sample CR carries a namespace: `config/samples/ran_v1alpha1_telcohealthcheck.yaml:5`, `README.md:51`.
- No kustomization in the repo; deployment is plain `kubectl apply -f config/...` via Makefile `deploy`/`undeploy`.

---

## Workstream A — API & CRD (cluster-scoped)

1. `api/v1alpha1/telcohealthcheck_types.go:105` — change marker to
   `// +kubebuilder:resource:scope=Cluster,shortName=thc`.
2. Same file — add a canonical-name constant, e.g.:
   ```go
   // TelcoHealthcheckCanonicalName is the required name of the singleton CR.
   const TelcoHealthcheckCanonicalName = "telco-healthcheck"
   ```
3. Run `make manifests` → `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml:17`
   becomes `scope: Cluster`.
4. No struct fields change, so deepcopy is unaffected — run `make generate` to be safe.

## Workstream B — Validating webhook (singleton, create-only)

New package `internal/webhook/`:

1. `telcohealthcheck_webhook.go` — an `admission.CustomValidator` for `TelcoHealthcheck`.
   On **CREATE**, reject any object whose `name != TelcoHealthcheckCanonicalName` with an
   error that names the expected value.
   - Guarantees at most one instance: the API server already returns `AlreadyExists` for a
     duplicate canonical create, and object names are immutable (no update guard needed).
2. `telcohealthcheck_webhook_test.go` — build `webhook.Admission` requests; assert canonical
   name is allowed, non-canonical name is denied with the expected message.

Wiring in `cmd/controller/main.go`:

3. Add a webhook server to the manager options:
   ```go
   WebhookServer: webhook.NewServer(webhook.Options{Port: 9443})
   ```
   (controller-runtime's default `CertDir` is `/tmp/k8s-webhook-server/serving-certs`.)
   Register the handler at `/validate-ran-openshift-io-telcohealthcheck`.
   No new flag — the cert dir is the default and is supplied by the mounted Secret.

### TLS via OpenShift service CA (no external cert tooling)

OpenShift's service CA operator issues + auto-rotates the serving cert when a Service is
annotated. This removes `hack/gen-webhook-cert.sh`, the `make webhook-cert` target, and any
committed cert secret.

4. `config/webhook/webhook-service.yaml` — Service `telco-anomaly-webhook` in
   `telco-healthcheck-system`, selector `app: telco-anomaly-controller` (matches the
   controller Deployment), port `443` → targetPort `9443`, with annotation:
   ```yaml
   metadata:
     annotations:
       service.beta.openshift.io/serving-cert-secret-name: telco-anomaly-webhook-cert
   ```
   The operator writes `tls.crt`, `tls.key`, `ca.crt`, `ca-bundle.crt` into a Secret named
   `telco-anomaly-webhook-cert` (same namespace) and auto-rotates before expiry.
5. `config/webhook/validatingwebhookconfiguration.yaml` — one webhook:
   - `clientConfig.service`: `{name: telco-anomaly-webhook, namespace: telco-healthcheck-system, path: /validate-ran-openshift-io-telcohealthcheck, port: 443}`.
   - `clientConfig.caBundle`: empty — kube-apiserver trusts service-CA-signed certs for
     webhooks. (Fallback if a given version does not: base64 the `ca-bundle.crt` from the
     auto-created `telco-anomaly-webhook-cert` Secret. Verify on a live cluster.)
   - `rules`: `apiGroups: [ran.openshift.io]`, `apiVersions: [v1alpha1]`,
     `resources: [telcohealthchecks]`, `operations: [CREATE]`.
   - `sideEffects: None`, `failurePolicy: Fail`, `admissionReviewVersions: [v1]`.
6. `config/manager/manager.yaml` — mount Secret `telco-anomaly-webhook-cert` read-only at
   `/tmp/k8s-webhook-server/serving-certs`; add container port `9443` (webhook).

**RBAC:** no change — the controller SA already has `get;list` on `telcohealthchecks` and
the webhook runs in the controller binary under the same SA.

## Workstream C — Controller single-CR simplification

`internal/controller/telcohealthcheck_controller.go`:
1. `:80-89` log-level sync — replace the `List` + `IsDebugLevel` with a direct check of
   `thc.Spec.LogLevel`.
2. `:285-304` `mapManagedClusterToTelcoHealthchecks` — return a single request for the
   canonical name instead of listing all CRs.
3. `:259-269` `alertReceiverURL` — drop the `thc.Namespace` fallback and the now-unused
   `namespace` parameter; keep `OperatorNamespace`/`AlertReceiverSvcURL` as the source.
   Update the call site at `:125`.
4. `internal/controller/agenticrun.go:80` — remove the `telco-anomaly.io/owner-namespace`
   label (always empty when cluster-scoped); keep `telco-anomaly.io/healthcheck-ref`.
5. `api/v1alpha1/telcohealthcheck_types.go:16-24` — remove `IsDebugLevel` (dead after C.1).

## Workstream D — Alert receiver single-CR

`internal/alertreceiver/handler.go`:
1. `:201-234` `getMonitoredClusters` — replace `List` + union over all CRs with a single
   `Get` of the canonical CR; derive `monitoredClusters` from its
   `status.monitoredClusters` and the log level from its `spec.logLevel`.

## Workstream E — Tests

1. `internal/controller/telcohealthcheck_controller_test.go`
   - `makeTelcoHealthcheck` (`:19-38`) — drop the `namespace` arg; use the canonical name.
   - All CR `types.NamespacedName{Name, Namespace: "default"}` become name-only.
   - `TestMapManagedClusterToTelcoHealthchecks` (`:83-95`) — expect a single request for one
     canonical CR (not two CRs in two namespaces).
2. `internal/alertreceiver/handler_test.go`
   - `makeTHCWithMonitoredClusters` (`:36-42`) — drop the `namespace` arg; use the canonical name.
3. `api/v1alpha1/telcohealthcheck_types_test.go:16-17` — drop the now-meaningless
   `Namespace: "default"`.
4. New `internal/webhook/telcohealthcheck_webhook_test.go` (Workstream B.2).

## Workstream F — Manifests, samples & docs

1. `config/manager/manager.yaml` — add webhook port `9443`, mount the cert Secret (B.6).
2. `Makefile`
   - `deploy`/`undeploy` — include `config/webhook/` (apply after RBAC, before manager;
     delete in reverse).
   - No `webhook-cert` target (service CA handles certs).
3. `config/samples/ran_v1alpha1_telcohealthcheck.yaml:5` — remove `namespace`; set
   `name: telco-healthcheck`.
4. `README.md:51` — remove `namespace` from the sample; note the CR is a cluster-scoped
   singleton with the canonical name.
5. `docs/architecture.md`
   - `:147` — `Scope:` Namespaced → Cluster (singleton).
   - `:182` — sample: no namespace, canonical name.
   - `:336` — remove the `telco-anomaly.io/owner-namespace` label row.
   - Update reconcile steps (log-level sync, `ManagedCluster` enqueue) to single-CR.
   - Document the webhook, canonical name, service-CA cert, and `9443` webhook port.
6. `AGENTS.md:53` — "Namespace-scoped" → "Cluster-scoped singleton (canonical name
   `telco-healthcheck`)".
7. `docs/future/cluster-scoped-crd.md` — mark superseded by this document.

## Verification

1. `make generate && make manifests && make build && make vet && make lint && make test`
   — all green.
2. On an OpenShift cluster: apply CRD → RBAC → `config/webhook/` → manager. Confirm:
   - `kubectl get crd telcohealthchecks.ran.openshift.io -o jsonpath='{.spec.scope}'` → `Cluster`.
   - Service CA operator creates Secret `telco-anomaly-webhook-cert`; controller reaches
     `Running`.
   - `kubectl apply` a CR named `telco-healthcheck` (no namespace) → **accepted**.
   - `kubectl apply` a CR with any other name → **rejected** by the webhook.
   - `kubectl get thc` (no `-n`) lists the CR; operator logs show a clean reconcile.

## Risks / open items

- **CRD scope change is irreversible on a live cluster.** You cannot flip an existing
  Namespaced CRD to Cluster in place; the CRD (and any existing namespaced CRs) must be
  deleted and re-applied. Plan the deployment/migration ordering accordingly.
- **`failurePolicy: Fail`** — while the webhook is unavailable (e.g., the service-CA Secret
  is not yet provisioned on a fresh install), CR creation is blocked. It self-heals once the
  Secret exists and the controller is up. Choose `Ignore` instead if availability must win
  over strict singleton enforcement.
- **Deployment ordering** — the webhook Service must be applied so the cert Secret exists
  before the controller pod starts (it otherwise waits in `ContainerCreating` on the volume
  mount). `make deploy` ordering (RBAC → `config/webhook/` → manager) handles this.
- **Webhook makes the operator required for CR creation** (no longer a no-op API). This is
  the intended singleton behavior but is a behavioral change worth noting.
