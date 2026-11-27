//! `devbox sync` on one repository at a time, with real git: every state a checkout can be in
//! when its upstream has moved, and what must be true afterwards — updated with everything
//! local kept, or exactly as it was.

use devbox_sync::SyncStatus;
use devbox_tests::sandbox::Sandbox;

const CHANGED_LINE_TWO: &str = "one\nTWO upstream\nthree\nfour\nfive\n";

#[test]
fn a_clean_checkout_behind_its_upstream_is_fast_forwarded() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    for n in 1..=3 {
        s.push_upstream("app", "main", &format!("new-{n}.txt"), "new\n");
    }
    let result = s.sync_one("app");
    assert_eq!((result.status, result.detail.as_str()), (SyncStatus::Updated, "pulled 3 commits"));
    assert_eq!(s.git(&dir, &["rev-parse", "HEAD"]), s.git(&dir, &["rev-parse", "origin/main"]));
    assert!(dir.join("new-3.txt").exists());
    assert_eq!(s.git(&dir, &["status", "--porcelain"]), "");
    assert_eq!(s.git(&dir, &["for-each-ref", "refs/devbox-sync"]), "", "nothing was stashed");
}

#[test]
fn staged_unstaged_and_untracked_changes_survive_an_update() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "new.txt", "new\n");

    s.write(&dir, "staged.txt", "staged\n");
    s.git(&dir, &["add", "staged.txt"]);
    s.write(&dir, "README.md", "# app\nedited, not staged\n");
    s.write(&dir, "notes/untracked.txt", "untracked\n");
    s.write(&dir, ".gitignore", "*.log\n");
    s.write(&dir, "debug.log", "ignored\n");
    let before = s.snapshot(&dir);

    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (SyncStatus::Updated, "pulled 1 commit, kept your uncommitted changes")
    );
    assert_eq!(s.git(&dir, &["rev-parse", "HEAD"]), s.git(&dir, &["rev-parse", "origin/main"]));
    // What was staged is staged, what was not is not, what was untracked is untracked.
    assert_eq!(
        s.git(&dir, &["status", "--porcelain", "--untracked-files=all"]),
        " M README.md\nA  staged.txt\n?? .gitignore\n?? notes/untracked.txt"
    );
    let after = s.snapshot(&dir);
    for (path, content) in &before.files {
        assert_eq!(after.files.get(path), Some(content), "{path}");
    }
    assert!(after.files.contains_key("new.txt"));
    assert_eq!(after.stashes, "", "the stash was used up");
    // The safety ref still points at the stash commit that carried the changes.
    let backup = s.git(&dir, &["for-each-ref", "--format=%(refname)", "refs/devbox-sync"]);
    assert_eq!(backup, "refs/devbox-sync/backup-1790000000");
    assert_eq!(s.git(&dir, &["show", &format!("{backup}:staged.txt")]), "staged");
}

#[test]
fn uncommitted_changes_that_conflict_leave_the_checkout_exactly_as_it_was() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "src/lib.rs", CHANGED_LINE_TWO);
    s.push_upstream("app", "main", "other.txt", "x\n");

    s.write(&dir, "src/lib.rs", "one\nTWO mine\nthree\nfour\nfive\n");
    s.write(&dir, "staged.txt", "staged\n");
    s.git(&dir, &["add", "staged.txt"]);
    s.write(&dir, "untracked.txt", "untracked\n");
    s.write(&dir, ".git/info/exclude", "*.log\n");
    s.write(&dir, "debug.log", "ignored\n");
    let before = s.snapshot(&dir);

    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (
            SyncStatus::Conflict,
            "your uncommitted changes conflict with 2 new upstream commits; left exactly as it was"
        )
    );
    assert_eq!(s.snapshot(&dir), before);
}

#[test]
fn an_untracked_file_upstream_now_has_too_leaves_the_checkout_exactly_as_it_was() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "plan.md", "theirs\n");
    s.write(&dir, "plan.md", "mine\n");
    let before = s.snapshot(&dir);
    assert_eq!(s.sync_one("app").status, SyncStatus::Conflict);
    assert_eq!(s.snapshot(&dir), before);
}

