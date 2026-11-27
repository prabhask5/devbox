# 07 — Repo sync

`devbox sync [--json] [--repo <name>]` (Rust, crate `devbox-sync`) brings every repository in `~/code` up to date with its upstream. Runs on every wake (via the hub, spec 04), on cold boot, and on demand (`devbox sync`, app "Sync now"). Prototype: `reference/prototype/devbox/bin/devbox-sync` (bash) — its test matrix passed and must be ported as-is.

## Guarantee

**Never lose work.** After a sync, each repo is in exactly one of two states: updated with every local change preserved (commits, staged, unstaged, untracked files), or **byte-identical to before** (HEAD, index, working tree, untracked files, stashes) and reported. Ignored files are never touched.

## Algorithm

1. **Discover**: every directory of `~/code` that has a `.git` (hidden directories are not looked at). With a GitHub token — `GITHUB_TOKEN` or `GH_TOKEN`, else what `gh auth token` prints — the owner's repositories are listed and those not in `~/code` are cloned. Without a token GitHub is not asked, which is not an error.
   - **Listing**: `GET /user/repos?affiliation=owner&per_page=100`, every page. With `owner` or `include_orgs` in the dotfiles manifest (spec 10) the listing is `affiliation=owner,organization_member` and only repositories of `owner` (absent: the login from `GET /user`) and of `include_orgs` are kept. Archived repositories are left out unless `archived = true`; names matching an `exclude` glob (`*`, `?`, case-insensitive) are left out. Of two repositories with the same name the first listed wins.
   - The API is asked with `curl`, run through `System`: the token is given on curl's standard input (`--config -`), never on a command line. A listing that fails is one `error` result named `github`; a manifest that is not valid is one named `dotfiles`; in both cases nothing is cloned and the local repositories are still synced.
   - **Clone**: `git clone` over SSH (after making sure `github.com` is in `~/.ssh/known_hosts`, with `ssh-keyscan`), and when that fails over HTTPS with the token in that one git's environment (`http.extraHeader`), so it is neither on a command line nor written into the clone. Anything already in `~/code` under the repository's name — a repository or not — is left alone and nothing is cloned over it.
2. **Fetch** all repos in parallel (6 at a time), `git fetch --all --prune`, each with a 60 s timeout. A fetch failure marks that repo `error` and it is not touched further.
3. **Update** each repo, in name order (sequential; local and fast):
   - skip (`skipped`) if a merge, rebase, cherry-pick or revert is in progress, HEAD is detached, the branch has no commits yet, has no upstream, or its upstream is gone;
   - fast-forward other local branches strictly behind their upstream (`update-ref` with the old value; no checkout), except branches checked out in another worktree;
   - if not behind: `current` ("up to date on main", or "up to date; 2 local commits not pushed");
   - remember `before = HEAD`; if the tree is dirty, `stash push --include-untracked` and keep a safety ref `refs/devbox-sync/backup-<unix>` to the stash commit (stashes the owner already had are never touched);
   - no local commits → `merge --ff-only @{u}`; local commits → `rebase @{u}`;
   - re-apply the stash on the new HEAD with `--index`, so what was staged stays staged; when the index cannot be carried over, as plain changes; then drop it → `updated` ("pulled 3 commits, rebased 1 local, kept your uncommitted changes");
   - on any failure: **restore** — abort the rebase, `reset --hard before`, `clean -fd` only when there is a stash (it then removes just what a failed apply created: every untracked file is in the stash, and ignored files are not cleaned), re-apply the stash with `--index`; when `git stash apply` refuses even on the commit the stash was made on (it does for a path whose deletion is staged while an untracked file has taken its place), rebuild the state from the three trees the stash commit keeps — working tree, untracked files, index — through a scratch index; drop the stash → `conflict` with a reason ("your 2 unpushed commits conflict with 3 upstream commits"; "your uncommitted changes conflict with 3 new upstream commits"; "couldn't fast-forward") followed by "; left exactly as it was".
4. Safety refs older than 30 days are deleted. A stash that could not be re-applied (should be impossible) is left in `git stash list` and reported as `error`, naming the stash list and the safety ref.

One `git for-each-ref` per repository answers everything the common case needs (branch, upstream, ahead/behind, other branches, safety refs), so a repository with nothing to pull costs a fetch and that.

Git operations use the `git` binary (matching the owner's config: hooks, credential helpers), invoked through `System` with `GIT_TERMINAL_PROMPT=0`, `ssh -o BatchMode=yes -o ConnectTimeout=15` and `LC_ALL=C`. No git library is linked.

Git does not record empty directories: an untracked directory with nothing in it is not work and may be gone after a sync.

## Where

`~/code`, or `DEVBOX_CODE_DIR`. The log is `sync.log` in `~/.local/state/devbox` (`XDG_STATE_HOME/devbox`, or `DEVBOX_STATE_DIR`). The GitHub API is `https://api.github.com`, or `DEVBOX_GITHUB_API`. `--repo <name>` syncs that one directory of the code directory, fetches no other and clones nothing.

## Output

Human: `Syncing 6 repos in /home/you/code, cloning 1`, then one line per repo: two spaces, an icon (`↓` updated or cloned, `⚠` conflict, `✕` error, `✓` current, `·` skipped), the name padded to 30 columns, the detail. `--json`: NDJSON per the `sync-progress` schema (`start{total}`, `begin{name}` — including the pseudo-name `fetching` during step 2 —, `repo{name, status, detail}`, `done`); clones are reported before the updates. Status ∈ `current | updated | cloned | conflict | skipped | error`. Every result is appended to `sync.log` as `<RFC 3339 time> <name> <status> <detail>`, between a start and a done line. Exit code 0 even with conflicts and errors of single repositories (they are reported); 2 when the sync could not run at all (the directories cannot be used, `--repo` names no repository).

The library is `devbox_sync::sync(system, options, report)`: it reports each step to a callback and returns the results; `devbox sync` only formats them.

## Acceptance

- [x] Port the prototype's matrix with real git in temp dirs: clean behind → updated; dirty + staged + untracked behind → updated with everything kept (staged stays staged); dirty conflicting → conflict, byte-identical restore; unpushed commits conflicting → conflict, byte-identical; unpushed commits non-conflicting → rebased; up to date; detached → skipped; no upstream → skipped; rebase in progress → skipped; fetch failure → error, untouched; other branches fast-forwarded. (`tests/rust/tests/sync_matrix.rs`)
- [x] Property test (proptest): random working-tree states (files added/modified/deleted/staged/untracked/binary/renamed) × random upstream changes → either all local changes present after sync or the repo is byte-identical (every file of the work dir with its mode + index + untracked list + stash list compared). 40 cases per run, more with `PROPTEST_CASES`. (`tests/rust/tests/sync_property.rs`)
- [x] Clone of missing repos through a fake GitHub API + local bare remotes; archived repos and manifest filters respected. (`tests/rust/tests/sync_clone.rs`; on a machine that exists only in the test — host key, token never on a command line: `tests/rust/tests/sync_system.rs`)
- [x] `--json` output golden; human output golden. (`tests/rust/tests/sync_output.rs` against `tests/fixtures/sync/`; the hub reads the same golden: `TestTheHubUnderstandsWhatDevboxSyncPrints`)
- [x] Budget (13): 11 repos, nothing to pull, local remotes → ≤ 600 ms. (`tests/perf/sync`)
