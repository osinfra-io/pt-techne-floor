# Factory Floor Local Milestone Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the factory floor end to end on the local docker-desktop stack: Flux Operator + tofu-controller plan two fixture workspaces, a PR gets a Branch Planner comment for the affected workspace only, `floor-api` approves it through agentgateway with an authentik identity, sandbox repoints at the PR and applies, and merging returns sandbox to `main`.

**Architecture:** Flux Operator and tofu-controller run off-mesh in `flux-system`, deployed by a new `tests/integration/floor/` component in `pt-pneuma`. Floor configuration (`GitRepository`, `Terraform`, runner RBAC) is plain Kustomize in `pt-techne-floor/floors/local/`, reconciled by a Flux `Kustomization`. `floor-api` is a Go MCP server (streamable HTTP) in `pt-techne-floor`, the sole writer of `spec.approvePlan`, exposed at `https://floor.localhost` through agentgateway behind the authentik outpost. A new fixture repo `osinfra-io/pt-techne-floor-fixture` holds two tiny OpenTofu roots with a `dependsOn` chain.

**Tech Stack:** Flux Operator 0.62.1 (Flux v2.10.0), tofu-controller 0.16.6, Kustomize, Go 1.26 with `github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0, `sigs.k8s.io/controller-runtime` (unstructured client + fake client), `github.com/google/go-github/v68`, OpenTofu roots in `pt-pneuma/tests/integration` (helm/kubernetes/authentik providers), bash `set -euo pipefail` scripts.

**Spec:** `docs/superpowers/specs/2026-11-15-factory-floor-flux-design.md` (§5.10 defines this milestone; §5.3, §5.4, §5.7, §5.9 bind the behaviour). Evidence: `docs/superpowers/specs/2026-11-15-flux-tofu-controller-spike-findings.md`.

## Global Constraints

- Flux, tofu-controller and Branch Planner run in `flux-system`, **not** labelled `istio.io/dataplane-mode=ambient`; `floor-system` **is** labelled ambient (spec §5.9).
- Fixture `Terraform` objects and runner pods live in namespace `pt-techne-floor-fixture` (off-mesh). `Terraform` names are workspace names: `main-sandbox`, `regional-sandbox`; `regional-sandbox` has `dependsOn: [main-sandbox]` (spec §5.2).
- `spec.approvePlan` is never set in Git manifests. `floor-api` writes it with server-side apply, field manager `floor-api`, `force: true`. This is the §7 "mechanism that stops Flux from reverting approvePlan": Flux never owns the field (spec §5.4).
- The sandbox `GitRepository` carries annotation `kustomize.toolkit.fluxcd.io/ssa: IfNotPresent` so Flux creates it once and `floor-api` owns `spec.ref.branch` thereafter (spec §5.3 step 1).
- `disableDriftDetection: true` on every `Terraform` object (spec §5.5). `spec.branchPlanner.enablePathScope: true` on every object (see Deviations).
- Plan ID format `plan-<branch>-<10 chars of commit>`; `approve` succeeds only when `planId == status.plan.pending` (spec §5.4).
- Destroys: plans whose `tofu show -json` contains any `delete` action are annotated `floor.osinfra.io/review: destroys`; `approve` of such a plan requires `acknowledgeDestroys: true` (spec §5.4).
- Approvers: members of authentik group `pt-techne: floor Approvers` (local naming convention `<team>: <label>`; this is the spec's `platform-team` group). Authors may approve their own plans. Identity arrives as `X-authentik-username` / `X-authentik-groups` headers from the outpost (spec §5.4, §5.7).
- Every approval is a Kubernetes `Event` (reason `Approved`, reporting controller `floor-api`) on the `Terraform` object carrying approver, plan ID and commit, and is mirrored as a PR comment when the object is a Branch Planner plan (spec §5.4, §5.8).
- Product names in prose: lowercase **authentik**, **agentgateway**.
- Never print or persist to Git: GitHub tokens, authentik tokens or app passwords, the state passphrase. They live under `tests/integration/floor/.work/` only.
- Helm charts, container images and GitHub Actions pinned: Flux Operator chart `oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator` `0.62.1`; tofu-controller repo `https://flux-iac.github.io/tofu-controller/` chart `tofu-controller` `0.16.6`; actions by 40-char SHA with version comment.
- OpenTofu style per platform instructions: logic in `locals.tofu`, alphabetical arguments, comment headers with resolving URLs, `pre-commit run -a` before every commit in repos that have `.pre-commit-config.yaml`.
- Commit messages: sentence case, no Conventional Commits prefixes, `Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>` trailer.

