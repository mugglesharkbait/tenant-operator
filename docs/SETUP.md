# Setup Guide — Build `tenant-operator` from Scratch (macOS + minikube)

A step-by-step, reproducible guide to building this Kubernetes operator locally on
macOS (Apple Silicon) using **minikube**. Each section is a phase; run them in order.

> This guide is written as we actually build the project, so the commands match what
> was run. For the conceptual overview of *what* the operator does, see
> [tenant-operator-README.md](tenant-operator-README.md). For a live status log, see
> [progress.md](progress.md).

---

## Prerequisites

You need Homebrew and Docker Desktop. Everything else is installed below.

| Tool | Purpose |
|---|---|
| Homebrew | Package manager used to install the tools below |
| Docker Desktop | Container runtime that backs minikube's `docker` driver |
| Go | Builds the operator |
| kubectl | Talks to the cluster |
| minikube | Runs a local single-node Kubernetes cluster |
| kubebuilder | Scaffolds the operator project |

---

## Phase 1 — Install tooling

```bash
brew install kubebuilder minikube
```

This installs `kubebuilder` and `minikube`, and pulls in `go` and `kubernetes-cli`
(kubectl) as dependencies. Verify:

```bash
kubebuilder version    # v4.14.0
minikube version       # v1.38.1
go version             # go1.26.x
kubectl version --client
```

---

## Phase 2 — Start the cluster

1. Launch **Docker Desktop** and wait until it reports "running".
2. Start minikube on the docker driver:

   ```bash
   minikube start --driver=docker
   ```

3. Confirm the cluster is up and `kubectl` points at it:

   ```bash
   kubectl get nodes
   # NAME       STATUS   ROLES           AGE   VERSION
   # minikube   Ready    control-plane   ...   v1.xx.x
   ```

Useful later:
- `minikube stop` — shut the cluster down (frees RAM) without deleting it.
- `minikube start` — bring it back (fast after the first time).
- `minikube delete` — remove the cluster entirely.

---

## Phase 3 — Scaffold the project

```bash
kubebuilder init --domain example.io --repo github.com/your-github-username/tenant-operator
kubebuilder create api --group platform --version v1alpha1 --kind Tenant --resource --controller
```

- `--domain example.io` → the API group suffix, giving GVK `platform.example.io/v1alpha1/Tenant`.
- `--repo ...` → the **Go module path** (see below).
- `create api ... --resource --controller` → generates both the API types and a controller
  (the `--resource`/`--controller` flags skip the interactive yes/no prompts).

`init` generates the project skeleton (`go.mod`, `Makefile`, `Dockerfile`, `PROJECT`,
`cmd/main.go`, `config/**`) and downloads dependencies. `create api` adds
`api/v1alpha1/tenant_types.go` and `internal/controller/tenant_controller.go` — the two
files we edit in Phases 4–5.

### What is the Go module path (the `--repo` value)?

`go mod init` writes one line into `go.mod`:

```
module github.com/your-github-username/tenant-operator
```

That string is the module's **identity**, and it does two jobs:

1. **Prefix for every internal import.** The project is several packages
   (`api/v1alpha1`, `internal/controller`, `cmd`). When the controller needs the `Tenant`
   type it imports it by full path:
   ```go
   import platformv1alpha1 "github.com/your-github-username/tenant-operator/api/v1alpha1"
   ```
   kubebuilder bakes this prefix into every generated `.go` file — it is how your code
   refers to itself.

2. **How other projects would fetch yours.** `go get github.com/your-github-username/tenant-operator`
   turns the path into a URL and clones it. This is the *only* case where the path must
   match a real GitHub repo.

**It does not require GitHub to exist for local builds.** Go sees the `module` line, treats
that prefix as "this is me," and resolves those imports to local files. The network is only
used for third-party dependencies (controller-runtime, k8s libraries), never for your own
module path.

**Why a stable placeholder?** Changing the module path later means editing `go.mod` *and*
the import line in every `.go` file that references your packages. Using the placeholder
`your-github-username` makes publishing later a single find-replace of that one segment.

> Reminder for when you publish: replace `your-github-username` everywhere with your real
> GitHub username, e.g. `grep -rl your-github-username . | xargs sed -i '' 's/your-github-username/<real>/g'`.

## Phase 4 — Define the Tenant API

Edit `api/v1alpha1/tenant_types.go`. Replace the scaffold's placeholder `Foo` field and
generic status with the real API:

- **`TenantSpec`**: `displayName` (optional), `owners` (`[]string`,
  `+kubebuilder:validation:MinItems=1`, required), `resourceQuota` (new
  `TenantResourceQuota` type), `networkIsolation` (`bool`, `+kubebuilder:default=true`).
- **`TenantResourceQuota`**: `cpu` (default `"4"`), `memory` (default `"8Gi"`), `pods`
  (`int32`, default `20`).
- **`TenantStatus`**: `namespace` (string), `observedGeneration` (int64), `conditions`
  (`[]metav1.Condition`).
