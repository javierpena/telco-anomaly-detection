# OpenShift Console Plugin for TelcoHealthCheckRun

## Background

The operator creates `TelcoHealthCheckRun` resources on the hub cluster as an audit trail for
every `AgenticRun` dispatched to a spoke. This plugin surfaces those records in the OpenShift
console as a dedicated "Telco Healthcheck" section, replacing `kubectl get thcr` for day-to-day
inspection.

**Target OpenShift version:** 4.22+  
**Plugin location:** `console-plugin/` at the project root  
**Namespace scope:** `telco-healthcheck-system` (hardcoded — no namespace picker)

---

## Directory Layout

```
console-plugin/
├── package.json               # npm metadata + consolePlugin block
├── tsconfig.json
├── webpack.config.ts          # Module Federation via ConsoleRemotePlugin
├── .eslintrc.js
├── nginx.conf                 # nginx TLS server block (copied into image at build time)
├── console-extensions.json    # Declarative extension manifest
└── src/
    ├── plugin.ts              # Webpack entry point
    ├── components/
    │   ├── TelcoHealthCheckRunList.tsx   # Page component + watch hook
    │   ├── TelcoHealthCheckRunRow.tsx    # Table row + kebab menu
    │   ├── columns.ts                    # Column definitions
    │   └── DeleteModal.tsx              # Confirm-then-delete modal
    ├── types/
    │   └── TelcoHealthCheckRun.ts       # TypeScript interface matching the CRD
    └── utils/
        └── links.ts           # ACM URL builder functions
```

**Project root** (alongside existing `Dockerfile.controller`, `Dockerfile.alertreceiver`):

```
Dockerfile.consoleplugin       # Multi-stage: node build + nginx serve
```

Kubernetes manifests (canonical location, not duplicated inside `console-plugin/`):

```
config/consoleplugin/
├── consoleplugin.yaml
├── deployment.yaml
├── service.yaml
├── serviceaccount.yaml
├── clusterrole.yaml
└── clusterrolebinding.yaml
```

---

## Plugin Manifest (`console-extensions.json`)

Three extensions are registered:

| Extension type | Purpose |
|---|---|
| `console.navigation/section` | Adds "Telco Healthcheck" sidebar section in the admin perspective |
| `console.navigation/href` | Adds "Runs" nav item linking to `/telco-healthcheck/runs` |
| `console.page/route` | Mounts `TelcoHealthCheckRunList` at `/telco-healthcheck/runs` |

The `$codeRef` in the route extension references the `exposedModules.TelcoHealthCheckRunList`
entry in `package.json`.

**OCP 4.22 note:** `perspective: "admin"` confines entries to the Administrator view. Verify the
`insertAfter` nav section ID against the running console — it should reference a built-in section
ID such as `workloads`.

---

## TypeScript Types (`src/types/TelcoHealthCheckRun.ts`)

Extends the SDK's `K8sResourceCommon` (provides `metadata`, `apiVersion`, `kind`):

```ts
interface TelcoHealthCheckRunStatus {
  agenticRunName: string
  clusterName: string
  triggeredBy: 'alert' | 'periodicHealthCheck'
  trigger: string
}
interface TelcoHealthCheckRun extends K8sResourceCommon {
  status: TelcoHealthCheckRunStatus
}
```

Also exports the `GroupVersionKind` constant used by watch hooks and delete calls:

```ts
const TelcoHealthCheckRunGVK = {
  group: 'ran.openshift.io',
  version: 'v1alpha1',
  kind: 'TelcoHealthCheckRun',
}
```

---

## ACM URL Builders (`src/utils/links.ts`)

Two pure functions:

| Function | Returns |
|---|---|
| `clusterUrl(name)` | `/multicloud/infrastructure/clusters/details/${name}/overview` |
| `agenticRunUrl(cluster, runName)` | `/multicloud/search?filters={"textsearch":"kind%3AAgenticRun%20cluster%3A${cluster}%20name%3A${runName}"}` |

The filter string must match the encoding ACM search expects. Verify against a live ACM instance
during implementation.

---

## React Components

### `TelcoHealthCheckRunList.tsx`

