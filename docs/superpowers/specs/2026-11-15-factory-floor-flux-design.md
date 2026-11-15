# Factory floor on Flux: running OpenTofu plan and apply on the platform

Design for project 1 of the platform software factory. Supersedes `2026-11-14-factory-floor-design.md` (custom operator) and the orchestration sections of `platform-on-platform-spec.md`; the current-state diagrams there remain accurate. Spike evidence is in `2026-11-15-flux-tofu-controller-spike-findings.md`.

Home: this repository (`pt-techne-floor`).

## 1. Intent

Replace the GitHub Actions OpenTofu pipelines (`sandbox.yml`, `non-production.yml`, `production.yml` in `pt-logos`, `pt-corpus`, `pt-pneuma`, backed by `pt-techne-opentofu-workflows`) with an in-cluster system that plans and applies platform changes, asks a platform-team member for approval, and reports back — reachable from a CLI without opening GitHub.

### Why

- The Actions pipelines are large, slow, and re-initialise every workspace on every run. `pt-pneuma` production alone is a 20-job `needs:` graph. Humans and agents spend most of their time waiting.
- Cross-repo order (Logos → Corpus → Pneuma) is encoded only in `needs:` lists and workflow triggers. Nobody can ask "what does this change affect?"
- The agentic loop (Copilot CLI or Nomos writes the change and opens the PR) stalls at GitHub Actions: the agent polls runs and scrapes job summaries.

### Decisions that bind this design

- **Git stays the source of truth; it stops being the interface.** Every change is a commit. The floor reacts to commits.
- **A platform-team member approves every apply, in every environment, including sandbox.** Merging is not approval.
- **Buy the engine, build the thin layer.** The state machine (plan → approve → apply, dependency ordering, PR previews, drift) is Flux Operator + flux-iac/tofu-controller. Custom code is limited to: the approval/status API behind authentik, the agent narration, and the CLI (project 3).
- **All OpenTofu code, modules, `.tfvars`, state layout and encryption conventions are unchanged.** Only the executor changes.
- **Approach C holds:** the controller owns the state machine; the agent provides judgment and narration and never holds the apply trigger.

### The larger programme (context)

| # | Project | Status |
| --- | --- | --- |
| 1 | **Factory floor** — this document | Design |
| 2 | Platform knowledge graph — cross-repo workspace/dependency edges | Replaced in v1 by `Terraform.spec.dependsOn`; a graph can later *generate* the Terraform objects |
| 3 | CLI front door — persona-aware CLI talking to the floor API through agentgateway | After 1; a thin Copilot plugin is the first client |
| 4 | Intent to code — Nomos + `pt-techne-mcp-server` render specs to `tfvars` and open PRs | Exists; wire to the floor API |
| 5 | Cloud phases — floor on the Pneuma GKE cluster, Workload Identity, GCS/KMS | After 1 works locally |

## 2. Goals, non-goals, anti-goals

### Goals

- A new commit on `main` in a watched repo produces plans for the affected workspaces, in dependency order, with no GitHub Actions run.
- Every open PR gets a plan against sandbox state posted as a PR comment within a minute of pushing, for the workspaces it touches only.
- Approving a PR's sandbox plan applies that PR into the one shared sandbox — the same proving ground `sandbox.yml` applies to today. Seeing another developer's changes in your plan is the intended signal to talk to them.
- Approval is an explicit act by an authenticated platform-team member, bound to a specific plan of a specific commit. Any new commit invalidates it.
- Apply uses exactly the saved plan that was approved.
- What ran, what is waiting on whom, and why, is readable from a CLI via the API.
- The first milestone runs end to end on the Docker Desktop local integration stack.

### Non-goals

- Datadog observability of the floor itself (deferred, as in the spec).
- Day-zero bootstrap or disaster recovery of the Pneuma cluster that hosts the floor. One-time bootstrap is a documented operator procedure (see 5.9); full recovery is designed later.
- Moving Pneuma Helm add-ons from `helm_release` resources to Flux `HelmRelease`. Possible later, orthogonal.
- The dedicated CLI (project 3). This design fixes the API it will use.

### Anti-goals

- A model deciding whether an apply happens.
- Long-lived cloud credentials in the cluster.
- Rebuilding GitHub's review/history features.
- Orchestration only a prompt can explain. Every run must be explicable from Kubernetes objects alone (`kubectl get terraform`).

