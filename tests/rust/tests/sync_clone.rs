//! `devbox sync` finds the owner's repositories on GitHub that are not on this machine and
//! clones them: through a fake GitHub API, from local bare repositories.

use std::process::ExitCode;

use devbox_sync::{Event, RepoResult, SyncStatus};
use devbox_tests::github::{FakeGitHub, TOKEN, repo};
use devbox_tests::sandbox::Sandbox;
use serde_json::{Value, json};

/// A repository on the fake GitHub whose URLs lead to a local bare repository.
fn hosted(s: &Sandbox, owner: &str, name: &str) -> Value {
    s.upstream(name);
    let url = s.remote(name).to_string_lossy().into_owned();
    repo(owner, name, &url, &url)
}

fn signed_in(s: &mut Sandbox, github: &FakeGitHub) {
    s.set_env("GITHUB_TOKEN", TOKEN);
    s.set_env("DEVBOX_GITHUB_API", &github.url);
}

fn summary(results: &[RepoResult]) -> Vec<(&str, SyncStatus)> {
    results.iter().map(|result| (result.name.as_str(), result.status)).collect()
}

#[test]
fn missing_repositories_are_cloned_and_existing_ones_synced() {
    let mut s = Sandbox::new();
    let _ = s.repository("app");
    s.push_upstream("app", "main", "new.txt", "new\n");
    let mut attic = hosted(&s, "dev", "attic");
    attic["archived"] = json!(true);
    let github = FakeGitHub::start(
        "dev",
        vec![
            repo("dev", "app", "unused", "unused"),
            hosted(&s, "dev", "site"),
            attic,
            hosted(&s, "acme", "team-tool"),
        ],
    );
    signed_in(&mut s, &github);

    let (results, events) = s.sync_events(&s.options());
    assert_eq!(summary(&results), [("site", SyncStatus::Cloned), ("app", SyncStatus::Updated)]);
    assert_eq!(results[0].detail, "new on GitHub");
    let cloned = |name: &str, status: SyncStatus, detail: &str| {
        Event::Repo(RepoResult { name: name.to_owned(), status, detail: detail.to_owned() })
    };
    assert_eq!(
        events,
        [
            Event::Start { local: 1, new: 1 },
            Event::Begin("fetching".to_owned()),
            Event::Begin("site".to_owned()),
            cloned("site", SyncStatus::Cloned, "new on GitHub"),
            Event::Begin("app".to_owned()),
            cloned("app", SyncStatus::Updated, "pulled 1 commit"),
            Event::Done,
        ]
    );
    assert_eq!(s.git(&s.repo("site"), &["log", "--format=%s"]), "first");
    assert!(!s.repo("attic").exists(), "archived repositories are left out");
    assert!(!s.repo("team-tool").exists(), "repositories of others are left out");

    // One listing, asked the way the owner's own token allows, and nothing else.
    assert_eq!(github.targets(), ["/user/repos?affiliation=owner&per_page=100&page=1"]);
    assert_eq!(github.requests()[0].authorization, format!("Bearer {TOKEN}"));

    // The next sync has nothing to clone.
    assert_eq!(summary(&s.sync()), [("app", SyncStatus::Current), ("site", SyncStatus::Current)]);
}

