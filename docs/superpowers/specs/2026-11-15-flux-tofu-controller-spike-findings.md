# Flux + tofu-controller spike — findings

Throwaway spike run on the local Docker Desktop stack (Istio 1.31 ambient, authentik, OpenBao 2.5.2, Agent Substrate). Repo: `osinfra-io/pt-techne-flux-spike` (private, throwaway).

Versions: Flux Operator 0.62.1 / Flux v2.10.0, tofu-controller 0.16.6 (runner ships OpenTofu 1.12), Kubernetes 1.36.

## What was tested and what happened

| # | Question | Result |
| --- | --- | --- |
| 1 | Does ambient mesh break controller ↔ runner gRPC mTLS? | **No.** `flux-system` and `flux-spike` ambient-labelled; plan/apply worked unchanged. |
| 2 | Does `TF_ENCRYPTION` + encrypted GCS/S3-style state work in the runner? | **Yes.** Env via `runnerPodTemplate.spec.envFrom`; state object in bucket is `encrypted_data` with pbkdf2 meta. A deliberate HCL typo failed `init`, proving the env is honoured. |
| 3 | Manual approval by plan ID, and does a new commit invalidate a stale approval? | **Yes / yes.** `approvePlan: plan-main-<10 chars>` applied. Pushing a new commit produced `plan-main-<new>`; the old approval value stayed in spec but nothing applied — stale approvals are inert. |
| 4 | Branch Planner: does a PR get a plan comment? | **Yes.** `hello-pr-1` Terraform object created in plan-only mode within ~1 min; plan posted as a PR comment (as the token owner). |
| 5 | Flux Operator `ResourceSet` per-PR sandbox apply + destroy on close? | **Yes, with a gotcha.** Label-gated `ResourceSetInputProvider` (GitHubPullRequest, `deploy/sandbox`) stamped a `GitRepository` + `Terraform` (`approvePlan: auto`, `destroyResourcesOnDeletion: true`) and applied. On PR close the ResourceSet deleted both objects at once; the Terraform's destroy needs the source artifact and hung on "Source is not ready, artifact not found". Recreating the GitRepository let destroy finish in ~20 s. |

## Design findings to carry forward

1. **OpenBao → runner secrets need a bridge.** The OpenBao agent injector writes files; tf-runner wants env vars (`TF_ENCRYPTION`, backend creds). Use OpenBao Secrets Operator or External Secrets to render a Kubernetes Secret the runner `envFrom`s. In GCP use Workload Identity for GCS backend + KMS key provider instead of static creds, so the only secret is nothing (`gcp_kms` key provider).
2. **tf-runner RBAC.** The runner SA has no cluster permissions by default. For the Pneuma add-on layer (Kubernetes/Helm providers) the runner SA needs explicit RBAC per workspace namespace. For GCP-only layers (Logos/Corpus) it needs only Workload Identity.
3. **Per-PR sandbox GC ordering.** Don't template a per-PR `GitRepository` next to the `Terraform` in a ResourceSet. Either point per-PR Terraform objects at a long-lived GitRepository that tracks the PR branch via the input provider (ResourceSet only templates the Terraform), or template the GitRepository in a separate ResourceSet with `dependsOn`, so the Terraform is removed first. Verify destroy completes before trusting it in the design.
4. **Approval semantics are sound.** `approvePlan` is pinned to a plan hash that embeds the git revision; a new commit invalidates. The custom approval API only needs to patch `spec.approvePlan` (or commit it) — no additional state machine required.
5. **Branch Planner vs ResourceSet.** Branch Planner = plan-only PR feedback (free, no apply). ResourceSet = real pre-merge sandbox apply, gated by a PR label; the label is the "apply to sandbox" button. Both can coexist on the same workspace.
6. **Failed apply recovery.** After a failed apply `status.plan.pending` is empty; set `approvePlan: replan` to force a new plan. The approval API should expose "replan".
7. **Secrets of record.** Branch Planner and the input provider both need a GitHub token/App; store in OpenBao, sync to the Flux namespace.
8. **State bucket.** Spike used a throwaway rustfs. Real design: GCS bucket per repo/env as today, `gcp_kms` key provider via `TF_ENCRYPTION`, Workload Identity on the runner SA.

## Not tested

- GCP providers / Workload Identity from a GKE runner.
- `dependsOn` chaining across Terraform objects (Logos → Corpus → Pneuma) and `writeOutputsToSecret`.
- Multi-cluster: where the Flux "management" cluster lives that creates GKE clusters.
- Notification controller → Slack/Datadog events.