### Deviations from the spec (decided here, flag in handoff)

1. **Change detection:** tofu-controller v0.16.6 ships `spec.branchPlanner.enablePathScope` (plans a PR only when it changes files under `spec.path`). The spec §5.3 says Branch Planner has no change detection; Task 1 corrects that passage. `shared/` symlink targets and `pt-arche-*` SHA bumps are not detected in this milestone; `replan` is the escape hatch.
2. **State backend:** tofu-controller's default `kubernetes` backend (Secret `tfstate-default-<name>` in the object's namespace) instead of an s3 fixture bucket. `TF_ENCRYPTION` with a `pbkdf2` passphrase is still exercised through `runnerPodTemplate.spec.envFrom`.
3. **Secrets source:** the local milestone writes Secrets directly from `gh auth token` and a generated passphrase in `setup.sh`. OpenBao Secrets Operator is deferred to the cloud milestone.
4. **GitHub auth:** PAT from `gh auth token` locally (the spec's GitHub App is a cloud concern).
5. **Destroy guard:** `ErrDestroysUnacknowledged`/`acknowledgeDestroys` are implemented but nothing yet writes `floor.osinfra.io/review: destroys`; the plan-output detector is deferred, so destroy plans are currently approvable without acknowledgement.

## Review Focus

1. `approve` with a `planId` that no longer equals `status.plan.pending` (new commit landed) → returns an error naming both IDs and patches nothing. Pinned in Task 4.
2. Someone pushes to the PR branch between repoint and re-plan → `floor-api` stops after the revision check, leaves `GitRepository` on the PR branch, returns an error naming the expected and observed commit, and sets nothing. Pinned in Task 5.
3. Flux reconciles the `Kustomization` after `floor-api` has set `approvePlan` → the field is retained (SSA ownership). Pinned in Task 3 `verify.sh`.
4. A caller whose `X-authentik-groups` lacks `pt-techne: floor Approvers` calls `approve` → tool error `forbidden`, nothing patched; `list_workspaces` still works. Pinned in Task 6.
5. The sandbox `GitRepository` is already pointed at a different open PR when a second PR is approved → last approval wins: repoint again, annotation updated. Pinned in Task 5.

---

### Task 1: Fixture repository and spec correction

**Files:**
- Modify: `logos/pt-logos/teams/pt-techne.tfvars` (add repository `pt-techne-floor-fixture`, same shape as `pt-techne-floor`)
- Create (new repo `osinfra-io/pt-techne-floor-fixture`, cloned to `techne/pt-techne-floor-fixture`): `README.md`, `main/{main.tofu,providers.tofu,locals.tofu}`, `regional/{main.tofu,providers.tofu,locals.tofu}`, `.github/copilot-instructions.md`
- Modify: `techne/pt-techne-floor/docs/superpowers/specs/2026-11-15-factory-floor-flux-design.md:118-119` (Branch Planner change detection paragraph)

**Interfaces:**
- Produces: repo `https://github.com/osinfra-io/pt-techne-floor-fixture`, default branch `main`, roots `./main` (creates `kubernetes_namespace_v1` `floor-fixture`) and `./regional` (creates `kubernetes_config_map_v1` `floor-fixture/regional` with data `{ workspace = "regional" }`). Both use provider `hashicorp/kubernetes` with `host = "https://kubernetes.default.svc"`, `token`/`cluster_ca_certificate` read in `locals.tofu` from `/var/run/secrets/kubernetes.io/serviceaccount/`.

- [ ] **Step 1: Add the repository to Logos** — copy the `pt-techne-floor` entry in `pt-techne.tfvars`, name `pt-techne-floor-fixture`, description "Fixture workspaces for pt-techne-floor local integration tests". Run `pre-commit run -a` in `logos/pt-logos`, open draft PR (label `opentofu`), merge, `git pull`, delete branch. Clone the new repo to `techne/pt-techne-floor-fixture`.
- [ ] **Step 2: Write the two roots.** `main/providers.tofu` and `regional/providers.tofu` identical; `regional` reads nothing from `main` (the dependency is expressed by `dependsOn` on the `Terraform` object, not remote state).
- [ ] **Step 3: Validate** — Run: `for d in main regional; do tofu -chdir=$d init -backend=false && tofu -chdir=$d validate; done`. Expected: `Success! The configuration is valid.` twice.
- [ ] **Step 4: Correct the spec.** Replace the §5.3 paragraph beginning "Branch Planner has no change detection" with: Branch Planner scopes plans with `spec.branchPlanner.enablePathScope` (PR must touch `spec.path`); `floor-api` adds detection for `shared/` symlink targets and `pt-arche-*` SHA bumps after this milestone.
- [ ] **Step 5: Commit** both repos (fixture: "Add fixture workspaces for the floor local milestone"; floor: "Correct Branch Planner change detection in the design"); push fixture `main` directly (fixture repo has no reviewers yet — note in README that it is test data).

### Task 2: Floor engine component in pt-pneuma

**Files:**
- Create: `pneuma/pt-pneuma/tests/integration/floor/{setup.sh,verify.sh,teardown.sh}`
- Create: `pneuma/pt-pneuma/tests/integration/floor/engine/{main.tofu,locals.tofu,variables.tofu,providers.tofu}`
- Modify: `pneuma/pt-pneuma/tests/integration/setup.sh:24` (append `"${SCRIPT_DIR}/floor/setup.sh"` after openbao), `verify.sh` component list, `teardown.sh` component list (floor first, so it is torn down before its dependencies), `tests/integration/README.md` (one bullet for the component and the `gh` prerequisite)

**Interfaces:**
- Consumes: `source "${SCRIPT_DIR}/../shared/cluster.sh"` (`assert_docker_desktop_kind_cluster`, `assert_local_gateway_stack_owner`, `tofu_stage <stage> <tofu args>`), `.work/` convention, `fail()` pattern from `kagent/setup.sh`.
- Produces: namespace `flux-system` with Flux Operator, `FluxInstance` `flux` (components `source-controller`, `kustomize-controller`, `notification-controller`), tofu-controller with `branchPlanner.enabled=true` and `allowedNamespaces=["pt-techne-floor-fixture"]`; Secret `flux-system/branch-planner-token` (`token`); Secret `pt-techne-floor-fixture/floor-runner-env` (`TF_ENCRYPTION` = `key_provider "pbkdf2" "default" { passphrase = "<generated>" }` + `method "aes_gcm" "default"` + `state { method = method.aes_gcm.default; enforced = true }`); ConfigMap `flux-system/branch-planner` with `secretName: branch-planner-token`, `resources: [{namespace: pt-techne-floor-fixture}]`; env `FLOOR_CONFIG_REF` (default `main`) and `FLOOR_CONFIG_REPO` (default `https://github.com/osinfra-io/pt-techne-floor`) consumed by Task 3.

- [ ] **Step 1: Write `verify.sh` first** (the failing test): asserts `kubectl get ns flux-system -o jsonpath='{.metadata.labels.istio\.io/dataplane-mode}'` is empty; `kubectl -n flux-system rollout status deploy/flux-operator deploy/source-controller deploy/kustomize-controller deploy/notification-controller deploy/tofu-controller deploy/branch-planner --timeout=180s`; `kubectl get crd terraforms.infra.contrib.fluxcd.io`; Secrets and ConfigMap above exist.
- [ ] **Step 2: Run it** — `tests/integration/floor/verify.sh`. Expected: fails on `flux-operator` rollout (not found).
- [ ] **Step 3: Write `engine/` root**: `helm_release.flux_operator` (OCI chart, namespace `flux-system`, create_namespace), `kubernetes_manifest.flux_instance` (`fluxcd.controlplane.io/v1 FluxInstance`, `distribution.version: 2.10.x`, `distribution.registry: ghcr.io/fluxcd`, `cluster.networkPolicy: false`, the three components), `helm_release.tofu_controller` with values `branchPlanner.enabled`, `allowedNamespaces`, `runner.image` pinned; `kubernetes_config_map_v1.branch_planner`. `FluxInstance` needs its CRD at plan time, so apply the operator release in a first `tofu_stage engine apply -target=helm_release.flux_operator`, then the full apply (pattern from `kagent/setup.sh` line 36).
- [ ] **Step 4: Write `setup.sh`**: preflight (`require gh`, `gh auth token` non-empty), write `${WORK_DIR}/github-token` and generate `${WORK_DIR}/state-passphrase` (`openssl rand -base64 32`) if absent, `kubectl create ns pt-techne-floor-fixture --dry-run=client -o yaml | kubectl apply -f -`, create the two Secrets with `kubectl create secret generic ... --dry-run=client -o yaml | kubectl apply -f -`, then the two `tofu_stage` applies. `teardown.sh`: delete floor `Kustomization` (Task 3) first, then `tofu_stage engine destroy -auto-approve`, delete namespaces `pt-techne-floor-fixture`, `floor-system`, `flux-system`, and the CRDs `terraforms.infra.contrib.fluxcd.io`, `fluxinstances.fluxcd.controlplane.io` (spike gotcha: CRDs outlive the Helm release).
- [ ] **Step 5: Run** `tests/integration/floor/setup.sh && tests/integration/floor/verify.sh`. Expected: `floor local verification passed`.
- [ ] **Step 6: Wire** into top-level `setup.sh`/`verify.sh`/`teardown.sh`; `pre-commit run -a`; commit "Add floor engine fixture to the local integration stack".

### Task 3: Floor configuration for the local fixture

**Files:**
- Create: `techne/pt-techne-floor/floors/local/{kustomization.yaml,namespace.yaml,runner-rbac.yaml,gitrepository.yaml,terraform-main.yaml,terraform-regional.yaml}`
- Create: `techne/pt-techne-floor/.github/workflows/kustomize.yml` (builds `floors/*` with `kubectl kustomize` and asserts `approvePlan` is absent)
- Modify: `pneuma/pt-pneuma/tests/integration/floor/setup.sh` (apply the Flux `GitRepository`+`Kustomization` for the floor config), `verify.sh`

**Interfaces:**
- Consumes: Task 2 namespaces/secrets, `FLOOR_CONFIG_REPO`/`FLOOR_CONFIG_REF`.
- Produces: `GitRepository` `pt-techne-floor-fixture/fixture` (url `https://github.com/osinfra-io/pt-techne-floor-fixture`, `ref.branch: main`, interval `30s`, annotation `kustomize.toolkit.fluxcd.io/ssa: IfNotPresent`); `Terraform` `main-sandbox` (path `./main`) and `regional-sandbox` (path `./regional`, `dependsOn: [{name: main-sandbox}]`), both `interval: 1m`, `sourceRef` the GitRepository, `serviceAccountName: tf-runner`, `disableDriftDetection: true`, `storeReadablePlan: json`, `branchPlanner: {enablePathScope: true}`, `runnerPodTemplate.spec.envFrom: [{secretRef: {name: floor-runner-env}}]`, no `approvePlan`; ServiceAccount `tf-runner` with a ClusterRole allowing `namespaces` and `configmaps` verbs `*` and `secrets` `*` in its own namespace (state backend); Flux `Kustomization` `flux-system/floor-local` path `./floors/local`, `prune: true`, `interval: 1m`.

- [ ] **Step 1: Write the workflow test first**: `kustomize.yml` runs `kubectl kustomize floors/local > /tmp/out.yaml && ! grep -q approvePlan /tmp/out.yaml`. Run the same commands locally. Expected: fails (`floors/local` missing).
- [ ] **Step 2: Write the manifests** above. `kustomization.yaml` lists all five files; `namespace.yaml` has no ambient label.
- [ ] **Step 3: Run** `kubectl kustomize floors/local | grep -c 'kind: Terraform'`. Expected: `2`; the `approvePlan` grep is empty.
- [ ] **Step 4: Extend `setup.sh`**: after the engine applies, `kubectl apply` a `GitRepository` `flux-system/pt-techne-floor` (`url: ${FLOOR_CONFIG_REPO}`, `ref.branch: ${FLOOR_CONFIG_REF}`) and the `Kustomization` `floor-local`.
- [ ] **Step 5: Extend `verify.sh`** with an engine round-trip that does not need `floor-api` yet: wait up to 300s for both `Terraform` objects to report `status.plan.pending` matching `^plan-main-[0-9a-f]{10}$` (`regional-sandbox` must stay `DependencyNotReady` until `main-sandbox` is applied — assert its condition reason before approving main); approve `main-sandbox` with `kubectl patch terraform main-sandbox -n pt-techne-floor-fixture --type=merge --field-manager=floor-api -p '{"spec":{"approvePlan":"<pending>"}}'` (this is the only place outside `floor-api` that writes the field; Task 6 replaces it with the MCP call); wait for `Ready=True` and `kubectl get ns floor-fixture`; then `flux reconcile kustomization floor-local` and assert `spec.approvePlan` is still the approved value (Review Focus 3); approve `regional-sandbox` the same way and assert ConfigMap `floor-fixture/regional` exists.
- [ ] **Step 6: Run** `tests/integration/floor/setup.sh && tests/integration/floor/verify.sh` with `FLOOR_CONFIG_REF=<this branch>`. Expected: pass. Reset: `kubectl -n pt-techne-floor-fixture delete terraform --all` is **not** needed between runs; verify must be idempotent (an already-applied object has empty `pending` → skip approval).
- [ ] **Step 7: Commit** in both repos: "Add local floor configuration" / "Reconcile the local floor configuration from the fixture".

### Task 4: floor-api core — Terraform object client

**Files:**
- Create: `techne/pt-techne-floor/go.mod` (module `github.com/osinfra-io/pt-techne-floor`, go 1.26), `cmd/floor-api/main.go` (stub `func main()` that calls `run()` in Task 6), `internal/floor/{types.go,client.go,client_test.go}`
- Create: `techne/pt-techne-floor/.github/workflows/go-test.yml` (copy `pt-techne-mcp-server/.github/workflows/go-test.yml`, SHAs unchanged)

**Interfaces:**
- Produces (package `floor`):
  - `var TerraformGVK = schema.GroupVersionKind{Group: "infra.contrib.fluxcd.io", Version: "v1alpha2", Kind: "Terraform"}`; `var GitRepositoryGVK = schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"}`
  - `type Workspace struct { Name, Namespace, Path, SourceBranch, Phase, PendingPlan, LastAppliedRevision, Review string; Destroy, PlanOnly bool; PullRequest int; PrimaryResource string }` — `PullRequest`/`PrimaryResource` from labels `infra.weave.works/pr-id`, `infra.weave.works/primary-resource` (Branch Planner objects); `Review` from annotation `floor.osinfra.io/review`.
  - `type Client struct { k client.Client; namespace string }`; `func NewClient(k client.Client, namespace string) *Client`
  - `func (c *Client) List(ctx) ([]Workspace, error)`; `func (c *Client) Get(ctx, name string) (Workspace, error)`
  - `func (c *Client) Approve(ctx, name, planID string, acknowledgeDestroys bool) error` — errors: `ErrPlanMismatch` (wraps pending and given), `ErrDestroysUnacknowledged`, `ErrNoPendingPlan`
  - `func (c *Client) SetApprovePlan(ctx, name, value string) error` — SSA patch of `spec.approvePlan` only, field manager `floor-api`, force; used by `Approve`, `Hold` (`disable`), `Unhold` (`""`), `Replan` (`replan`)
  - `func (c *Client) SetDestroy(ctx, name string) error` — SSA `spec.destroy: true`
  - `func (c *Client) Workspace(ctx, name string) (*unstructured.Unstructured, error)`

- [ ] **Step 1: Write `client_test.go`** with controller-runtime `fake.NewClientBuilder().WithScheme(s)` and unstructured objects. Tests: `TestListMapsStatus` (pending plan, phase from `status.conditions[type=Ready].reason`, labels → PR fields); `TestApproveSetsApprovePlan` (patched object has `spec.approvePlan == planID`); `TestApproveRejectsStalePlan` (pending `plan-main-aaaaaaaaaa`, given `plan-main-bbbbbbbbbb` → `errors.Is(err, ErrPlanMismatch)`, `approvePlan` unset); `TestApproveRequiresDestroyAck` (annotation `floor.osinfra.io/review: destroys`, ack false → `ErrDestroysUnacknowledged`; ack true → ok); `TestHoldSetsDisable`.
- [ ] **Step 2: Run** `go test ./internal/floor/...`. Expected: compile failure (package missing).
- [ ] **Step 3: Implement** `types.go`, `client.go`. SSA via `c.k.Patch(ctx, obj, client.Apply, client.FieldOwner("floor-api"), client.ForceOwnership)` on an unstructured containing only `apiVersion`, `kind`, `metadata.name/namespace`, and the one spec field.
- [ ] **Step 4: Run** `go test ./internal/floor/... -v`. Expected: all PASS.
- [ ] **Step 5: Commit** "Add floor-api Terraform object client".

### Task 5: floor-api sandbox — repoint, revision check, return to main

**Files:**
- Create: `techne/pt-techne-floor/internal/sandbox/{sandbox.go,sandbox_test.go}`, `internal/githubapi/{client.go,client_test.go}`

**Interfaces:**
- Consumes: `floor.Client`, `floor.GitRepositoryGVK`.
- Produces:
  - `githubapi.Client` wrapping `go-github/v68` with `NewClient(token, owner, repo string, baseURL string) *Client` (baseURL for `httptest`), `PullRequest(ctx, number int) (PR, error)` with `type PR struct { HeadSHA, HeadRef, State string; Merged bool }`, `UpsertComment(ctx, number int, marker, body string) error` (edit in place when a comment containing `marker` exists), `Comment(ctx, number int, body string) error`.
  - `sandbox.Manager` with `NewManager(fl *floor.Client, gh *githubapi.Client, k client.Client, namespace, gitRepository string) *Manager`
  - `func (m *Manager) ApprovePullRequestPlan(ctx, plannerName, planID string, ack bool, approver string) error`:
    1. `Get` planner object → `PullRequest`, `PrimaryResource`; validate `planID == PendingPlan` (reuse `floor.Approve` semantics on the planner object without patching it).
    2. `gh.PullRequest` → head SHA, state `open` required.
    3. SSA `GitRepository` `spec.ref.branch = <PR head branch>` and annotation `floor.osinfra.io/pull-request: <n>`, field manager `floor-api`, force (Review Focus 5: unconditional overwrite).
    4. Poll `PrimaryResource` every 2s up to `RevisionTimeout` (default 180s) until `PendingPlan` has suffix `head[:10]`; a pending plan with a different non-empty suffix → `ErrRevisionMismatch{Expected, Observed}` and return (Review Focus 2).
    5. `fl.Approve(primary, pendingPlan, ack)`; `gh.UpsertComment(n, "<!-- floor:sandbox -->", ...)` with approver, plan ID, commit.
  - `func (m *Manager) ReturnToMainIfClosed(ctx) (returned bool, err error)` — reads the annotation; if the PR is `closed` or merged, SSA `spec.ref.branch = "main"` and remove the annotation. `func (m *Manager) Run(ctx, every time.Duration)` loops it.

- [ ] **Step 1: Write tests.** `githubapi`: `httptest.Server` returning fixtures for `GET /repos/o/r/pulls/7`, `GET/POST/PATCH .../issues/7/comments` → `TestUpsertCommentEditsExisting`. `sandbox`: fake k8s client preloaded with GitRepository (`branch: main`), primary `regional-sandbox` and planner object (labels pr-id `7`, primary `regional-sandbox`, pending `plan-floor-test-7-abcdef1234`); test doubles: a `fakePlanner` that flips the primary's pending plan when the GitRepository branch changes. Tests: `TestApproveRepointsThenApproves` (GitRepository branch == head branch, primary `approvePlan` set, annotation set, comment posted); `TestApproveStopsOnRevisionMismatch` (fake sets pending with suffix `9999999999` → `ErrRevisionMismatch`, `approvePlan` unset, branch still PR head); `TestSecondApprovalOverwrites` (annotation `pull-request: 3` present → becomes `7`); `TestReturnToMainWhenMerged`.
- [ ] **Step 2: Run** `go test ./internal/sandbox/... ./internal/githubapi/...`. Expected: compile failure.
- [ ] **Step 3: Implement.** Head branch is `PR.HeadRef`; the branch poll compares `PendingPlan` suffix with `PR.HeadSHA[:10]`.
- [ ] **Step 4: Run** the tests. Expected: PASS.
- [ ] **Step 5: Commit** "Add floor-api sandbox repoint and return-to-main".

### Task 6: floor-api MCP server, events, deployment and end-to-end verify

**Files:**
- Create: `techne/pt-techne-floor/internal/server/{server.go,server_test.go,identity.go}`, `cmd/floor-api/main.go` (real), `Dockerfile` (copy `pt-techne-mcp-server/Dockerfile`, binary `floor-api`, `EXPOSE 8080`), `.github/workflows/release.yml` (copy from `pt-techne-mcp-server`; image `ghcr.io/osinfra-io/pt-techne-floor-api`)
- Create: `pneuma/pt-pneuma/tests/integration/floor/api/{main.tofu,locals.tofu,variables.tofu,providers.tofu}` (namespace `floor-system` labelled ambient, ServiceAccount + ClusterRole for `terraforms`, `gitrepositories` get/list/watch/patch and `events` create, Deployment `floor-api` image `pt-techne-floor-api:local` `imagePullPolicy: Never`, env `FLOOR_NAMESPACE=pt-techne-floor-fixture`, `FLOOR_GIT_REPOSITORY=fixture`, `FLOOR_GITHUB_REPO=osinfra-io/pt-techne-floor-fixture`, `FLOOR_APPROVER_GROUP=pt-techne: floor Approvers`, `GITHUB_TOKEN` from Secret `floor-system/floor-github-token`; Service port 8080; HTTPRoute `floor-ingress` hostname `floor.localhost` on `istio-ingress/gateway` → agentgateway `Gateway` service; HTTPRoute `floor-backend` on the agentgateway `Gateway` → `floor-system/floor-api:8080` + ReferenceGrant; HTTPRoute `authentik-outpost-floor` for `/outpost.goauthentik.io` — mirror `agentgateway/local/main.tofu`)
- Create: `pneuma/pt-pneuma/tests/integration/floor/auth/{main.tofu,locals.tofu,variables.tofu,providers.tofu}` (authentik provider using `authentik/.work/config/bootstrap.tfvars.json` token: `authentik_group` `pt-techne: floor Approvers`, `authentik_user` `floor-verify` in that group, `authentik_token` intent `app_password` for it; output the app password to `${WORK_DIR}/auth/verify-token` via `local_sensitive_file`)
- Modify: `pneuma/pt-pneuma/tests/integration/istio/browser-auth/main.tofu` policies (add `floor = { host = "floor.localhost", path = "/", required_groups = ["pt-techne: floor Approvers"] }`), `floor/setup.sh`, `floor/verify.sh`, `floor/teardown.sh`

**Interfaces:**
- Consumes: `floor.Client`, `sandbox.Manager`, `githubapi.Client`.
- Produces:
  - `server.New(fl *floor.Client, sb *sandbox.Manager, rec record.EventRecorder, approverGroup string) *mcp.Server` registering tools `list_workspaces` (`{}` → `{workspaces: []floor.Workspace}`), `get_workspace` (`{name}`), `pending_approvals` (`{}` → workspaces with non-empty `PendingPlan`), `approve` (`{name, planId, acknowledgeDestroys bool}` → `{applied: false, message}`; routes to `sb.ApprovePullRequestPlan` when the object has `PullRequest != 0`, else `fl.Approve`; records Event reason `Approved` message `"<approver> approved <planId>"`), `reject` (`{name, reason}` → Event `Rejected`, no patch), `replan`, `hold`, `unhold`, `destroy` (`{name}`).
  - `identity.FromRequest(r *http.Request) Identity{User string; Groups []string}` from `X-authentik-username`, `X-authentik-groups` (pipe-separated as the outpost sends); stored in `context` by the HTTP handler; every mutating tool returns error `forbidden: <user> is not in <group>` when the group is absent (Review Focus 4). Read tools require only a username.
  - `run()` in `main.go`: in-cluster `rest.InClusterConfig()`, `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)` mounted at `/mcp`, `/healthz` → 200, listens `:8080`; starts `sb.Run(ctx, 30*time.Second)`.

- [ ] **Step 1: Write `server_test.go`** using `mcp.NewInMemoryTransports()` with a real `floor.Client` over the controller-runtime fake client (as in Task 4), a `sandbox.Manager` whose `githubapi.Client` points at an `httptest.Server`, and a recording `EventRecorder` (`k8s.io/client-go/tools/record.NewFakeRecorder`). Tests: `TestListWorkspaces` returns two names; `TestApproveRecordsEvent` (`Approved` event contains plan ID and user `brett`); `TestApproveForbiddenWithoutGroup` (identity groups `["pt-pneuma: kagent Admins"]` → error contains `forbidden`, object unpatched); `TestIdentityParsesGroups` (`"a|b"` → `["a","b"]`).
- [ ] **Step 2: Run** `go test ./internal/server/...`. Expected: compile failure.
- [ ] **Step 3: Implement** `server.go`, `identity.go`, `main.go`, `Dockerfile`. Run `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 4: Extend `setup.sh`**: `docker build -t pt-techne-floor-api:local "${FLOOR_REPO_DIR:-${SCRIPT_DIR}/../../../../../techne/pt-techne-floor}"`; create Secret `floor-system/floor-github-token` from `${WORK_DIR}/github-token`; `tofu_stage auth apply`, `tofu_stage api apply`; rerun `istio/apply-auth.sh` (policy map changed). `teardown.sh`: destroy `api`, `auth` stages before the engine.
- [ ] **Step 5: Replace the `kubectl patch` approvals in `verify.sh`** with an `mcp_call()` helper: `curl --insecure -u "floor-verify:$(cat "${WORK_DIR}/auth/verify-token")" -H 'Accept: application/json, text/event-stream' -H 'Content-Type: application/json' https://floor.localhost/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"<tool>","arguments":<json>}}'` (authentik outpost accepts Basic app passwords and forwards identity headers; first assert `mcp_call list_workspaces` returns both names — if it returns 302, stop and report: the Basic-auth assumption failed). Then the sandbox flow:
  1. `gh api repos/osinfra-io/pt-techne-floor-fixture/contents/regional/main.tofu` → create branch `floor-test-$(date +%s)`, change the ConfigMap data value to the branch name, open PR with `gh pr create`.
  2. Wait ≤300s for a Branch Planner planner object with label `infra.weave.works/pr-id=<n>` and `primary-resource=regional-sandbox`; assert **no** planner object for `main-sandbox` (path scope); assert `gh pr view <n> --json comments` contains `Terraform Plan`.
  3. `mcp_call pending_approvals` lists the planner object; `mcp_call approve {name, planId}`.
  4. Assert `GitRepository fixture spec.ref.branch == <branch>`, annotation `floor.osinfra.io/pull-request == <n>`, `regional-sandbox` `Ready=True` with `lastAppliedRevision` containing the PR head SHA, ConfigMap data equals the branch name, an Event `reason=Approved` on `regional-sandbox`, and the PR has a comment containing `floor-verify`.
  5. `gh pr merge <n> --squash --delete-branch`; wait ≤120s for `spec.ref.branch == main` and `regional-sandbox` pending plan at the new `main` commit; approve it via `mcp_call` so the stack is left applied.
  6. Negative: `mcp_call approve` with `planId: plan-main-0000000000` → response `isError: true` containing `mismatch`.
- [ ] **Step 6: Run** `tests/integration/floor/setup.sh && tests/integration/floor/verify.sh` (with `FLOOR_CONFIG_REF` set to the Task 3 branch until it merges). Expected: `floor local verification passed`. Then full `tests/integration/verify.sh` to confirm nothing else regressed.
- [ ] **Step 7: `pre-commit run -a`** in both repos; commit "Add floor-api MCP server and local deployment" (pt-techne-floor) and "Deploy floor-api and verify the sandbox approval flow" (pt-pneuma).

### Task 7: Documentation and handoff

**Files:**
- Modify: `techne/pt-techne-floor/README.md` (badges: OpenTofu Tests not applicable; add Dependabot only if `dependabot.yml` added; short "Local milestone" section: prerequisites `gh auth login`, `docker`, run `tests/integration/setup.sh` in `pt-pneuma`), `.github/copilot-instructions.md` (replace "(planned)" for floor config and `floor-api` with the real paths)
- Modify: `pneuma/pt-pneuma/tests/integration/README.md` (floor component: what it deploys, the `gh` requirement, that verify opens and merges a PR on the fixture repo)

- [ ] **Step 1: Edit** the three files; keep each change to the passages above.
- [ ] **Step 2: `pre-commit run -a`**, commit "Document the floor local milestone", push, open draft PRs: `pt-techne-floor` (labels `kubernetes`, `tests`), `pt-pneuma` (labels `tests`, `devex`), fixture already on `main`.
- [ ] **Step 3: Handoff note** in the PR body: the four deviations listed above, and that `FLOOR_CONFIG_REF` must be unset once `pt-techne-floor` merges.
