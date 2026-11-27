//! Updating one repository: fast-forward or rebase onto its upstream with every local change
//! kept, or leave it exactly as it was.

use std::path::Path;

use devbox_core::proto::SyncStatus;
use devbox_core::system::System;

use crate::RepoResult;
use crate::git::Git;
use crate::text::{count, one_line, rfc3339};

/// How long a safety ref to a stash is kept.
const BACKUP_SECONDS: u64 = 30 * 86400;
const BACKUP_PREFIX: &str = "refs/devbox-sync/backup-";

/// A local branch, as `git for-each-ref` describes it.
struct Branch {
    name: String,
    commit: String,
    upstream: String,
    ahead: u64,
    behind: u64,
    gone: bool,
    head: bool,
    checked_out: bool,
}

fn result(name: &str, status: SyncStatus, detail: impl Into<String>) -> RepoResult {
    RepoResult { name: name.to_owned(), status, detail: detail.into() }
}

/// `ahead 2, behind 3` as numbers.
fn track(text: &str) -> (u64, u64) {
    let number = |label: &str| {
        text.split(", ")
            .find_map(|part| part.strip_prefix(label))
            .and_then(|n| n.trim().parse().ok())
            .unwrap_or(0)
    };
    (number("ahead "), number("behind "))
}

/// The local branches, and the safety refs that are older than they need to be.
fn refs(git: Git, now: u64) -> Option<(Vec<Branch>, Vec<String>)> {
    let format = "--format=%(HEAD)%00%(refname)%00%(objectname)%00%(upstream)%00%(upstream:track,nobracket)%00%(worktreepath)";
    let listing = git.text(&["for-each-ref", format, "refs/heads", "refs/devbox-sync"])?;
    let (mut branches, mut expired) = (Vec::new(), Vec::new());
    for line in listing.lines() {
        let fields: Vec<&str> = line.split('\0').collect();
        let [head, refname, commit, upstream, tracking, worktree] = fields[..] else { continue };
        if let Some(made) = refname.strip_prefix(BACKUP_PREFIX) {
            if made.parse::<u64>().is_ok_and(|made| made + BACKUP_SECONDS < now) {
                expired.push(refname.to_owned());
            }
        } else if let Some(name) = refname.strip_prefix("refs/heads/") {
            let (ahead, behind) = track(tracking);
            branches.push(Branch {
                name: name.to_owned(),
                commit: commit.to_owned(),
                upstream: upstream.to_owned(),
                ahead,
                behind,
                gone: tracking == "gone",
                head: head == "*",
                checked_out: !worktree.is_empty(),
            });
        }
    }
    Some((branches, expired))
}

/// Whether the repository is in the middle of something only its owner can finish.
fn busy(system: &dyn System, git_dir: &Path) -> bool {
    ["rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"]
        .iter()
        .any(|marker| system.exists(&git_dir.join(marker)))
}

/// Removes the stash entry that is `commit` from the stash list; other entries stay.
fn drop_stash(git: Git, commit: &str) {
    let Some(list) = git.text(&["stash", "list", "--format=%H"]) else { return };
    if let Some(index) = list.lines().position(|entry| entry == commit) {
        git.ok(&["stash", "drop", "--quiet", &format!("stash@{{{index}}}")]);
    }
}

/// Puts back what a stash holds on top of `base`: with the index as it was when that works,
/// else as plain changes. Whatever a failed attempt left behind is cleared before the next one;
/// that is safe because everything local, untracked files included, is in the stash.
fn apply_stash(git: Git, base: &str, stash: &str) -> bool {
    if git.ok(&["stash", "apply", "--index", "--quiet", stash]) {
        return true;
    }
    git.ok(&["reset", "--hard", "--quiet", base]);
    git.ok(&["clean", "-fdq"]);
    if git.ok(&["stash", "apply", "--quiet", stash]) {
        return true;
    }
    git.ok(&["reset", "--hard", "--quiet", base]);
    git.ok(&["clean", "-fdq"]);
    false
}

