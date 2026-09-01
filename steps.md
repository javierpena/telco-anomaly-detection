# Telco Anomaly Detection Operator – Implementation Tracker

## Phase 1: Project Scaffolding ✅

- [x] Create `go.mod` with module `github.com/javierpena/telco-anomaly-detection`
- [x] Create `Makefile` (build, container-build, lint, test, generate, manifests)
- [x] Create `hack/boilerplate.go.txt` (license header for code generation)
- [x] Create `steps.md` (this file)
- [ ] Run `go mod tidy` to resolve all indirect dependencies and generate `go.sum`
- [ ] Run `make install-tools` to install `controller-gen` and `golangci-lint`

## Phase 2: CRD and API Types ✅

- [x] Create `api/v1alpha1/groupversion_info.go` — scheme registration
- [x] Create `api/v1alpha1/telcohealthcheck_types.go` — TelcoHealthcheck CRD types
- [x] Create `api/v1alpha1/zz_generated.deepcopy.go` — DeepCopy implementations
- [x] Create `api/v1alpha1/telcohealthcheck_types_test.go` — type validation tests
- [ ] Run `make manifests` to regenerate `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml`
  (the initial YAML is a hand-written draft; the generated version is authoritative)

## Phase 3: Internal Packages ✅

- [x] Create `internal/agenticrun/types.go` — AgenticRun type definitions (local, no external import)
- [x] Create `internal/controller/managedcluster.go` — ManagedCluster discovery and kubeconfig fetch
- [x] Create `internal/controller/managedcluster_test.go`
- [x] Create `internal/controller/alertrules.go` — Thanos ConfigMap management
- [x] Create `internal/controller/alertrules_test.go`
- [x] Create `internal/controller/alertmanager.go` — AlertManager receiver config
- [x] Create `internal/controller/alertmanager_test.go`
- [x] Create `internal/controller/agenticrun.go` — AgenticRun creation on spoke clusters
- [x] Create `internal/controller/agenticrun_test.go`

## Phase 4: Controller ✅

- [x] Create `internal/controller/telcohealthcheck_controller.go` — main reconciler
- [x] Create `internal/controller/telcohealthcheck_controller_test.go`
- [x] Create `cmd/controller/main.go` — controller entrypoint

## Phase 5: Alert Receiver ✅

- [x] Create `internal/alertreceiver/server.go` — HTTP webhook server
- [x] Create `internal/alertreceiver/server_test.go`
- [x] Create `internal/alertreceiver/handler.go` — alert processing logic
- [x] Create `internal/alertreceiver/handler_test.go`
- [x] Create `cmd/alertreceiver/main.go` — alert receiver entrypoint

## Phase 6: RBAC and Deployment Manifests ✅

- [x] Create `config/rbac/serviceaccount.yaml`
- [x] Create `config/rbac/role.yaml`
- [x] Create `config/rbac/rolebinding.yaml`
- [x] Create `config/manager/manager.yaml` — controller Deployment
- [x] Create `config/manager/alertreceiver.yaml` — alert receiver Deployment + Service
- [x] Create `config/crd/bases/ran.openshift.io_telcohealthchecks.yaml` (draft)

## Phase 7: Container Images ✅

- [x] Create `Dockerfile.controller`
- [x] Create `Dockerfile.alertreceiver`
- [x] Create `Dockerfile.skills`

## Phase 8: Alert Rules ✅

- [x] Define actual Thanos alert rules for `hostNetwork` alerts
- [x] Define actual Thanos alert rules for `podNetwork` alerts
- [x] Define actual Thanos alert rules for `hostReservedCPU` alerts
- [x] Update `internal/controller/alertrules.go` with the rule definitions

## Phase 9: AgenticRun Enhancements (Partially complete)

- [x] Replace `spec.request: "test"` with a meaningful prompt for `hostNetwork`, `hostReservedCPU`, and `rds-compliance` check types
- [ ] Replace `spec.request: "test"` with a meaningful prompt for `podNetwork` check type
- [ ] Add actual skill paths to `spec.tools.skills[].paths` once the skills OCI image is finalized
- [x] Add MCP servers to `spec.tools.mcpServers` for `rds-compliance` (`${KUBE_COMPARE_MCP_URL}`)

## Phase 10: RDS Compliance Implementation (Deferred)

- [ ] Define what "RDS compliance" checks entail
- [ ] Implement a dedicated AgenticRun configuration for RDS compliance checks

## Verification Commands

```bash
# After go mod tidy and install-tools:
make build          # both binaries compile
make test           # all tests pass
make lint           # no linter violations
make manifests      # CRD YAML regenerated
make container-build   # all 3 images build
```
