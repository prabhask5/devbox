//! What `devbox sync` prints and logs, against golden files in `tests/fixtures/sync/`
//! (`UPDATE_GOLDEN=1 cargo test -p devbox-tests --test sync_output` rewrites them). The hub
//! reads the `--json` lines and the boot screen and the app show them, so they are a contract.

use std::process::ExitCode;

use devbox_core::proto::{SyncProgress, versions};
use devbox_tests::github::{FakeGitHub, TOKEN, repo};
use devbox_tests::sandbox::Sandbox;

/// One repository per outcome that does not depend on the wording of git or curl.
fn scenario() -> (Sandbox, FakeGitHub) {
    let mut s = Sandbox::new();
    for name in ["current", "detached", "dirty", "diverged", "unpushed", "updated"] {
        let _ = s.repository(name);
    }
    for n in 1..=3 {
        s.push_upstream("updated", "main", &format!("new-{n}.txt"), "new\n");
    }
    s.push_upstream("dirty", "main", "new.txt", "new\n");
    s.write(&s.repo("dirty"), "README.md", "# dirty\nedited\n");
    s.push_upstream("diverged", "main", "src/lib.rs", "one\nTWO upstream\nthree\nfour\nfive\n");
    s.write(&s.repo("diverged"), "src/lib.rs", "one\nTWO mine\nthree\nfour\nfive\n");
    s.git(&s.repo("diverged"), &["commit", "--quiet", "-am", "mine"]);
    s.write(&s.repo("unpushed"), "mine.txt", "mine\n");
    s.git(&s.repo("unpushed"), &["add", "."]);
    s.git(&s.repo("unpushed"), &["commit", "--quiet", "-m", "mine"]);
    s.git(&s.repo("detached"), &["checkout", "--quiet", "--detach"]);

    s.upstream("brand-new");
    let url = s.remote("brand-new").to_string_lossy().into_owned();
    let github = FakeGitHub::start("dev", vec![repo("dev", "brand-new", &url, &url)]);
    s.set_env("GITHUB_TOKEN", TOKEN);
    s.set_env("DEVBOX_GITHUB_API", &github.url);
    (s, github)
}

fn golden(name: &str, actual: &str) {
    let path = format!("tests/fixtures/sync/{name}");
    if std::env::var_os("UPDATE_GOLDEN").is_some() {
        std::fs::write(devbox_tests::repo(&path), actual).unwrap();
    }
    assert_eq!(actual, devbox_tests::read(&path), "{path}");
}

#[test]
fn json_output_and_the_log() {
    let (s, _github) = scenario();
    let (code, out, err) = s.devbox(&["sync", "--json"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("progress.ndjson", &out);
    // Every line is a sync-progress message of the current version.
    for line in out.lines() {
        let progress: SyncProgress = serde_json::from_str(line).unwrap();
        assert_eq!(progress.v, versions::SYNC_PROGRESS_VERSION, "{line}");
    }
    let log = std::fs::read_to_string(s.home().join(".local/state/devbox/sync.log")).unwrap();
    golden("sync.log", &log);

    // A second run adds to the log; it does not replace it.
    let _ = s.devbox(&["sync", "--json"]);
    let longer = std::fs::read_to_string(s.home().join(".local/state/devbox/sync.log")).unwrap();
    assert!(longer.starts_with(&log) && longer.len() > log.len());
}

#[test]
fn human_output() {
    let (s, _github) = scenario();
    let (code, out, err) = s.devbox(&["sync"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("human.txt", &s.scrub(&out));
}

#[test]
fn the_places_come_from_the_environment() {
    let mut s = Sandbox::new();
    let elsewhere = s.home().join("elsewhere");
    s.set_env("DEVBOX_CODE_DIR", &elsewhere.join("src").to_string_lossy());
    s.set_env("XDG_STATE_HOME", &elsewhere.join("state").to_string_lossy());
    let (code, out, _) = s.devbox(&["sync"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert_eq!(s.scrub(&out), "Syncing 0 repos in <sandbox>/home/elsewhere/src\n");
    assert!(elsewhere.join("src").is_dir(), "the code directory is created");
    assert!(elsewhere.join("state/devbox/sync.log").is_file());

    s.set_env("DEVBOX_STATE_DIR", &elsewhere.join("explicit").to_string_lossy());
    let _ = s.devbox(&["sync"]);
    assert!(elsewhere.join("explicit/sync.log").is_file());
}

#[test]
fn a_sync_that_cannot_run_at_all_exits_with_2() {
    let mut s = Sandbox::new();
    s.write(&s.home(), "blocked", "a file where the code directory should be\n");
    s.set_env("DEVBOX_CODE_DIR", &s.home().join("blocked").to_string_lossy());
    let (code, out, err) = s.devbox(&["sync", "--json"]);
    assert_eq!(code, ExitCode::from(2));
    assert_eq!(out, "");
    assert!(s.scrub(&err).starts_with("devbox sync: <sandbox>/home/blocked: "), "{err}");
}