- Calls `useK8sWatchResource<TelcoHealthCheckRun[]>` (SDK hook) scoped to
  `telco-healthcheck-system`.
- Renders a PatternFly 5 `PageSection` with a title and `VirtualizedTable` from the SDK.
- Passes `loaded`, `loadError`, `columns`, `data`, and `Row={TelcoHealthCheckRunRow}` to the
  table.
- Shows a `Spinner` while loading and an inline `Alert` on error.

### `columns.ts`

Exports `TableColumn<TelcoHealthCheckRun>[]`:

| id | Title | Sort field |
|---|---|---|
| `name` | Name | `metadata.name` |
| `clusterName` | Cluster | `status.clusterName` |
| `triggeredBy` | Triggered By | `status.triggeredBy` |
| `trigger` | Trigger | `status.trigger` |
| `agenticRunName` | AgenticRun | `status.agenticRunName` |
| `age` | Age | `metadata.creationTimestamp` |
| `kebab` | _(empty)_ | n/a |

### `TelcoHealthCheckRunRow.tsx`

Renders one `TableData` per column:

| Column | Cell content |
|---|---|
| `name` | Plain text: `obj.metadata.name` |
| `clusterName` | `<a>` linking to `clusterUrl(obj.status.clusterName)` |
| `triggeredBy` | PF5 `Label` — blue for `periodicHealthCheck`, red for `alert` |
| `trigger` | Plain text |
| `agenticRunName` | `<a>` linking to `agenticRunUrl(...)` |
| `age` | SDK `<Timestamp timestamp={obj.metadata.creationTimestamp}>` |
| `kebab` | Kebab toggle opening `DeleteModal` |

### `DeleteModal.tsx`

- PF5 `Dropdown` (three-dot kebab) with a single "Delete TelcoHealthCheckRun" item.
- On click: opens a PF5 `Modal` with the resource name and a "Delete" confirmation button.
- On confirm: calls `k8sDelete({ model: TelcoHealthCheckRunModel, resource: obj })` from the SDK.
  If the SDK exposes `ResourceDeleteModal`, use it instead of a custom modal.
- On success: the parent's `useK8sWatchResource` reflects the deletion automatically.
- Error state shown inline in the modal via a PF5 `Alert`.

---

## Kubernetes Manifests (`config/consoleplugin/`)

### `consoleplugin.yaml`

```yaml
apiVersion: console.openshift.io/v1
kind: ConsolePlugin
metadata:
  name: telco-healthcheck-plugin
spec:
  displayName: Telco Healthcheck Plugin
  backend:
    type: Service
    service:
      name: telco-healthcheck-plugin
      namespace: telco-healthcheck-system
      port: 9443
      basePath: /
  proxy: []
```

### `service.yaml`

ClusterIP service on port 9443. Annotated with
`service.beta.openshift.io/serving-cert-secret-name: telco-healthcheck-plugin-cert` so the
OpenShift service CA auto-provisions the TLS certificate secret.

### `deployment.yaml`

| Field | Value |
|---|---|
| Namespace | `telco-healthcheck-system` |
| Image | `$(CONSOLEPLUGIN_IMAGE)` |
| Replicas | 1 |
| Port | 9443 |
| TLS volume | `telco-healthcheck-plugin-cert` secret → `/etc/tls/private` |
| SecurityContext | `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `capabilities: drop: [ALL]`, `seccompProfile: RuntimeDefault` |
| Resources | requests: 10m CPU / 32Mi; limits: 100m CPU / 128Mi |
| ServiceAccount | `telco-healthcheck-plugin` |

### `clusterrole.yaml`

```yaml
rules:
- apiGroups: ["ran.openshift.io"]
  resources: ["telcohealthcheckruns"]
  verbs: ["get", "list", "watch", "delete"]