#[test]
fn the_dotfiles_manifest_says_which_repositories_are_wanted() {
    let mut s = Sandbox::new();
    let mut attic = hosted(&s, "dev", "attic");
    attic["archived"] = json!(true);
    let github = FakeGitHub::start(
        "dev",
        vec![
            hosted(&s, "dev", "site"),
            hosted(&s, "dev", "old-blog"),
            hosted(&s, "dev", "Old-Notes"),
            attic,
            hosted(&s, "acme", "team-tool"),
            hosted(&s, "elsewhere", "not-mine"),
            repo("acme", "site", "/nowhere/duplicate.git", ""),
        ],
    );
    signed_in(&mut s, &github);
    s.write(
        &s.home(),
        "dotfiles/devbox.toml",
        "version = 1\n[sync]\ninclude_orgs = [\"Acme\"]\nexclude = [\"old-*\"]\narchived = true\n",
    );
    let results = s.sync();
    assert_eq!(
        summary(&results),
        [
            ("site", SyncStatus::Cloned),
            ("attic", SyncStatus::Cloned),
            ("team-tool", SyncStatus::Cloned)
        ]
    );
    // Who "the owner" is had to be asked, because the manifest does not say.
    assert_eq!(
        github.targets(),
        ["/user", "/user/repos?affiliation=owner,organization_member&per_page=100&page=1"]
    );

    // With an owner named, nobody needs to be asked, and only that owner's repositories count.
    let mut other = Sandbox::new();
    let github = FakeGitHub::start(
        "dev",
        vec![hosted(&other, "dev", "site"), hosted(&other, "acme", "team-tool")],
    );
    signed_in(&mut other, &github);
    other.write(&other.home(), "dotfiles/devbox.toml", "version = 1\n[sync]\nowner = \"acme\"\n");
    assert_eq!(summary(&other.sync()), [("team-tool", SyncStatus::Cloned)]);
    assert_eq!(
        github.targets(),
        ["/user/repos?affiliation=owner,organization_member&per_page=100&page=1"]
    );
}

#[test]
fn every_page_of_the_listing_is_read() {
    let mut s = Sandbox::new();
    let mut repos: Vec<Value> =
        (0..230).map(|n| repo("dev", &format!("bulk-{n:03}"), "/nowhere", "")).collect();
    repos.push(hosted(&s, "dev", "last"));
    let github = FakeGitHub::start("dev", repos);
    signed_in(&mut s, &github);
    s.write(&s.home(), "dotfiles/devbox.toml", "version = 1\n[sync]\nexclude = [\"bulk-*\"]\n");
    assert_eq!(summary(&s.sync()), [("last", SyncStatus::Cloned)]);
    assert_eq!(github.targets().len(), 3);
    assert!(github.targets()[2].ends_with("&page=3"));
}