#[test]
fn unpushed_commits_that_conflict_leave_the_checkout_exactly_as_it_was() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "src/lib.rs", CHANGED_LINE_TWO);
    s.push_upstream("app", "main", "a.txt", "a\n");
    s.push_upstream("app", "main", "b.txt", "b\n");

    s.write(&dir, "src/lib.rs", "one\nTWO mine\nthree\nfour\nfive\n");
    s.git(&dir, &["commit", "--quiet", "-am", "mine 1"]);
    s.write(&dir, "mine.txt", "mine\n");
    s.git(&dir, &["add", "."]);
    s.git(&dir, &["commit", "--quiet", "-m", "mine 2"]);
    // And changes on top that are not committed at all.
    s.write(&dir, "README.md", "# app\nedited\n");
    s.write(&dir, "untracked.txt", "untracked\n");
    let before = s.snapshot(&dir);

    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (
            SyncStatus::Conflict,
            "your 2 unpushed commits conflict with 3 upstream commits; left exactly as it was"
        )
    );
    assert_eq!(s.snapshot(&dir), before);
    assert!(!dir.join(".git/rebase-merge").exists(), "no rebase is left in progress");
}

#[test]
fn unpushed_commits_that_do_not_conflict_are_rebased() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "theirs.txt", "theirs\n");
    s.push_upstream("app", "main", "theirs-2.txt", "theirs\n");
    s.write(&dir, "mine.txt", "mine\n");
    s.git(&dir, &["add", "."]);
    s.git(&dir, &["commit", "--quiet", "-m", "mine"]);
    s.write(&dir, "README.md", "# app\nedited\n");

    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (SyncStatus::Updated, "pulled 2 commits, rebased 1 local, kept your uncommitted changes")
    );
    assert_eq!(
        s.git(&dir, &["log", "--format=%s", "-3"]),
        "mine\nupstream: theirs-2.txt\nupstream: theirs.txt"
    );
    assert_eq!(s.git(&dir, &["rev-list", "--count", "origin/main..HEAD"]), "1");
    assert_eq!(s.git(&dir, &["status", "--porcelain"]), " M README.md");

    // Nothing new upstream: the local commit is still there and said to be unpushed.
    let again = s.sync_one("app");
    assert_eq!(
        (again.status, again.detail.as_str()),
        (SyncStatus::Current, "up to date; 1 local commit not pushed")
    );
}

#[test]
fn an_up_to_date_checkout_is_left_alone_even_when_dirty() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.write(&dir, "README.md", "# app\nedited\n");
    s.write(&dir, "untracked.txt", "untracked\n");
    let before = s.snapshot(&dir);
    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (SyncStatus::Current, "up to date on main")
    );
    assert_eq!(s.snapshot(&dir), before);
    assert_eq!(s.git(&dir, &["for-each-ref", "refs/devbox-sync"]), "");
}

#[test]
fn a_detached_head_is_skipped() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "new.txt", "new\n");
    s.git(&dir, &["checkout", "--quiet", "--detach"]);
    let before = s.snapshot(&dir);
    let result = s.sync_one("app");
    assert_eq!((result.status, result.detail.as_str()), (SyncStatus::Skipped, "detached HEAD"));
    assert_eq!(s.snapshot(&dir), before);
}

#[test]
fn a_branch_without_an_upstream_is_skipped() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "new.txt", "new\n");
    s.git(&dir, &["checkout", "--quiet", "-b", "experiment"]);
    let before = s.snapshot(&dir);
    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        // main, which is not checked out, was moved forward on the way.
        (SyncStatus::Skipped, "branch experiment has no upstream")
    );
    assert_eq!(s.snapshot(&dir), before);
    assert_eq!(s.git(&dir, &["rev-parse", "main"]), s.git(&dir, &["rev-parse", "origin/main"]));
}

#[test]
fn a_branch_whose_upstream_was_deleted_is_skipped() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "feature", "feature.txt", "x\n");
    s.git(&dir, &["fetch", "--quiet"]);
    s.git(&dir, &["checkout", "--quiet", "feature"]);
    s.git(&s.remote("app"), &["branch", "-D", "feature"]);
    let before = s.snapshot(&dir);
    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (SyncStatus::Skipped, "the upstream of branch feature is gone")
    );
    assert_eq!(s.snapshot(&dir), before);
}

