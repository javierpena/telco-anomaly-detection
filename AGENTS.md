# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

Go operator that monitors telco workloads across ACM-managed clusters. It auto-triggers AI-driven health checks (via Lightspeed `AgenticRun` resources) when ACM alerts fire or on a configurable schedule.

**Module:** `github.com/javierpena/telco-anomaly-detection`  
**Go version:** 1.22  
**controller-runtime:** v0.19.0

## Common commands

```bash
make build            # compile both binaries to bin/
make test             # run all tests with -v -count=1
make lint             # golangci-lint
make vet              # go vet

# Single package test
go test ./internal/controller/... -v -count=1 -run TestName

make generate         # regenerate DeepCopy methods (controller-gen)
make manifests        # regenerate CRD YAML and RBAC from markers
make tidy             # go mod tidy

make container-build     # build all 3 images (controller, alertreceiver, skills)
make deploy           # kubectl apply CRD + RBAC + Deployments
make undeploy         # kubectl delete all of the above

# Override registry
make container-build REGISTRY=quay.io/youruser
```

Install tools (controller-gen, golangci-lint):
```bash
make install-tools
```

## Architecture

### Three components

| Component | Entry point | Image |
|---|---|---|
| Controller | `cmd/controller/main.go` | `Dockerfile.controller` |
| Alert receiver | `cmd/alertreceiver/main.go` | `Dockerfile.alertreceiver` |
| Skills OCI image | `skills/` directory | `Dockerfile.skills` |

### CRD: `TelcoHealthcheck` (`ran.openshift.io/v1alpha1`, shortName `thc`)

Namespace-scoped. Key spec fields: `managedClusters` (include/exclude, mutually exclusive), `managedNamespaces`, `alerts.{hostNetwork,podNetwork}`, `periodicHealthChecks.{period,rdsCompliance}`. Status tracks `monitoredClusters`, `lastPeriodicRunTime`, `lastRDSComplianceRunTime`.

### Controller (`internal/controller/`)

Reconciles `TelcoHealthcheck` CRs and watches `ManagedCluster` events (re-enqueues all TelcoHealthchecks on cluster change). Each reconcile:
1. Resolves monitored clusters via `managedcluster.go` (honors include/exclude against ACM `ManagedCluster` list).
2. Reconciles the Thanos alert rules ConfigMap (`thanos-ruler-custom-rules` in `open-cluster-management-observability`).
3. Configures the AlertManager receiver in the `alertmanager-config` Secret (same namespace) to POST to the alert receiver webhook.
4. Triggers periodic `AgenticRun` creation on each spoke cluster when the configured period elapses; requeues for the next due check.

**Key namespaces:**
- Operator: `telco-healthcheck-system`
- ACM Observability: `open-cluster-management-observability`
- AgenticRun target (spoke): `openshift-lightspeed`

**ManagedCluster kubeconfigs:** Secret `<name>-admin-kubeconfig` in namespace `<name>`.

### Alert receiver (`internal/alertreceiver/`)

HTTP server listening on `:8080`. `POST /webhook` accepts AlertManager JSON payloads. For each alert, validates `labels.cluster` against monitored clusters and `labels.alertname` against Thanos rule names, then creates an `AgenticRun` on the matching spoke cluster.

### AgenticRun (`internal/agenticrun/types.go`)

`agentic.openshift.io/v1alpha1` — no published Go client library exists, so objects are created via the unstructured client. Type constants (`Group`, `Version`, `Kind`) live in `internal/agenticrun/types.go`. AgenticRuns are always created in `openshift-lightspeed` on the spoke cluster.

## Code generation

After modifying `api/v1alpha1/telcohealthcheck_types.go`:
- Run `make generate` to regenerate `zz_generated.deepcopy.go`.
- Run `make manifests` to regenerate the CRD YAML in `config/crd/bases/` and RBAC in `config/rbac/`.

The generated CRD YAML is authoritative; the file committed is a hand-written draft from the initial scaffolding.

## Commit conventions

All commits that include Claude-assisted work must carry a co-author trailer:

```
Co-Authored-By: Claude Sonnet 4.6 (1M context) <noreply@anthropic.com>
```

## Design documentation

`docs/architecture.md` is the canonical design document. It covers all components, YAML files, data flows, namespaces, and RBAC. **Update it whenever you change a component, add a new Kubernetes resource, modify the reconcile loop, or alter how AgenticRun objects are built or configured.**

## Implementation status

See `steps.md` for the phase tracker. Phases 1–8 are complete. Deferred work:
- **Phase 9:** Real `AgenticRun` prompts for `podNetwork` (others done); skill paths once OCI image is finalized.
- **Phase 10:** RDS compliance check implementation.
