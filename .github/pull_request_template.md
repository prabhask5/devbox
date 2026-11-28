## What

<!-- What this changes, and why -->

## Acceptance criteria covered

- … (test: `tests/...`)

## Spec changes

<!-- Decisions made where a spec was wrong or silent; files touched in docs/spec/ -->

## Checklist

- [ ] `scripts/ci.sh` green
- [ ] Tests in `tests/`, against the fakes only (no real Incus, Docker, systemd or firewall)
- [ ] The UI matches the visual baselines, or the change to them is called out for approval
- [ ] No secrets; contract changes went through `contracts/` + `scripts/gen.sh`
- [ ] Nothing personal or host-specific; decisions written into the spec as fact
- [ ] README, guides and CHANGELOG updated for what a user can see