```

**Important:** The OCP console proxies k8s API calls as the logged-in user. This ClusterRole must
be bound to any user or group that will access the plugin UI. Hub cluster admins already have
these permissions via `cluster-admin`. For restricted users, use a namespace-scoped `RoleBinding`
targeting `telco-healthcheck-system` instead.

---

## `nginx.conf`

The nginx configuration (stored in `console-plugin/nginx.conf`, copied into the image during
build) must:

- Listen on port 9443 with TLS.
- Reference the service CA certificate at `/etc/tls/private/tls.crt` and `tls.key`.
- Serve `/usr/share/nginx/html` as the document root.
- Use `try_files $uri $uri/ /index.html` for SPA routing.
- Set `Cache-Control: immutable` for hashed asset files; `no-cache` for `index.html`.

Use `nginxinc/nginx-unprivileged` or `registry.access.redhat.com/ubi9/nginx-124` as the base
image — both support non-root operation.

---

## `Dockerfile.consoleplugin` (project root)

Multi-stage build. Build context is the project root.

**Stage 1 — Node builder** (`node:20-alpine`):
1. Copy `console-plugin/package.json` and `package-lock.json`.
2. Run `npm ci --legacy-peer-deps`.
3. Copy the rest of `console-plugin/`.
4. Run `npm run build` → outputs to `console-plugin/dist/`.

**Stage 2 — nginx server** (`registry.access.redhat.com/ubi9/nginx-124`):
1. Copy `--from=builder /build/dist` to `/usr/share/nginx/html`.
2. Copy `console-plugin/nginx.conf` to `/etc/nginx/nginx.conf`.
3. Expose port 9443.
4. `CMD ["nginx", "-g", "daemon off;"]`

---

## Makefile Targets

Add to `Makefile`:

| Target | Action |
|---|---|
| `console-plugin-install` | `cd console-plugin && npm ci --legacy-peer-deps` |
| `console-plugin-build` | `cd console-plugin && npm run build` |
| `console-plugin-lint` | `cd console-plugin && npm run lint` |
| `console-plugin-start` | `cd console-plugin && npm run start` (dev server, port 9001) |
| `container-build-consoleplugin` | Build `Dockerfile.consoleplugin` tagged as `$(CONSOLEPLUGIN_IMAGE)` |
| `container-push-consoleplugin` | Push `$(CONSOLEPLUGIN_IMAGE)` |
| `install-consoleplugin` | `kubectl apply -f config/consoleplugin/` |
| `undeploy-consoleplugin` | `kubectl delete -f config/consoleplugin/ --ignore-not-found` |
| `enable-consoleplugin` | Patch `console.operator.openshift.io/cluster` to add `telco-healthcheck-plugin` to `.spec.plugins` |

`container-build` and `container-push` extend to include the consoleplugin targets. `deploy` does
NOT automatically install the plugin — it is optional UI, kept separate.

---

## Documentation Updates

- `docs/architecture.md`: Add the Console Plugin row to the components table; document the
  ConsolePlugin CR, Service, RBAC, and ACM link patterns.
- `steps.md`: Add Phase 11 — Console Plugin.

---

## Verification

### Local development (against a real cluster)

```bash
oc login <hub-cluster>
make console-plugin-install
make console-plugin-start          # webpack-dev-server on http://localhost:9001
```

Run the console bridge with the plugin flag:

```bash
BRIDGE_PLUGINS=telco-healthcheck-plugin=http://localhost:9001 ./bridge \
  --k8s-auth=openshift \
  --k8s-host-url=<api-url>
```

Open `http://localhost:9000` — verify "Telco Healthcheck / Runs" appears in the sidebar.

### Cluster-deployed verification

```bash
make container-build-consoleplugin container-push-consoleplugin
make install-consoleplugin
make enable-consoleplugin
# Confirm plugin loaded:
oc get consoleplugins.console.openshift.io telco-healthcheck-plugin -o yaml | grep -A3 conditions
# Confirm TLS cert secret created:
oc get secret telco-healthcheck-plugin-cert -n telco-healthcheck-system
```

### Functional checks

1. Table populates after creating a test `TelcoHealthCheckRun` (or triggering a real health check).
2. Cluster column links navigate to the correct ACM cluster details page.
3. AgenticRun column links surface the correct AgenticRun in ACM search.
4. Kebab → Delete shows a confirmation modal; confirming removes the row and the object.
5. `oc get thcr -n telco-healthcheck-system` confirms deletion.
