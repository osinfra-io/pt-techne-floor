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