/// Puts the repository back exactly as it was before the update was tried. `Err` when the
/// uncommitted changes could not be put back: they are then still in the stash.
fn restore(git: Git, before: &str, stash: Option<&str>) -> Result<(), ()> {
    git.ok(&["rebase", "--abort"]);
    git.ok(&["reset", "--hard", "--quiet", before]);
    git.ok(&["clean", "-fdq"]);
    let Some(stash) = stash else { return Ok(()) };
    if apply_stash(git, before, stash) {
        drop_stash(git, stash);
        Ok(())
    } else {
        Err(())
    }
}

fn not_restored(name: &str, backup: &str) -> RepoResult {
    result(
        name,
        SyncStatus::Error,
        format!(
            "couldn't put your uncommitted changes back; they are safe in `git stash list` and in {backup}"
        ),
    )
}

/// A repository that is behind its upstream and may be updated.
struct Outdated {
    before: String,
    ahead: u64,
    behind: u64,
    /// What to add to the result about other branches.
    extra: String,
}

/// Looks at a repository and does what needs no care: expires old safety refs and moves other
/// branches forward. `Err` is the result for a repository that is not to be updated.
fn assess(
    git: Git,
    name: &str,
    fetch_error: Option<&str>,
    now: u64,
) -> Result<Outdated, RepoResult> {
    let mut git_dir = git.dir.join(".git");
    if git.system.read_dir(&git_dir).is_err() {
        // A worktree or a submodule: `.git` is a file that says where the real one is.
        match git.text(&["rev-parse", "--absolute-git-dir"]) {
            Some(path) => git_dir = path.into(),
            None => return Err(result(name, SyncStatus::Error, "not a git repository")),
        }
    }
    if busy(git.system, &git_dir) {
        let detail = "a merge or rebase is in progress — finish it first";
        return Err(result(name, SyncStatus::Skipped, detail));
    }
    if let Some(error) = fetch_error {
        return Err(result(name, SyncStatus::Error, format!("fetch failed: {error}")));
    }
    let Some((branches, expired)) = refs(git, now) else {
        return Err(result(name, SyncStatus::Error, "couldn't read its branches"));
    };
    for backup in &expired {
        git.ok(&["update-ref", "-d", backup]);
    }

    // Other branches that are strictly behind their upstream move forward. No checkout is
    // involved, and the old value is given, so a branch that moved in the meantime stays.
    let mut others = 0;
    for branch in &branches {
        let behind_only = branch.ahead == 0 && branch.behind > 0 && !branch.upstream.is_empty();
        if !branch.head && !branch.checked_out && behind_only {
            let target = format!("refs/heads/{}", branch.name);
            if git.ok(&["update-ref", &target, &branch.upstream, &branch.commit]) {
                others += 1;
            }
        }
    }
    let extra = match others {
        0 => String::new(),
        1 => " (+1 other branch fast-forwarded)".to_owned(),
        n => format!(" (+{n} other branches fast-forwarded)"),
    };

    let skipped = |detail: String| Err(result(name, SyncStatus::Skipped, detail));
    let Some(current) = branches.iter().find(|branch| branch.head) else {
        return match git.text(&["symbolic-ref", "--quiet", "--short", "HEAD"]) {
            Some(unborn) => skipped(format!("branch {unborn} has no commits yet")),
            None => skipped("detached HEAD".to_owned()),
        };
    };
    if current.upstream.is_empty() {
        return skipped(format!("branch {} has no upstream", current.name));
    }
    if current.gone {
        return skipped(format!("the upstream of branch {} is gone", current.name));
    }
    if current.behind == 0 {
        let detail = if current.ahead > 0 {
            format!("up to date; {} not pushed{extra}", count(current.ahead, "local commit"))
        } else {
            format!("up to date on {}{extra}", current.name)
        };
        return Err(result(name, SyncStatus::Current, detail));
    }
    Ok(Outdated {
        before: current.commit.clone(),
        ahead: current.ahead,
        behind: current.behind,
        extra,
    })
}

/// A stash of the uncommitted changes and the ref that keeps it alive.
struct Stash {
    commit: String,
    backup: String,
}

