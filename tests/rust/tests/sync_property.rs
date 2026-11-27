//! The guarantee of `devbox sync`, tried on random states: after a sync a checkout is either
//! updated with every local change present, or exactly as it was.

use std::collections::BTreeMap;

use devbox_sync::SyncStatus;
use devbox_tests::sandbox::Sandbox;
use proptest::prelude::*;

/// Paths both sides may change. Every file is one line, so a file changed on both sides can
/// never be merged: the sync has to notice and step back.
const SHARED: [&str; 6] = ["a.txt", "b.txt", "dir/c.txt", "dir/deep/d.txt", "new-1.txt", "bin.dat"];
/// Paths only the local side renames, so the sync never has to follow a rename upstream changed.
const RENAMED: [&str; 2] = ["r-1.txt", "r-2.txt"];
const CONTENTS: [&[u8]; 4] = [b"alpha\n", b"beta\n", b"\x00\x01\xff binary \x00\n", b""];

#[derive(Clone, Debug)]
enum Local {
    /// Writes a file: a change to a tracked one, or a new untracked one.
    Write(usize, usize),
    Remove(usize),
    /// Stages whatever the path's state is.
    Stage(usize),
    /// Renames a tracked file with `git mv`.
    Rename(usize),
    /// Makes a file executable.
    Executable(usize),
    /// Commits what is staged.
    Commit,
}

/// One upstream commit: a path written with a content, or deleted.
type Upstream = (usize, Option<usize>);

fn local() -> impl Strategy<Value = Local> {
    prop_oneof![
        4 => (0..SHARED.len(), 0..CONTENTS.len()).prop_map(|(path, content)| Local::Write(path, content)),
        1 => (0..SHARED.len()).prop_map(Local::Remove),
        3 => (0..SHARED.len()).prop_map(Local::Stage),
        1 => (0..RENAMED.len()).prop_map(Local::Rename),
        1 => (0..SHARED.len()).prop_map(Local::Executable),
        2 => Just(Local::Commit),
    ]
}

fn upstream() -> impl Strategy<Value = Upstream> {
    (0..SHARED.len(), prop::option::weighted(0.8, 0..CONTENTS.len()))
}

type Files = BTreeMap<String, (bool, Vec<u8>)>;

/// The files of a commit, as the working tree of a clean checkout of it would have them.
fn tree(s: &Sandbox, dir: &std::path::Path, commit: &str) -> Files {
    let mut files = Files::new();
    for line in s.git(dir, &["ls-tree", "-r", commit]).lines() {
        let (meta, path) = line.split_once('\t').unwrap();
        let fields: Vec<&str> = meta.split(' ').collect();
        let output = std::process::Command::new("git")
            .args(["cat-file", "blob", fields[2]])
            .current_dir(dir)
            .output()
            .unwrap();
        files.insert(path.to_owned(), (fields[0] == "100755", output.stdout));
    }
    files
}

/// One aspect of one file after an update, given what it was when the sides parted, here before
/// the sync, and upstream: changed here, it is exactly that; untouched here, it is what upstream
/// has. Where both sides changed it and git could still merge, what was here wins in the working
/// tree and upstream's is in the history — except that `nothing_here` (a new file that is empty,
/// which to git's merge is no change at all) takes what upstream put there.
fn kept_or_updated<T: PartialEq + std::fmt::Debug>(
    path: &str,
    aspect: &str,
    [was, mine, theirs, after]: &[T; 4],
    nothing_here: bool,
) -> Result<(), TestCaseError> {
    if mine == was || (nothing_here && after == theirs) {
        prop_assert_eq!(after, theirs, "{} of {}", aspect, path);
    } else {
        prop_assert_eq!(after, mine, "{} of {}", aspect, path);
    }
    Ok(())
}

