# Plan: Package the operator for OLM

> **Status:** Proposed — implementation and publication are deferred. This
> document records the packaging work; writing it does not execute the plan.

## Goals and decisions

- Install the operator on an ACM hub running OpenShift **4.22.z or a later
  validated release**, including future minor and major releases (for example,
  5.0). OpenShift 4.22 is the minimum target, not a guarantee that an unreleased
  version is compatible. Validate each new release before advertising support
  or adding it to a version-specific Community Operators catalog.
- Publish an OLM bundle and a **private file-based catalog** first; subsequently
  submit the operator to the **Red Hat Community Operators** catalog. This is a
  community-catalog track, not a Certified Operators submission.
- Install the Subscription, CSV, and operator pods in the fixed namespace
  `telco-healthcheck-system`. The cluster-scoped singleton `TelcoHealthcheck`
  and cluster-wide ManagedCluster watches require an AllNamespaces OperatorGroup.
  Do not advertise installation into arbitrary namespaces.
- **Document prerequisites only:** ACM with classic Multicluster Observability
  on the hub, suitable ManagedClusters and admin kubeconfig Secrets, and
  OpenShift Lightspeed on monitored spokes. Do not introduce OLM dependencies
  or prerequisite probes as part of packaging. Revisit this prerequisite text
  if either proposed MCOA or lightspeed-hub integration is implemented first.

## Current packaging inputs and gaps

The direct-install entry point is `make deploy` in `Makefile`. It applies the
CRDs in `config/crd/bases/`, generated RBAC in `config/rbac/`, the webhook
Service and ValidatingWebhookConfiguration in `config/webhook/`, and two
Deployments plus Services in `config/manager/`. There is no bundle, CSV, or
catalog yet.

The controller and alert receiver are separate binaries, Deployments, and
images. A third image contains skills referenced by embedded ConfigMap assets,
but is not itself a Deployment. Optional RDS compliance starts a
`kube-compare-mcp` Deployment from an embedded manifest; optional record
purging creates a CronJob using an `ose-cli` image currently tagged `v4.22`.
Production release images must account for all of these references. The
existing Deployments and skill references use `:latest`, and the webhook uses
an OpenShift service-CA Secret plus a readiness check that expects a
ValidatingWebhookConfiguration with a fixed name. These assumptions must be
addressed explicitly in the OLM install path.

## Implementation sequence

### 1. Define the release and image contract

1. Select the package name, first semantic version, and default channel; use
   the same package identity for private and Community Operators catalogs.
2. Produce versioned, immutable controller, alert-receiver, and skills images.
   Pin the image references in a release bundle to digests. Make the embedded
   skills references follow the same release: replacing Deployment image tags
   alone will not update the skills image inside controller-managed ConfigMaps.
3. Inventory and pin every other runtime image the operator can create:
   `internal/controller/assets/kube-compare-mcp.yaml` and
   `internal/controller/assets/purge-cronjob.yaml` as well as the images in
   `config/manager/`. Record all images in the bundle/catalog `relatedImages`
   for mirroring and disconnected installations. Check the availability and
   suitability of the `ose-cli:v4.22` image on each later OpenShift release;
   don't assume a 4.22-tagged tool is a cross-major compatibility guarantee.
4. Use a reproducible release input (version and immutable image references)
   for CSV generation and embedded asset substitution. Keep `make deploy`
   useful for development without letting its `:latest` references leak into
   published bundles.

### 2. Build the bundle and CSV

1. Create `bundle/manifests/`, `bundle/metadata/annotations.yaml`, and a
   bundle Dockerfile. Include both generated CRDs:
   `telcohealthchecks.ran.openshift.io` (cluster-scoped) and
   `telcohealthcheckruns.ran.openshift.io` (namespaced).
2. Add a CSV with both CRDs in `spec.customresourcedefinitions.owned`, useful
   descriptions/examples, version and display metadata, the fixed install
   namespace instructions, and `AllNamespaces: true` as the supported install
   mode. Mark other install modes unsupported; an OperatorGroup watching only
   the operator namespace cannot represent the singleton and cluster watches.
3. Put **both** Deployments in the CSV deployment strategy, including their
   probes, resource requests, security contexts, arguments, and shared
   ServiceAccount. Translate the required generated RBAC into CSV
   `permissions`/`clusterPermissions`; account for the shared ServiceAccount,
   cluster-wide informer watches, secrets in ACM namespaces, and optional
   kube-compare RBAC creation. Avoid shipping duplicate independently applied
   Deployments or ClusterRoleBindings in the bundle. Review the resulting
   permission scope and test it under OLM rather than assuming the direct
   `make deploy` ClusterRole is sufficient in a CSV.
4. Decide how to install the metrics and alert-receiver Services alongside the
   CSV. Confirm the chosen bundle manifest mechanism and OLM ownership/lifecycle
   on OpenShift 4.22, including removal and upgrades; neither Service may be
   left dependent on a separate `make deploy` step. Keep Service names and the
   alert-receiver URL stable.

### 3. Make webhook lifecycle compatible with OLM

1. Declare the existing CREATE-only singleton validation rule as an OLM CSV
   validating webhook definition, tied to the controller Deployment. Preserve
   `failurePolicy: Fail`, the admission path, and the `TelcoHealthcheck` scope;
   ensure the rule does not intercept OLM's own APIs.
2. Select **one** certificate/CA owner for OLM installations. Reconcile the
   OLM-managed webhook definition and certificates with the existing
   service-CA annotation, `telco-anomaly-webhook-cert` volume, and readiness
   check in `cmd/controller/main.go`. Do not assume the current fixed
   ValidatingWebhookConfiguration name or Secret mount matches what OLM creates.
   Adjust the serving-cert path and readiness condition for the selected model.