#[test]
fn a_clone_that_fails_over_ssh_is_tried_over_https_and_a_failed_one_is_reported() {
    let mut s = Sandbox::new();
    s.upstream("site");
    let good = s.remote("site").to_string_lossy().into_owned();
    let github = FakeGitHub::start(
        "dev",
        vec![
            repo("dev", "site", "/nowhere/site.git", &good),
            repo("dev", "broken", "/nowhere/broken.git", "/nowhere/either.git"),
            repo("dev", "ssh-only", "/nowhere/ssh-only.git", ""),
        ],
    );
    signed_in(&mut s, &github);
    let results = s.sync();
    assert_eq!(
        summary(&results),
        [
            ("site", SyncStatus::Cloned),
            ("broken", SyncStatus::Error),
            ("ssh-only", SyncStatus::Error)
        ]
    );
    assert!(results[1].detail.starts_with("clone failed: "), "{}", results[1].detail);
    assert!(results[1].detail.contains("/nowhere/either.git"), "{}", results[1].detail);
    assert!(results[2].detail.contains("/nowhere/ssh-only.git"), "{}", results[2].detail);
    assert!(!s.repo("broken").exists() && !s.repo("ssh-only").exists());
    // The token was not written into the clone.
    let config = std::fs::read_to_string(s.repo("site").join(".git/config")).unwrap();
    assert!(!config.contains("extraHeader") && !config.contains("Authorization"), "{config}");

    // The human output marks the failures.
    let (code, out, _) = s.devbox(&["sync"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert!(out.contains("  ✕ broken                         clone failed: "), "{out}");
}

#[test]
fn something_else_with_a_repositorys_name_is_never_replaced() {
    let mut s = Sandbox::new();
    let github =
        FakeGitHub::start("dev", vec![hosted(&s, "dev", "site"), hosted(&s, "dev", "Notes")]);
    signed_in(&mut s, &github);
    s.write(&s.code(), "site/draft.txt", "not a repository, but mine\n");
    s.write(&s.code(), "notes", "a file\n");
    assert_eq!(s.sync(), []);
    assert_eq!(
        std::fs::read_to_string(s.code().join("site/draft.txt")).unwrap(),
        "not a repository, but mine\n"
    );
    assert_eq!(std::fs::read_to_string(s.code().join("notes")).unwrap(), "a file\n");
}

#[test]
fn a_github_that_does_not_answer_is_reported_and_local_repositories_are_still_synced() {
    let mut s = Sandbox::new();
    let _ = s.repository("app");
    s.push_upstream("app", "main", "new.txt", "new\n");
    let github = FakeGitHub::start("dev", vec![hosted(&s, "dev", "site")]);

    // A token GitHub refuses.
    s.set_env("GITHUB_TOKEN", "FAKE-wrong");
    s.set_env("DEVBOX_GITHUB_API", &github.url);
    let results = s.sync();
    assert_eq!(summary(&results), [("github", SyncStatus::Error), ("app", SyncStatus::Updated)]);
    assert!(
        results[0].detail.starts_with("couldn't list your repositories: ")
            && results[0].detail.contains("401"),
        "{}",
        results[0].detail
    );
    assert!(!s.repo("site").exists());

    // Nothing listening at all.
    s.set_env("DEVBOX_GITHUB_API", "http://127.0.0.1:1");
    assert_eq!(summary(&s.sync()), [("github", SyncStatus::Error), ("app", SyncStatus::Current)]);
}

#[test]
fn a_manifest_that_is_not_valid_stops_cloning_but_not_syncing() {
    let mut s = Sandbox::new();
    let _ = s.repository("app");
    let github = FakeGitHub::start("dev", vec![hosted(&s, "dev", "site")]);
    signed_in(&mut s, &github);
    s.write(&s.home(), "dotfiles/devbox.toml", "version = 1\n[sync]\nexclude = \"old-*\"\n");
    let results = s.sync();
    assert_eq!(summary(&results), [("dotfiles", SyncStatus::Error), ("app", SyncStatus::Current)]);
    assert!(results[0].detail.contains("sync.exclude"), "{}", results[0].detail);
    assert!(results[0].detail.ends_with("; nothing was cloned"), "{}", results[0].detail);
    assert_eq!(github.targets(), [] as [&str; 0]);
}

#[test]
fn without_a_token_github_is_not_asked() {
    let mut s = Sandbox::new();
    let _ = s.repository("app");
    let github = FakeGitHub::start("dev", vec![hosted(&s, "dev", "site")]);
    s.set_env("DEVBOX_GITHUB_API", &github.url);
    assert_eq!(summary(&s.sync()), [("app", SyncStatus::Current)]);
    assert_eq!(github.targets(), [] as [&str; 0]);

    // `gh`, once signed in, is where the token comes from.
    s.gh_token(TOKEN);
    assert_eq!(summary(&s.sync()), [("site", SyncStatus::Cloned), ("app", SyncStatus::Current)]);
    assert_eq!(github.requests()[0].authorization, format!("Bearer {TOKEN}"));
}

#[test]
fn syncing_one_repository_touches_no_other_and_clones_nothing() {
    let mut s = Sandbox::new();
    for name in ["app", "docs"] {
        let _ = s.repository(name);
        s.push_upstream(name, "main", "new.txt", "new\n");
    }
    let github = FakeGitHub::start("dev", vec![hosted(&s, "dev", "site")]);
    signed_in(&mut s, &github);
    let docs_before = s.snapshot(&s.repo("docs"));

    let (code, out, err) = s.devbox(&["sync", "--repo", "app"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert_eq!(
        s.scrub(&out),
        "Syncing 1 repo in <sandbox>/home/code\n  ↓ app                            pulled 1 commit\n"
    );
    assert_eq!(s.snapshot(&s.repo("docs")), docs_before);
    assert_eq!(s.git(&s.repo("docs"), &["rev-list", "--count", "HEAD..origin/main"]), "0");
    assert_eq!(github.targets(), [] as [&str; 0]);

    let (code, out, err) = s.devbox(&["sync", "--repo", "nope"]);
    assert_eq!(code, ExitCode::from(2));
    assert_eq!(out, "");
    assert_eq!(s.scrub(&err), "devbox sync: no repository named nope in <sandbox>/home/code\n");
}