## 3. Constraints

- Flux Operator, Flux, and tofu-controller run **outside the ambient mesh** (`flux-system` not labelled). They need no mesh mTLS (API server, git, OCI only; controller ↔ runner gRPC has its own cert-manager mTLS) and must keep working while Istio is being changed by a Terraform object they run. The spike labelled `flux-system` as a stress test; that is not the deployed configuration.
- The **Pneuma GKE cluster is the management cluster**. Flux lives there, reconciles Pneuma itself (its own cluster, add-ons and Istio) and every other cluster in the fleet. Because Flux is off-mesh, a Terraform object changing Istio on the same cluster cannot take Flux down with it. Self-management of the GKE cluster resource is the usual in-place pattern (node pools, add-ons); a plan that would recreate the hosting cluster is a destroy and is caught by the `review` guard.
- Platform conventions apply to the custom layer: Go, SHA-pinned consumers, semver tags, OpenTofu for any Kubernetes/cloud resources the floor needs.
- No inbound webhooks in v1; Flux sources and the PR input provider poll.
- The local phase must not require GCP.

## 4. Approaches considered

| | Approach | Trade-off | Outcome |
| --- | --- | --- | --- |
| A | **Custom operator** (`2026-11-14` design): Run/Step/Approval CRDs, Substrate actors, kagent narrator | Full control, platform as its own workload; months of controller code before the first apply; reinvents plan storage, approval binding, PR previews, drift | Dropped after the Flux evaluation |
| B | **Atlantis / Burrito / Terramate** | Atlantis is PR-comment-driven and GitHub-shaped — the interface we are leaving; Burrito is young; Terramate still runs on Actions | Dropped |
| C | **Flux Operator + tofu-controller** as engine, thin custom layer | Proven in spike: approval by plan hash, invalidation on new commit, Branch Planner PR comments, per-PR sandbox via ResourceSet, encrypted state via `TF_ENCRYPTION`. Costs: tofu-controller is a smaller community project; tf-runner RBAC and secret delivery need care | **Chosen** |

## 5. Design

### 5.1 Components

| Component | Source | Role |
| --- | --- | --- |
| Flux Operator + FluxInstance | controlplane.io OCI chart | Installs source/kustomize/notification controllers |
| tofu-controller (+ Branch Planner) | flux-iac Helm chart | `Terraform` CRD: plan/approve/apply, `dependsOn`, outputs-to-secret, drift, PR plan comments |
| OpenBao Secrets Operator (or ESO) | existing stack | Renders OpenBao KV into Kubernetes Secrets the runner `envFrom`s (GitHub token, `TF_ENCRYPTION`, local-only backend creds) |
| `floor-api` | **custom**, Go, this repo | MCP server behind agentgateway/authentik: list/get/approve/reject/replan/explain |
| floor agent | kagent, existing stack | Read-only narration: plan summaries, anomaly flags, "explain" |
| Floor config repo | **custom**, plain YAML/Kustomize | One central repo (this Techne repo) declaring the `GitRepository` and `Terraform` objects for every deployment repo; reconciled by a Flux `Kustomization`, **not** by OpenTofu, so the floor's own objects are never a Terraform object the floor runs |

### 5.2 Mapping today's workspaces to Terraform objects

One `Terraform` object per (workspace, environment), named as today's workspace (`main-production`, `us-east1-b-onboarding-production`). `path` = directory, `vars`/`varsFrom` = the existing `.tfvars`, `backendConfig` = today's GCS bucket/prefix, `workspace` = the OpenTofu workspace name so **state keys do not move**.

Order is expressed with `dependsOn` and replaces `needs:` and the cross-repo workflow triggers:

```text
pt-logos/main-production
  └─ pt-corpus/main-production
       └─ pt-corpus/us-east1-production …
            └─ pt-pneuma/main-production
                 └─ pt-pneuma/us-east1-b-production
                      └─ pt-pneuma/us-east1-b-onboarding-production
                           └─ us-east1-b-{cert-manager,istio,…}-production
```

Cross-repo data still flows through `pt-arche-core-helpers` remote state, unchanged. `dependsOn` only orders; it carries no outputs in v1.