3. Exercise initial installation, CA injection, cert rotation, CSV replacement,
   and uninstall on OpenShift 4.22. Confirm CR creation fails closed only when
   the webhook genuinely cannot serve requests, and that normal startup does
   not deadlock waiting for a configuration or certificate with the wrong name.
   Keep the direct-install webhook manifests consistent with the chosen
   implementation.

### 4. Build and publish a private file-based catalog

1. Source-control the package, channel, and bundle entries in a catalog
   directory (for example, `catalog/telco-anomaly-detection/`). Add the first
   bundle as the channel head, with immutable bundle and `relatedImages`
   references. For later versions, define explicit `replaces`/`skips` or
   `skipRange` edges and keep older installable versions where needed.
2. Add repeatable targets or scripts to build and push the runtime images,
   bundle image, and catalog image; validate the bundle and file-based catalog
   before publication. Treat published bundles as immutable: fix a bad bundle
   with a new version and upgrade edge instead of changing its existing image.
3. Document hub installation with a private `CatalogSource`, the dedicated
   namespace, an AllNamespaces `OperatorGroup`, and a `Subscription`. Include
   image-mirror guidance using the recorded `relatedImages`. Avoid a second
   install of the same operator through `make deploy`.

### 5. Prepare Community Operators publication

1. Follow the current Red Hat Community Operators contribution process and
   file-based-catalog workflow. Supply bundle metadata, examples, source and
   support links, image references, and any required catalog templates or
   `ci.yaml` settings. Run the community and OpenShift catalog validators
   applicable at submission time.
2. Start with the OpenShift 4.22 catalog. Map subsequent OpenShift catalogs
   only after testing each release. Set `com.redhat.openshift.versions` and
   catalog version mappings according to the submission pipeline's *current*
   syntax; do not use a speculative open-ended version range that silently
   claims untested releases. `spec.minKubeVersion` is a Kubernetes constraint,
   not a substitute for OpenShift-version validation.
3. Omit an `olm.maxOpenShiftVersion` constraint unless a demonstrated
   incompatibility justifies blocking a cluster upgrade. If a future major
   OpenShift release changes OLM, supported APIs, ACM, or Lightspeed behavior,
   adapt and release a compatible bundle before claiming that major release.

### 6. Document migration and lifecycle

1. Update `README.md` with OLM install/upgrade/uninstall instructions, the
   fixed namespace, required OperatorGroup, and the documented ACM, MCO,
   Lightspeed, and kubeconfig prerequisites. Update `docs/architecture.md`
   whenever the implementation changes component installation, manifests,
   webhook behavior, RBAC, or reconciliation; this is the canonical design
   document. Record the completed implementation in `steps.md`.
2. Define and test a migration from an existing `make deploy` installation.
   Resolve ownership of existing CRDs, Deployments, RBAC, Services, and the
   webhook before creating a Subscription; do not let OLM and direct manifests
   fight over those resources. Keep the singleton and run records intact.
3. Document precisely what uninstall does to the CRDs and existing CRs, and
   what happens to runtime-managed Thanos/AlertManager changes, ConfigMaps,
   optional kube-compare resources, and run records if the operator is removed
   without first deleting its singleton. Provide an explicit cleanup procedure
   for any resources OLM does not own.

## Verification gates

- Run `make manifests` when source markers or API schemas change; inspect the
  generated CRDs/RBAC and compare them with the CSV. Run `make test`,
  `make vet`, and `make lint` for code changes.
- Validate the bundle with `operator-sdk bundle validate` and the private
  catalog with `opm validate`; run the Community Operators checks required at
  submission time. Inspect all bundled API versions and the actual image
  references, including embedded and optional images.
- Install from the **private CatalogSource** on an OpenShift 4.22 ACM hub with
  the documented prerequisites. Verify the CSV succeeds, both Deployments are
  Ready, both APIs are served, webhook validation works, the receiver Service
  is reachable, the singleton reconciles, and alert-triggered and periodic
  spoke AgenticRuns work. Exercise optional RDS compliance and purging at
  least once to validate their runtime images and RBAC.
- Publish a second bundle in a test catalog and verify an OLM upgrade. Confirm
  both Deployments and embedded skills references advance, the singleton and
  `TelcoHealthCheckRun` records persist, and webhook admission works across
  the transition. Test uninstall and the documented cleanup or retention path.
- Repeat install, upgrade, and API/image compatibility checks on **each**
  OpenShift minor or major release before adding that release to Community
  Operators or advertising support. In particular, recheck OLM webhook
  behavior, removed Kubernetes APIs, ACM observability integration, Lightspeed
  compatibility, and auxiliary images on a future OpenShift 5.0 target.

## References

- [OLM bundles](https://olm.operatorframework.io/docs/tasks/creating-operator-bundle/)
- [OLM file-based catalogs](https://olm.operatorframework.io/docs/reference/file-based-catalogs/)
- [OLM admission webhooks](https://olm.operatorframework.io/docs/advanced-tasks/adding-admission-and-conversion-webhooks/)
- [OLM OperatorGroup install modes](https://olm.operatorframework.io/docs/advanced-tasks/operator-scoping-with-operatorgroups/)
- [Community Operators publishing guidance](https://github.com/redhat-openshift-ecosystem/operator-pipelines/blob/main/docs/README.md)
- [OpenShift catalog criteria](https://github.com/redhat-openshift-ecosystem/operator-pipelines/blob/main/docs/users/packaging-required-criteria-ocp.md)