#[test]
fn a_repository_without_commits_is_skipped() {
    let s = Sandbox::new();
    let dir = s.repo("fresh");
    s.git(&s.code(), &["init", "--quiet", "fresh"]);
    s.write(&dir, "draft.txt", "draft\n");
    let result = s.sync_one("fresh");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (SyncStatus::Skipped, "branch main has no commits yet")
    );
    assert_eq!(std::fs::read_to_string(dir.join("draft.txt")).unwrap(), "draft\n");
}

#[test]
fn a_rebase_or_merge_in_progress_is_skipped() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "src/lib.rs", CHANGED_LINE_TWO);
    s.write(&dir, "src/lib.rs", "one\nTWO mine\nthree\nfour\nfive\n");
    s.git(&dir, &["commit", "--quiet", "-am", "mine"]);
    s.git(&dir, &["fetch", "--quiet"]);

    // The owner is in the middle of resolving a rebase by hand.
    assert!(!s.try_git(&dir, &["rebase", "origin/main"]).0);
    let before = s.snapshot(&dir);
    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (SyncStatus::Skipped, "a merge or rebase is in progress — finish it first")
    );
    assert_eq!(s.snapshot(&dir), before);
    assert!(dir.join(".git/rebase-merge").exists() || dir.join(".git/rebase-apply").exists());

    s.git(&dir, &["rebase", "--abort"]);
    assert!(!s.try_git(&dir, &["merge", "origin/main"]).0);
    let before = s.snapshot(&dir);
    assert_eq!(s.sync_one("app").status, SyncStatus::Skipped);
    assert_eq!(s.snapshot(&dir), before);
}

#[test]
fn a_failed_fetch_is_an_error_and_nothing_is_touched() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "new.txt", "new\n");
    s.write(&dir, "README.md", "# app\nedited\n");
    s.git(&dir, &["remote", "set-url", "origin", &s.remote("nowhere").to_string_lossy()]);
    let before = s.snapshot(&dir);

    let result = s.sync_one("app");
    assert_eq!(result.status, SyncStatus::Error);
    assert!(result.detail.starts_with("fetch failed: "), "{}", result.detail);
    assert!(result.detail.contains("nowhere.git"), "{}", result.detail);
    assert!(!result.detail.contains('\n') && result.detail.len() < 220, "{}", result.detail);
    assert_eq!(s.snapshot(&dir), before);
}

#[test]
fn other_branches_that_are_only_behind_are_fast_forwarded() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    for branch in ["behind", "diverged", "ahead"] {
        s.push_upstream("app", branch, &format!("{branch}.txt"), "1\n");
    }
    s.git(&dir, &["fetch", "--quiet"]);
    for branch in ["behind", "diverged", "ahead"] {
        s.git(&dir, &["branch", "--quiet", "--track", branch, &format!("origin/{branch}")]);
    }
    s.git(&dir, &["branch", "--quiet", "local-only"]);
    // "ahead" and "diverged" get a local commit each; "behind" and "diverged" get one upstream.
    for branch in ["ahead", "diverged"] {
        s.git(&dir, &["checkout", "--quiet", branch]);
        s.write(&dir, "local.txt", "local\n");
        s.git(&dir, &["add", "."]);
        s.git(&dir, &["commit", "--quiet", "-m", "local"]);
    }
    s.git(&dir, &["checkout", "--quiet", "main"]);
    for branch in ["behind", "diverged"] {
        s.push_upstream("app", branch, &format!("{branch}.txt"), "2\n");
    }
    let tips = |branches: &[&str]| -> Vec<String> {
        branches.iter().map(|b| s.git(&dir, &["rev-parse", b])).collect()
    };
    let untouched_before = tips(&["diverged", "ahead", "local-only"]);

    let result = s.sync_one("app");
    assert_eq!(
        (result.status, result.detail.as_str()),
        (SyncStatus::Current, "up to date on main (+1 other branch fast-forwarded)")
    );
    assert_eq!(tips(&["behind"]), tips(&["origin/behind"]));
    assert_eq!(tips(&["diverged", "ahead", "local-only"]), untouched_before);
    assert_eq!(s.git(&dir, &["symbolic-ref", "--short", "HEAD"]), "main");
}

