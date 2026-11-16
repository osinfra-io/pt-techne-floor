# Techne Floor

The factory floor: in-cluster OpenTofu plan, approval, and apply for the osinfra.io platform, built on Flux Operator and flux-iac/tofu-controller. It replaces the GitHub Actions deployment pipelines in `pt-logos`, `pt-corpus`, and `pt-pneuma` with Kubernetes objects a platform-team member approves from a CLI.

Design: [`docs/superpowers/specs/2026-11-15-factory-floor-flux-design.md`](docs/superpowers/specs/2026-11-15-factory-floor-flux-design.md). Spike evidence: [`docs/superpowers/specs/2026-11-15-flux-tofu-controller-spike-findings.md`](docs/superpowers/specs/2026-11-15-flux-tofu-controller-spike-findings.md).

## Local milestone

Prerequisites: `gh auth login` with a token that can read pull requests, comment, and merge with admin bypass on `osinfra-io/pt-techne-floor-fixture`; Docker Desktop with Kubernetes enabled.

Run the local integration stack from `pt-pneuma`:

```bash
tests/integration/setup.sh
tests/integration/verify.sh
```

Set `FLOOR_CONFIG_REF=<branch>` before `setup.sh` to point the cluster at an unmerged `pt-techne-floor` branch instead of the default `main`.

## Sandbox seed bootstrap

With the local floor running and `gcloud auth application-default login` completed, run `bash bootstrap/sandbox/prepare.sh`. This opt-in path uses folder `648523111758` and the existing billing account to **plan**, not automatically apply, a dedicated seed project, versioned private state bucket, and KMS key. It creates no GKE cluster, networking, team projects, or service-account keys. Logos/Corpus bootstrap and the eventual GKE handoff are subsequent stages, not implemented by this seed root.

Only a short-lived Google access token is delivered to the runner; the local ADC refresh credential is never copied. Rerun the preparation script before approval if the token expires. The bootstrap uses its own `pt-bootstrap-sandbox` namespace, outside the disposable fixture namespace. Its encrypted Kubernetes state and `.work/bootstrap-sandbox/encryption.hcl` are both required for recovery: preserve them securely and do not reset Docker Desktop. The seed state cannot depend on the GCS/KMS resources it is creating. Moving that state to durable storage is a separate, explicit migration after the first apply; never start a second owner with empty state.

After preparing the bootstrap namespace, rerun `tests/integration/floor/setup.sh` from `pt-pneuma` with `FLOOR_ADDITIONAL_NAMESPACES=pt-bootstrap-sandbox` to expose it through the same authenticated floor API as the fixtures. Retain that opt-in on subsequent setup runs. Additional namespaces receive namespace-scoped API access, never access to their credential or state Secrets; the fixture's PR source-repointing remains restricted to its own namespace.

The floor's `list_workspaces` and `pending_approvals` report `pt-bootstrap-sandbox/bootstrap-sandbox`; use that qualified name with `get_workspace` and the normal human approval tools. Inspect the readable plan in the bootstrap namespace before approval. No approval is committed in Git. Neither a Git push nor running `prepare.sh` or the local setup approves cloud changes.

After the seed is Ready, rerun `prepare.sh` to refresh the short-lived credential and prepare separate KMS-backed encryption for the Logos stages. The seed enables Cloud Billing, Cloud Identity, Billing Budgets, and IAM APIs and creates keyless `pt-corpus-floor-sb` and `pt-pneuma-floor-sb` service accounts before `logos-global` can run. That stage uses the distinct `pt-logos-global` GCS workspace and exclusively owns the shared Google billing-users, state-readers, and GKE security groups. It delegates billing/state-reader membership administration to the Corpus identity and GKE nesting to the Pneuma identity. Consumers own their individual memberships, never the global manager memberships; adding a consumer does not require another global apply. These identities receive no domain-wide admin role or service-account keys; runtime trust is configured during the later floor handoff. Do not register this global owner on every environment's floor. Existing Google groups require an explicit state handoff before enabling it; GitHub/Datadog resources remain with their current Production owners.

`logos-foundation-sandbox` follows the global stage and keeps the existing `pt-logos-main-sandbox` state, its Sandbox root and four team-type folders. It adds the Logos team's resources from `teams/pt-logos.tfvars`; the other six team workspaces follow it using their canonical tfvars files. Only the Corpus team state owns the three environment-root access grants. Each stage needs its own human approval.

All Logos stages use the seed GCS bucket with prefix `pt-logos` and distinct workspace state keys. The shared inputs select that bucket for foundation reads and the seed project for user-credential API quota. This does not migrate or replace the seed's encrypted Kubernetes state. Preserve the seed project and KMS key while any foundation depends on them. Core Helpers/Corpus bootstrap and the eventual GKE handoff remain subsequent stages.

Flux source archives omit repository symlinks. The Logos source therefore uses native artifact includes to assemble `workspace/` with the canonical shared provider and backend files, without duplicating them in Git. Both GitRepository references are pinned to the same Logos commit. Their names also include that full commit: update both names, the include references and the shared Terraform source reference when changing the pin. This prevents the composed source from consuming the previous raw artifact while the new revision is being fetched.