fn check(local_ops: &[Local], upstream_ops: &[Upstream]) -> Result<(), TestCaseError> {
    let s = Sandbox::new();
    let dir = s.repository("app");
    // More files than the first commit has, on both sides, before they part ways.
    for path in SHARED.iter().take(4).chain(&RENAMED) {
        s.write(&dir, path, format!("{path} at first\n"));
    }
    s.git(&dir, &["add", "."]);
    s.git(&dir, &["commit", "--quiet", "-m", "files"]);
    s.git(&dir, &["push", "--quiet", "origin", "main"]);
    let seed = s.seed("app");
    s.git(&seed, &["pull", "--quiet"]);
    let base = s.git(&dir, &["rev-parse", "HEAD"]);

    for op in local_ops {
        match op {
            Local::Write(path, content) => s.write(&dir, SHARED[*path], CONTENTS[*content]),
            Local::Remove(path) => {
                let _ = std::fs::remove_file(dir.join(SHARED[*path]));
            }
            Local::Stage(path) => {
                let _ = s.try_git(&dir, &["add", "--all", "--", SHARED[*path]]);
            }
            Local::Rename(path) => {
                let _ = s.try_git(&dir, &["mv", RENAMED[*path], &format!("moved-{path}.txt")]);
            }
            Local::Executable(path) => {
                use std::os::unix::fs::PermissionsExt;
                let _ = std::fs::set_permissions(
                    dir.join(SHARED[*path]),
                    std::fs::Permissions::from_mode(0o755),
                );
            }
            Local::Commit => {
                let _ = s.try_git(&dir, &["commit", "--quiet", "-m", "local"]);
            }
        }
    }
    let mut pushed = 0;
    for (path, content) in upstream_ops {
        match content {
            Some(content) => s.write(&seed, SHARED[*path], CONTENTS[*content]),
            None => {
                let _ = std::fs::remove_file(seed.join(SHARED[*path]));
            }
        }
        s.git(&seed, &["add", "--all"]);
        if s.try_git(&seed, &["commit", "--quiet", "-m", "upstream"]).0 {
            pushed += 1;
        }
    }
    s.git(&seed, &["push", "--quiet", "origin", "main"]);

    let before = s.snapshot(&dir);
    let unpushed: u32 =
        s.git(&dir, &["rev-list", "--count", &format!("{base}..HEAD")]).parse().unwrap();
    let result = s.sync_one("app");
    let after = s.snapshot(&dir);

    match result.status {
        SyncStatus::Conflict => prop_assert_eq!(&after, &before, "{}", result.detail),
        SyncStatus::Current => {
            prop_assert_eq!(pushed, 0, "{}", result.detail);
            prop_assert_eq!(&after, &before);
        }
        SyncStatus::Updated => {
            prop_assert!(
                s.try_git(&dir, &["merge-base", "--is-ancestor", "origin/main", "HEAD"]).0
            );
            // A local commit that upstream already has, change for change, is dropped by the
            // rebase; none appears.
            let ahead: u32 =
                s.git(&dir, &["rev-list", "--count", "origin/main..HEAD"]).parse().unwrap();
            prop_assert!(ahead <= unpushed, "{} unpushed commits became {}", unpushed, ahead);
            prop_assert_eq!(&after.stashes, &before.stashes);
            let (ours, theirs) = (tree(&s, &dir, &base), tree(&s, &dir, "origin/main"));
            let paths = before.files.keys().chain(ours.keys()).chain(theirs.keys());
            for path in paths.collect::<std::collections::BTreeSet<_>>() {
                // A file's content and whether it is executable are changed, and merged, apart.
                let content = |files: &Files| files.get(path).map(|(_, content)| content.clone());
                let executable = |files: &Files| files.get(path).map(|(executable, _)| *executable);
                let states = [&ours, &before.files, &theirs, &after.files];
                let new_and_empty = !ours.contains_key(path)
                    && before.files.get(path).is_some_and(|(_, content)| content.is_empty());
                kept_or_updated(path, "content", &states.map(content), new_and_empty)?;
                kept_or_updated(path, "mode", &states.map(executable), new_and_empty)?;
            }
        }
        other => prop_assert!(false, "{other}: {}", result.detail),
    }
    Ok(())
}

/// Every case is a few dozen runs of git, so a usual run tries forty; `PROPTEST_CASES` asks for
/// more.
fn cases() -> u32 {
    std::env::var("PROPTEST_CASES").ok().and_then(|cases| cases.parse().ok()).unwrap_or(40)
}

proptest! {
    #![proptest_config(ProptestConfig { cases: cases(), failure_persistence: None, ..ProptestConfig::default() })]

    #[test]
    fn a_sync_never_loses_work(
        local_ops in prop::collection::vec(local(), 0..9),
        upstream_ops in prop::collection::vec(upstream(), 0..4),
    ) {
        check(&local_ops, &upstream_ops)?;
    }
}
