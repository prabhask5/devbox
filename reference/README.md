# reference/

Read-only input for building devbox. **Port behaviour, not code.** Deleted before 1.0 (M12) once everything here is captured in specs and tests; git history keeps it.

| Path | What |
|---|---|
| [`prototype/`](prototype/) | The first working version (Node + bash), built and tested against mocks in the dotfiles repo (PR prabhask5/dotfiles#1, closed in favour of this repo). Gateway (`host/devbox-gateway/`), hub + PWA (`devbox/hub/`), hook relay and CLIs (`devbox/bin/`), agent hooks (`agents/`), systemd units, VM bootstrap. `host/devbox-gateway/mock-vm.test-harness.js` is the mock Incus/VM used to test it |
| [`design/`](design/) | **Screenshots of the prototype UI — the approved design.** The new gateway UI and hub app must match these (layout, colours, typography, components, copy); see [docs/spec/16-design.md](../docs/spec/16-design.md) |
| [`cestmoi.md`](cestmoi.md) | The first real deployment: what exists today and what `devbox up --adopt` must preserve |

What the prototype proved (keep these behaviours; each has a test in the new specs):

- Wake with auth → phases → ETA from medians → sync → proxied page; idle stateful stop with clean-shutdown fallback; restore failure → cold boot; SSH wake waits for the sshd banner (an accepting port is not ready).
- tmux replaces control characters in `-F` output (use a printable separator or control mode); `pane_current_command` shows the interpreter for script agents (detect exit via the pane process's children instead).
- Claude fires `PermissionRequest` and, ~6 s later, `Notification(permission_prompt)` for the same prompt.
- Sending text then Enter immediately can be read as a paste; wait ~150 ms before Enter.
- Sync restores conflicting repos byte-for-byte (stash with untracked → reset → clean → re-apply with `--index`).
- Context: never count stash or backup refs as commits; only absolute dates so unchanged facts produce identical files.
- Docker Compose interpolates `$` in `.env` (hashes use `:` separators there).
