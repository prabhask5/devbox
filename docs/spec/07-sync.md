# 07 — Repo sync

`devbox sync [--json] [--repo <name>]` (Rust, crate `devbox-sync`) brings every repository in `~/code` up to date with its upstream. Runs on every wake (via the hub, spec 04), on cold boot, and on demand (`devbox sync`, app "Sync now"). Prototype: `reference/prototype/devbox/bin/devbox-sync` (bash) — its test matrix passed and must be ported as-is.

## Guarantee

**Never lose work.** After a sync, each repo is in exactly one of two states: updated with every local change preserved (commits, staged, unstaged, untracked files), or **byte-identical to before** (HEAD, index, working tree, untracked files, stashes) and reported. Ignored files are never touched.

## Algorithm

1. **Discover**: every `~/code/*/.git`. If `gh` is authenticated (or `GITHUB_TOKEN` is set), list the owner's repos (`GET /user/repos?affiliation=owner`, non-archived; filters from the dotfiles manifest, spec 10) and clone missing ones (`git clone` over SSH, falling back to HTTPS with the token). Ensure `github.com` is in `known_hosts`.
2. **Fetch** all repos in parallel (bounded, 6), `git fetch --all --prune`, each with a 60 s timeout. A fetch failure marks that repo `error` and it is not touched further.
3. **Update** each repo (sequential; local and fast):
   - skip (`skipped`) if a merge/rebase/cherry-pick/revert is in progress, HEAD is detached, or the branch has no upstream;
   - fast-forward other local branches strictly behind their upstream (`update-ref` with the old value; no checkout);
   - if not behind: `current` ("up to date", or "N local commit(s) not pushed");
   - remember `before = HEAD`; if the tree is dirty, `stash push --include-untracked` and keep a safety ref `refs/devbox-sync/backup-<unix>` to the stash commit;
   - no local commits → `merge --ff-only @{u}`; local commits → `rebase @{u}` (abort on conflict);
   - on any failure: **restore** — `reset --hard before`, `clean -fd` (only files a failed apply created; all untracked files were stashed), re-apply the stash with `--index` (falls back to plain), drop it → `conflict` with a reason ("your 2 unpushed commits conflict with 3 upstream commits"; "your uncommitted changes conflict with 3 new upstream commits"), "left exactly as it was";
   - re-apply the stash on the new HEAD; if that conflicts → restore as above → `conflict`; else drop it → `updated` ("pulled 3, rebased 1 local, kept your uncommitted changes").
4. Safety refs older than 30 days are deleted. A stash that could not be re-applied (should be impossible) is left in `git stash list` and reported as `error` with instructions.

Git operations use the `git` binary (matching the owner's config: hooks, credential helpers, `pull.rebase`), invoked through `System` with `GIT_TERMINAL_PROMPT=0` and `ssh -o BatchMode=yes`. (Using `gix` for the read-only parts — status, ahead/behind — is allowed where it is measurably faster.)

## Output

Human: one line per repo (`↓ name  pulled 3 commit(s)`, `⚠`, `✕`, `✓`, `·`). `--json`: NDJSON per the `sync-progress` schema (`start{total}`, `begin{name}` — including the pseudo-name `fetching` during step 2 —, `repo{name, status, detail}`, `done`). Status ∈ `current | updated | cloned | conflict | skipped | error`. Every result is appended to `~/.local/state/devbox/sync.log`. Exit code 0 even with conflicts (they are reported); 2 on an internal error.

## Acceptance

- [ ] Port the prototype's matrix with real git in temp dirs: clean behind → updated; dirty + staged + untracked behind → updated with everything kept (staged stays staged); dirty conflicting → conflict, byte-identical restore; unpushed commits conflicting → conflict, byte-identical; unpushed commits non-conflicting → rebased; up to date; detached → skipped; no upstream → skipped; rebase in progress → skipped; fetch failure → error, untouched; other branches fast-forwarded.
- [ ] Property test (proptest): random working-tree states (files added/modified/deleted/staged/untracked/binary/renamed) × random upstream changes → either all local changes present after sync or the repo is byte-identical (tree hash of the work dir + index + untracked list + stash list compared).
- [ ] Clone of missing repos through a fake GitHub API + local bare remotes; archived repos and manifest filters respected.
- [ ] `--json` output golden; human output golden.
- [ ] Budget (13): 11 repos, nothing to pull, local remotes → ≤ 600 ms.