/// Puts uncommitted changes, untracked files included, into a stash, and points a ref of our
/// own at that stash commit so it survives whatever happens to the stash list. `Ok(None)` when
/// there is nothing to stash.
fn stash_changes(git: Git, name: &str, now: u64) -> Result<Option<Stash>, RepoResult> {
    let clean = git
        .text(&["status", "--porcelain", "--untracked-files=normal"])
        .is_some_and(|status| status.is_empty());
    if clean {
        return Ok(None);
    }
    let stashed_before = git.text(&["rev-parse", "--quiet", "--verify", "refs/stash"]);
    let message = format!("devbox-sync {}", rfc3339(now));
    if !git.ok(&["stash", "push", "--include-untracked", "--quiet", "-m", &message]) {
        let detail = "couldn't stash local changes; left untouched";
        return Err(result(name, SyncStatus::Error, detail));
    }
    // Changes git has nothing to stash for (inside a submodule, say) make no new entry.
    match git.text(&["rev-parse", "--quiet", "--verify", "refs/stash"]) {
        Some(commit) if Some(&commit) != stashed_before.as_ref() => {
            let backup = format!("{BACKUP_PREFIX}{now}");
            git.ok(&["update-ref", &backup, &commit]);
            Ok(Some(Stash { commit, backup }))
        }
        _ => Ok(None),
    }
}

/// Brings one repository up to date with its upstream, or leaves it exactly as it was.
/// `fetch_error` is why fetching it failed, if it did.
pub(crate) fn update(
    system: &dyn System,
    dir: &Path,
    name: &str,
    fetch_error: Option<&str>,
    now: u64,
) -> RepoResult {
    let git = Git { system, dir };
    let Outdated { before, ahead, behind, extra } = match assess(git, name, fetch_error, now) {
        Ok(outdated) => outdated,
        Err(done) => return done,
    };
    let stash = match stash_changes(git, name, now) {
        Ok(stash) => stash,
        Err(done) => return done,
    };
    let stashed = stash.as_ref().map(|stash| stash.commit.as_str());
    // Back to how it was, and say why; or say where the changes are if even that failed.
    let give_up = |why: String| match restore(git, &before, stashed) {
        Ok(()) => result(name, SyncStatus::Conflict, format!("{why}; left exactly as it was")),
        Err(()) => not_restored(name, stash.as_ref().map_or("", |stash| stash.backup.as_str())),
    };

    if ahead == 0 {
        if !git.ok(&["merge", "--ff-only", "--quiet", "@{u}"]) {
            return give_up("couldn't fast-forward".to_owned());
        }
    } else if !git.ok(&["rebase", "--quiet", "@{u}"]) {
        return give_up(format!(
            "your {} {} with {}",
            count(ahead, "unpushed commit"),
            if ahead == 1 { "conflicts" } else { "conflict" },
            count(behind, "upstream commit")
        ));
    }
    if let Some(stashed) = stashed {
        let updated = git.text(&["rev-parse", "HEAD"]).unwrap_or_default();
        if !apply_stash(git, &updated, stashed) {
            return give_up(format!(
                "your uncommitted changes conflict with {}",
                count(behind, "new upstream commit")
            ));
        }
        drop_stash(git, stashed);
    }
    let mut detail = format!("pulled {}", count(behind, "commit"));
    if ahead > 0 {
        detail = format!("{detail}, rebased {ahead} local");
    }
    if stashed.is_some() {
        detail.push_str(", kept your uncommitted changes");
    }
    result(name, SyncStatus::Updated, detail + &extra)
}

/// Fetches everything for one repository; `Some` is why it failed.
pub(crate) fn fetch(system: &dyn System, dir: &Path) -> Option<String> {
    const LIMIT: std::time::Duration = std::time::Duration::from_secs(60);
    match (Git { system, dir }).run_within(LIMIT, &["fetch", "--all", "--prune", "--quiet"]) {
        Ok(output) if output.success() => None,
        Ok(output) if output.timed_out => Some("no answer within 60 s".to_owned()),
        Ok(output) => Some(one_line(&output.error_text(), 200)),
        Err(error) => Some(format!("couldn't run git: {error}")),
    }
}