#[test]
fn a_branch_checked_out_in_another_worktree_is_not_moved_under_it() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "feature", "feature.txt", "1\n");
    s.git(&dir, &["fetch", "--quiet"]);
    let worktree = s.home().join("feature-worktree");
    s.git(&dir, &["worktree", "add", "--quiet", &worktree.to_string_lossy(), "feature"]);
    s.push_upstream("app", "feature", "feature.txt", "2\n");
    let before = s.snapshot(&worktree);
    assert_eq!(s.sync_one("app").detail, "up to date on main");
    assert_eq!(s.snapshot(&worktree), before);
}

#[test]
fn safety_refs_are_kept_for_thirty_days() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    let head = s.git(&dir, &["rev-parse", "HEAD"]);
    let day = 86_400;
    let now = 1_790_000_000_u64;
    for made in [now - 31 * day, now - 29 * day] {
        s.git(&dir, &["update-ref", &format!("refs/devbox-sync/backup-{made}"), &head]);
    }
    s.sync();
    assert_eq!(
        s.git(&dir, &["for-each-ref", "--format=%(refname)", "refs/devbox-sync"]),
        format!("refs/devbox-sync/backup-{}", now - 29 * day)
    );
}

#[test]
fn an_older_stash_of_the_owner_is_left_in_place() {
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.write(&dir, "README.md", "# app\nan old idea\n");
    s.git(&dir, &["stash", "push", "--quiet", "-m", "old idea"]);
    let old = s.git(&dir, &["stash", "list", "--format=%H"]);

    // Once with an update that works, once with one that conflicts.
    s.push_upstream("app", "main", "new.txt", "new\n");
    s.write(&dir, "wip.txt", "wip\n");
    assert_eq!(s.sync_one("app").status, SyncStatus::Updated);
    assert_eq!(s.git(&dir, &["stash", "list", "--format=%H"]), old);

    s.push_upstream("app", "main", "wip.txt", "theirs\n");
    assert_eq!(s.sync_one("app").status, SyncStatus::Conflict);
    assert_eq!(s.git(&dir, &["stash", "list", "--format=%H"]), old);
    assert_eq!(std::fs::read_to_string(dir.join("wip.txt")).unwrap(), "wip\n");
}

#[test]
fn every_repository_gets_a_result_and_one_failing_does_not_stop_the_others() {
    let s = Sandbox::new();
    for name in ["alpha", "beta", "gamma"] {
        let _ = s.repository(name);
        s.push_upstream(name, "main", "new.txt", "new\n");
    }
    s.git(&s.repo("beta"), &["remote", "set-url", "origin", "/nowhere/beta.git"]);
    // Things in the code directory that are not repositories are not the sync's business.
    std::fs::create_dir_all(s.code().join("scratch")).unwrap();
    std::fs::create_dir_all(s.code().join(".hidden/.git")).unwrap();
    s.write(&s.code(), "a-file", "x");

    let results = s.sync();
    let summary: Vec<(&str, SyncStatus)> =
        results.iter().map(|result| (result.name.as_str(), result.status)).collect();
    assert_eq!(
        summary,
        [
            ("alpha", SyncStatus::Updated),
            ("beta", SyncStatus::Error),
            ("gamma", SyncStatus::Updated)
        ]
    );
}

#[test]
fn a_staged_deletion_with_an_untracked_file_in_its_place_is_put_back_exactly() {
    // `git stash apply` refuses this stash ("already exists, no checkout"), on any commit.
    let s = Sandbox::new();
    let dir = s.repository("app");
    s.push_upstream("app", "main", "new.txt", "new\n");
    std::fs::remove_file(dir.join("src/lib.rs")).unwrap();
    s.git(&dir, &["add", "--all", "--", "src/lib.rs"]);
    s.write(&dir, "src/lib.rs", "written again, not tracked\n");
    s.write(&dir, "README.md", "# app\nedited\n");
    s.write(&dir, "staged.txt", "staged\n");
    s.git(&dir, &["add", "staged.txt"]);
    let before = s.snapshot(&dir);
    assert!(before.index.contains("staged.txt") && !before.index.contains("src/lib.rs"));

    let result = s.sync_one("app");
    assert_eq!(result.status, SyncStatus::Conflict, "{}", result.detail);
    assert_eq!(s.snapshot(&dir), before);
    assert_eq!(
        s.git(&dir, &["status", "--porcelain", "--untracked-files=all"]),
        " M README.md\nD  src/lib.rs\nA  staged.txt\n?? src/lib.rs"
    );
    assert!(!dir.join(".git/devbox-sync-index").exists());
}