- **Markers on the `Tenant` type**:
  ```go
  // +kubebuilder:object:root=true
  // +kubebuilder:subresource:status
  // +kubebuilder:resource:scope=Cluster
  // +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
  // +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
  ```
  `scope=Cluster` matters — the scaffold defaults to `Namespaced`, but a Tenant provisions
  a cluster-scoped Namespace, so the CRD itself must be cluster-scoped.

> A full `go build` will fail at this point because the generated
> `zz_generated.deepcopy.go` still references the removed `Foo` field. That is expected;
> `make generate` (Phase 6) regenerates it. Verify syntax now with
> `gofmt -e api/v1alpha1/tenant_types.go`.

## Phase 5 — Implement the controller

Edit `internal/controller/tenant_controller.go`. The `Reconcile` function re-derives the full
desired state on every run (level-triggered + idempotent) and ensures, each via
`controllerutil.CreateOrUpdate` with an owner reference back to the Tenant:

1. **Namespace** named after the Tenant, labelled `platform.example.io/tenant=<name>`.
2. **ResourceQuota** (`tenant-quota`) — `cpu`/`memory` parsed with `resource.ParseQuantity`,
   `pods` via `resource.NewQuantity`.
3. **NetworkPolicy** (`tenant-default-deny`) — only when `networkIsolation` is true: deny all
   ingress + all egress except DNS (UDP/TCP 53). When false, the policy is deleted if present.
4. **RoleBinding** (`tenant-owners`) — binds the built-in `edit` ClusterRole to the owners.
   Owner strings are parsed: `user:` → User subject, `group:` → Group subject.
5. **Status** — sets `Ready=True`, `namespace`, `observedGeneration`, then `Status().Update`.
   On any step failure, `markFailed` records `Ready=False` with the reason and requeues.

Also added:
- **RBAC markers** for namespaces, resourcequotas, networkpolicies, rolebindings (these
  generate the operator's ClusterRole in Phase 6).
- **`SetupWithManager`** with `.Owns(...)` on all four managed types so changes to them
  trigger a reconcile — this is what makes self-healing work.

Verify it compiles (deepcopy must be regenerated first, since Phase 4 removed `Foo`):

```bash
make generate      # regenerate zz_generated.deepcopy.go
go build ./...     # should succeed
```

## Phase 6 — Generate manifests + install the CRD

```bash
make manifests   # regenerate CRD YAML + operator ClusterRole from the Go markers
make install     # apply the CRD into the cluster
```

- `make manifests` regenerates `config/crd/bases/platform.example.io_tenants.yaml` (the CRD,
  now `scope: Cluster` with the owners/quota/networkIsolation schema and NAMESPACE/READY
  print columns) and `config/rbac/role.yaml` (the operator ClusterRole from the
  `+kubebuilder:rbac` markers).
- `make install` uses kustomize to apply the CRD into the current cluster.

Verify:

```bash
kubectl get crd tenants.platform.example.io   # should exist
kubectl get tenants                            # "No resources found" (valid, empty)
```

To remove it later: `make uninstall`.

## Phase 7 — Run the operator + verify

Needs two terminals (`make run` blocks).

1. **Write a real sample** in `config/samples/platform_v1alpha1_tenant.yaml` — `team-falcon`
   with owners, quota (cpu "8" / memory "16Gi" / pods 40), `networkIsolation: true`. No
   `namespace:` in metadata (the Tenant is cluster-scoped).

2. **Run the operator (Terminal 1):**
   ```bash
   make run    # runs locally against the current kubecontext; blocks while watching
   ```
   Wait for `Starting workers`.

3. **Apply the Tenant (Terminal 2):**
   ```bash
   kubectl apply -f config/samples/platform_v1alpha1_tenant.yaml
   ```
   Terminal 1 prints `reconciled tenant ... namespace=team-falcon` (often a few times as it
   converges — each owned-resource create + the status update re-triggers a reconcile).

4. **Verify:**
   ```bash
   kubectl get tenants                                          # READY=True
   kubectl get ns team-falcon
   kubectl get resourcequota,networkpolicy,rolebinding -n team-falcon
   kubectl describe resourcequota tenant-quota -n team-falcon   # Hard: cpu 8, memory 16Gi, pods 40
   kubectl get tenant team-falcon -o yaml | grep -A12 "status:" # Ready=True, observedGeneration 1
   ```

5. **Self-heal test:**
   ```bash
   kubectl delete resourcequota tenant-quota -n team-falcon
   kubectl get resourcequota -n team-falcon   # recreated within ~1s (AGE resets)
   ```

6. **Cascade-delete test:**
   ```bash
   kubectl delete tenant team-falcon
   kubectl get ns team-falcon                 # NotFound — GC'd via owner references
   ```

7. **Stop the operator:** `Ctrl+C` in Terminal 1.

Notes:
- Each reconcile log line shows `namespace=""` (the request namespace — empty because Tenant
  is cluster-scoped) and a separate `namespace=team-falcon` (our log key for the provisioned ns).
- `displayName` is stored in the spec but not yet consumed by the controller (roadmap item).