Environment promotion: every environment's objects track `main`. `production` objects `dependsOn` their non-production counterpart, so a production plan for commit N is only generated once non-production has applied N. A newer commit replaces the pending plan and production waits for non-production to catch up — the same "production runs after non-production on the same commit" semantics the Actions workflows have today, with the explicit approval acting as the soak gate. No promotion branch, tag or verb.

`floor-api` exposes `hold(name)` / `unhold(name)`, which set `approvePlan: disable` and clear it, so production (or anything) can be frozen during an incident without touching git. If "ship N to production while N+1 soaks" is ever needed, pin `ref.commit` on the production `GitRepository` in the floor config repo; that is a config-repo-only change and is not in v1.

### 5.3 Pull requests and sandbox

Sandbox is a **shared proving ground**: one per repo, used serially, applied to by whichever PR was approved most recently. This is today's `sandbox.yml` behaviour and is deliberate — per-PR isolated environments cost real money and time, and a developer seeing changes they did not make in their plan is the cue to talk to the other developer. Resource names in Logos and Corpus come from the environment, so parallel copies would collide anyway.

Two engine features implement it:

**Plans for every PR — Branch Planner.** For each open PR, tofu-controller's Branch Planner creates a plan-only copy of each watched sandbox `Terraform` object at the PR head, plans it against the **shared sandbox state**, and posts the plan as a PR comment. Because all PRs plan against the same state, a plan naturally includes anything another PR has applied there and this branch lacks. `!replan` re-plans.

Branch Planner has no change detection and would plan every listed object on every PR (~20 runner pods for a one-line Pneuma change). `floor-api` therefore maintains the Branch Planner object list per PR: an object is **affected** if the PR touches its `path`, a `shared/` file it symlinks, or bumps the SHA of a `pt-arche-*` module it pins. Unaffected objects are not planned and the PR comment says so.

**Applying a PR into sandbox — approval repoints the source.** There is no separate "checkout" step. When an approver `approve`s a Branch Planner plan for a PR:

1. `floor-api` sets the repo's sandbox `GitRepository.spec.ref.branch` to the PR head branch.
2. The permanent sandbox `Terraform` objects re-plan at that revision. `floor-api` compares the new plan ID's revision with the approved PR head; if they match it sets `approvePlan`, otherwise it stops and reports (someone pushed in between).
3. Apply proceeds as in every environment. Last approved PR wins.
4. When that PR merges or closes, `floor-api` points the sandbox `GitRepository` back at `main` and sandbox re-plans at `main` for approval.

`sandbox-destroy.yml` becomes `floor-api` `destroy(name)`, which sets `spec.destroy: true`; the destroy plan still needs approval.

Non-production and production are unaffected: they track `main` only and never see PR branches.

Not in v1: per-PR throwaway environments via Flux Operator `ResourceSet` (proven in the spike, but rejected on cost).

### 5.4 Approval model

The engine's native model is sufficient:

- `status.plan.pending` = `plan-<branch>-<10 chars of commit>`; a new commit or spec change clears it.
- Approval = setting `spec.approvePlan` to that exact value. A stale value is inert (spike-proven).
- After a failed apply the pending plan is empty; `approvePlan: replan` forces a new plan.

