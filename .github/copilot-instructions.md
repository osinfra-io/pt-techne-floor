# Repository instructions for pt-techne-floor

Read this together with the platform- and techne-team instructions, both loaded by Copilot automatically:

- `pt-ai-context/.github/instructions/platform-group.instructions.md` — platform-wide conventions.
- `techne/pt-techne-ai-context/.github/instructions/techne-team.instructions.md` — techne team conventions.

## What this repo is

The factory floor: in-cluster OpenTofu plan, approval, and apply for the platform. Flux Operator and flux-iac/tofu-controller are the engine; this repo holds the thin custom layer and the floor configuration:

- `docs/superpowers/specs/` — design documents and spike findings. The current design is `2026-11-15-factory-floor-flux-design.md`; read it before changing anything.
- Floor configuration (planned) — the `GitRepository`, `Terraform`, and `ResourceSet` objects for each deployment repo, reconciled by a Flux `Kustomization`, never by OpenTofu.
- `floor-api` (planned) — Go MCP server behind agentgateway/authentik; the only writer of `spec.approvePlan`.

## Rules

- The OpenTofu code in the deployment repos is unchanged by this project; only the executor changes.
- A model never decides whether an apply happens. Approval is a human act recorded on the `Terraform` object.
- Flux and tofu-controller run off the ambient mesh (`flux-system` unlabelled); floor services run on it.
- Product names: lowercase **authentik** and **agentgateway** in prose.
