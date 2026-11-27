Read CLAUDE.md, docs/milestones.md and docs/agent-workflow.md. Then start the first unchecked milestone (or the next unchecked item of the current one).

Before writing code:
1. List the acceptance criteria (from the spec files the milestone names) that you will turn into tests.
2. List spec ambiguities with the decision you propose; write accepted decisions into the spec.

Then, on a branch named m<N>-<short-name>: write the tests first in tests/, implement until they pass, extend the end-to-end harness with this milestone's scenarios, do the cleanup pass (scripts/check-hygiene.sh green), keep scripts/ci.sh green, tick the boxes in docs/milestones.md, and open a PR describing the criteria covered (with test names), spec changes, and anything deferred.

Never touch this machine's real system (Incus, Docker, systemctl, firewall, /etc); use the fakes. UI must match reference/design/ (spec 16). Never commit secrets.