`floor-api` is the **only** writer of `spec.approvePlan` in non-local environments (RBAC: Flux's kustomize-controller is configured to ignore that field via `fluxcd.io/ignore` or server-side-apply field ownership — builder to confirm the mechanism). Approval is recorded as a Kubernetes `Event` on the object with the authentik identity, and mirrored to the PR comment.

Deterministic guard: any plan with destroys is flagged `review` on the object (annotation) regardless of the agent's view. Approving a `review` plan requires `approve(name, planId, acknowledgeDestroys: true)`.

Approval policy (v1): approvers = members of the `platform-team` authentik group. Authors may approve their own plans — the team is small and the plan, not the PR, is what is being approved. Every approval is an Event carrying approver, plan ID and commit.

### 5.5 Secrets and identity

| Need | Cloud | Local |
| --- | --- | --- |
| State backend | GCS, Workload Identity on `tf-runner` SA (one KSA per team/env) | `s3` backend to a fixture bucket; creds via OpenBao → Secret |
| State encryption | `TF_ENCRYPTION` with `gcp_kms` key provider — same `method.aes_gcm.default` the Actions workflows inject | `TF_ENCRYPTION` with `pbkdf2` passphrase from OpenBao |
| GCP provider auth | Workload Identity; no static keys | Not needed (kubernetes/helm providers only) |
| GitHub (Branch Planner, PR comments, commit status, PR state) | GitHub App installation token in OpenBao → Secret | Same |
| Kubernetes/Helm providers (Pneuma add-ons) | Unchanged: `google_client_config` token under Workload Identity, as `providers.tofu` does today. No kubeconfig Secrets, no `writeOutputsToSecret` dependency | Runner SA RBAC on docker-desktop |
| Other provider secrets (GitHub provider for Logos, Datadog API/app keys, …) | OpenBao KV on the Pneuma cluster (already deployed by Pneuma), rendered by OpenBao Secrets Operator into per-workspace Secrets the runner `envFrom`s. Seeding OpenBao is a one-time operator step, same as creating Actions secrets today | Same |

Drift detection is **off** in v1 (`disableDriftDetection: true`), matching today's pipelines, so `pending_approvals()` only ever contains plans caused by commits and `dependsOn` chains are not blocked by drift.

The OpenBao agent injector is not used for the runner (it writes files; the runner needs env). OpenBao Secrets Operator renders Secrets into the floor namespace.

### 5.6 Where the agent is called

Unchanged from the prior design in spirit; triggered by Flux notification-controller `Alert`s (webhook `Provider` → `floor-api` → kagent) rather than by a custom controller.

| Point | Input | Output | Fallback |
| --- | --- | --- | --- |
| Plan summary | `tofu show` (redacted) + commit diff | PR comment / API summary | Template comment |
| Anomaly flag | Plan JSON | `ok` / `review` + reasons | `ok`; destroys always `review` |
| Explain (API) | Terraform object(s), question | Prose | Structured dump |

The agent has read-only tools. It has no `approve`.

### 5.7 Client API (`floor-api`)

MCP server (streamable HTTP) behind agentgateway; authentik OIDC; groups passed as identity. Stable, client-agnostic; project 3's CLI and Nomos consume it.

| Tool | Who | Does |
| --- | --- | --- |
| `list_workspaces(repo?, env?, pr?, state?)` | platform-group or owning team | Terraform objects with phase, pending plan, last applied revision |
| `get_workspace(name)` | same | Full status + plan summary |
| `pending_approvals()` | approvers | Objects with a pending plan the caller may approve |
| `approve(name, planId)` / `reject(name, reason)` | platform-team | Sets `approvePlan` / records rejection. For a Branch Planner plan, first repoints the sandbox `GitRepository` at the PR (5.3) |
| `replan(name)` | platform-team | Sets `approvePlan: replan` |
| `hold(name)` / `unhold(name)` | platform-team | Sets / clears `approvePlan: disable` to freeze an object |
| `destroy(name)` | platform-team | Sets `spec.destroy: true`; the destroy plan still needs approval |
| `explain(name, question?)` | read | Agent narration |
| `watch(name)` | read | Streams status changes |

### 5.8 Reporting to GitHub

- One floor comment per environment per PR, edited in place (plan → awaiting approval → applied/failed), posted by `floor-api` on notification events.
- Commit status `floor/<environment>` on the head commit; branch protection can require it.

### 5.9 Topology

```text
Pneuma GKE cluster = management cluster (one per environment)
  flux-system (off-mesh): flux-operator, flux controllers, tofu-controller, branch-planner
  floor-system (on-mesh): floor-api, OpenBao secrets operator sync, kagent floor agent
  pt-logos / pt-corpus / pt-pneuma namespaces: GitRepository, Terraform objects
    ├─ pt-pneuma objects reconcile this cluster itself (self-hosting)
    └─ fleet: Terraform objects for every other team cluster (created and configured from here)

fleet clusters: no Flux of their own; the floor reaches them with the existing
google_client_config / Workload Identity provider pattern
```

Which Pneuma cluster hosts which floor: each environment's Pneuma cluster hosts the floor for that environment's objects (sandbox Pneuma runs `*-sandbox`, production Pneuma runs `*-production`). Logos has only production and runs on the production Pneuma cluster. This keeps credentials and blast radius per environment and matches today's one-workflow-per-environment split.

Bootstrap: the first Pneuma cluster per environment is created once with the existing Actions workflow (or locally), Flux Operator and the floor are installed on it, and from then on it reconciles itself. The Actions workflows for that repo are deleted in the same PR that adds its objects (no dual-running).

Locally, docker-desktop plays the Pneuma cluster: the floor runs there and the Pneuma add-on workspaces target the same cluster, exactly the self-hosting shape, which is why the local phase is a faithful test of the off-mesh requirement.

### 5.10 First milestone (local)

1. Flux Operator + tofu-controller deployed by a `tests/integration/floor/setup.sh` in `pt-pneuma`, off-mesh.
2. A throwaway GitHub repo containing a copy of the Pneuma local-integration workspaces; `Terraform` objects for two of them with `dependsOn`.
3. A PR gets a Branch Planner comment for affected objects only; approving it repoints sandbox at the PR, applies, and merging returns sandbox to `main`.
4. `floor-api` `list_workspaces`, `pending_approvals`, `approve` working through agentgateway with an authentik token; approval recorded as an Event and PR comment.
5. `tests/integration/floor/verify.sh` covering the above.

## 6. Decisions made in this design (your calls to check)

- Flux and tofu-controller run off-mesh; the floor's own services run on-mesh.
- Pneuma is the management cluster and is self-hosting; one floor per environment on that environment's Pneuma cluster; fleet clusters run no Flux.
- Terraform object names = today's workspace names; state keys unchanged.
- `dependsOn` replaces both `needs:` and cross-repo workflow triggers; production `dependsOn` non-production.
- One shared sandbox per repo as a proving ground; Branch Planner plans every PR against it (affected objects only); approving a PR plan repoints sandbox at the PR; no per-PR environments.
- Every environment tracks `main`; production `dependsOn` non-production; no promotion mechanism in v1, only `hold`/`unhold`.
- `floor-api` is the only writer of `approvePlan`; approvals are Events, not a database.
- OpenBao Secrets Operator over the agent injector for runner secrets; all provider secrets come from OpenBao, not Actions secrets.
- Floor objects live in one central config repo reconciled by Flux Kustomization, never by OpenTofu.
- Pneuma add-ons keep the `google_client_config` provider pattern; no kubeconfig Secrets.
- Drift detection off in v1.
- Authors may approve their own plans; destroy plans need an explicit acknowledgement.
- No dual-running of Actions and the floor for the same repo.
- GitHub App over PAT for all GitHub access.
- Pneuma Helm add-ons stay as OpenTofu `helm_release`.

## 7. Left to the builder

- Exact Helm values for Flux Operator, FluxInstance, tofu-controller (runner image digest pin, `allowedNamespaces`, Branch Planner polling interval).
- The mechanism that stops Flux from reverting `approvePlan` on reconcile (field manager / ignore annotation) and its test.
- Per-team runner ServiceAccounts, Workload Identity bindings, and RBAC manifests — as OpenTofu in Corpus/Pneuma.
- `floor-api` MCP schema, agentgateway route/policy, authentik provider.
- Redaction rules for plan output.
- Change-detection rules in `floor-api` (path, `shared/` symlink targets, arche SHA bumps) and how it maintains the Branch Planner object list per PR.
- How `floor-api` detects the applied PR closing/merging to return sandbox to `main` (poll via GitHub App).
- The revision check between the approved Branch Planner plan and the re-plan after repointing the sandbox source.
- Notification-controller `Provider`/`Alert` wiring to `floor-api`.
- Migration order: Logos first (one workspace, production only), then Corpus, then Pneuma. No dual-running: the moment a repo's `Terraform` objects exist, its Actions deploy workflows are deleted in the same PR; the Actions cross-repo triggers are replaced by `dependsOn` at that point.

## 8. Open questions carried forward

- Team spec location (Logos vs platform) — parked; does not affect project 1.
- Whether consuming teams get read access to their own PRs' objects via team-scoped authentik groups (likely yes; needed for Nomos).
- Whether the throwaway test repo becomes a permanent fixture.
- tofu-controller single-vendor risk; Burrito as a fallback engine with the same OpenTofu code (the custom layer stays engine-agnostic by talking only to the API, not the CRD).
