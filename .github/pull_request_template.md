## What

<!-- Milestone / checkbox this PR covers -->

## Acceptance criteria covered

- [ ] … (test: `tests/...`)

## Spec changes

<!-- Decisions made where the spec was ambiguous; files touched in docs/spec/ -->

## Checklist

- [ ] `scripts/ci.sh` green
- [ ] Tests in `tests/`, against fakes only (no real Incus/Docker/systemd/firewall)
- [ ] End-to-end harness extended with this milestone's scenarios
- [ ] UI matches `reference/design/` (or the visual change is called out for approval)
- [ ] No secrets; contract changes went through `contracts/` + `scripts/gen.sh`
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green; no unused code/deps (staticcheck, deadcode, cargo machete, knip)
- [ ] Nothing personal or host-specific outside `reference/`; settled decisions written into the spec as fact (spec 17)
- [ ] `docs/milestones.md` boxes ticked
