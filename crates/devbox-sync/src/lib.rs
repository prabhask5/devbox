//! Repository sync behind `devbox sync`: clones and updates every repository without ever
//! destroying local work.
//!
//! After a sync each repository is either updated with every local change kept (commits, staged
//! and unstaged changes, untracked files) or exactly as it was, and reported. [`sync`] is the
//! whole of it; what it does to the machine goes through a [`System`].

use std::path::PathBuf;
use std::sync::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::UNIX_EPOCH;

use devbox_core::config;
use devbox_core::proto::DotfilesManifestSync;
pub use devbox_core::proto::SyncStatus;
use devbox_core::system::System;
use devbox_core::time::rfc3339;

mod git;
mod github;
mod repo;
mod text;

/// Repositories fetched at the same time.
const FETCHES: usize = 6;

/// Where a sync works.
#[derive(Clone, Debug)]
pub struct Options {
    /// The owner's home directory: `dotfiles/devbox.toml` and `.ssh/known_hosts` are read there.
    pub home: PathBuf,
    /// The directory whose subdirectories are the repositories.
    pub code_dir: PathBuf,
    /// Where `sync.log` is kept.
    pub state_dir: PathBuf,
    /// Sync only the repository with this directory name, and clone nothing.
    pub only: Option<String>,
    /// The GitHub API to list repositories from.
    pub github_api: String,
}

impl Options {
    /// The usual places under `home`, overridden by what the environment says:
    /// `DEVBOX_CODE_DIR`, `DEVBOX_STATE_DIR` (else `XDG_STATE_HOME/devbox`) and
    /// `DEVBOX_GITHUB_API`.
    #[must_use]
    pub fn from_env(system: &dyn System) -> Self {
        let set = |name: &str| system.env(name).filter(|value| !value.is_empty());
        let home = PathBuf::from(set("HOME").unwrap_or_else(|| "/".to_owned()));
        let state = set("XDG_STATE_HOME").map_or_else(|| home.join(".local/state"), PathBuf::from);
        Self {
            code_dir: set("DEVBOX_CODE_DIR").map_or_else(|| home.join("code"), PathBuf::from),
            state_dir: set("DEVBOX_STATE_DIR").map_or_else(|| state.join("devbox"), PathBuf::from),
            only: None,
            github_api: devbox_core::github::api(system),
            home,
        }
    }
}

/// What became of one repository.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RepoResult {
    /// Its directory name.
    pub name: String,
    /// The outcome.
    pub status: SyncStatus,
    /// One line for the owner.
    pub detail: String,
}

/// A step of a sync, reported as it happens.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Event {
    /// The sync starts.
    Start {
        /// Repositories on this machine.
        local: usize,
        /// Repositories that will be cloned.
        new: usize,
    },
    /// Work on a repository begins; `fetching` while all of them are fetched.
    Begin(String),
    /// A repository is done.
    Repo(RepoResult),
    /// The sync is over.
    Done,
}

/// Why a sync could not run at all.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A directory or the log could not be created or read.
    #[error("{path}: {source}")]
    Io {
        /// What was being accessed.
        path: PathBuf,
        /// What went wrong.
        source: std::io::Error,
    },
    /// `only` names a repository that is not in the code directory.
    #[error("no repository named {name} in {code_dir}")]
    NoSuchRepo {
        /// The name asked for.
        name: String,
        /// Where it was looked for.
        code_dir: PathBuf,
    },
}

/// What a sync found on GitHub: repositories to clone and the token to clone them with, or why
/// it could not look.
struct Missing {
    remotes: Vec<github::Remote>,
    token: String,
    problem: Option<RepoResult>,
}

/// The `[sync]` rules of the dotfiles manifest; the defaults without one.
fn rules(system: &dyn System, options: &Options) -> Result<DotfilesManifestSync, String> {
    let path = options.home.join("dotfiles/devbox.toml");
    let text = match system.read(&path) {
        Ok(bytes) => String::from_utf8_lossy(&bytes).into_owned(),
        Err(_) => "version = 1\n".to_owned(),
    };
    match config::dotfiles_manifest("devbox.toml", &text) {
        Ok(manifest) => Ok(manifest.value.sync),
        Err(error) => Err(text::one_line(&error.to_string(), 200)),
    }
}

/// Looks on GitHub for repositories that are not in the code directory. Without a token there
/// is nothing to look with, which is not a problem; a manifest that cannot be read or a GitHub
/// that does not answer is one, and nothing is cloned then.
fn missing(system: &dyn System, options: &Options, have: &[String]) -> Missing {
    let mut found = Missing { remotes: Vec::new(), token: String::new(), problem: None };
    let Some(token) = devbox_core::github::token(system) else { return found };
    let problem = |name: &str, detail: String| {
        Some(RepoResult { name: name.to_owned(), status: SyncStatus::Error, detail })
    };
    let rules = match rules(system, options) {
        Ok(rules) => rules,
        Err(why) => {
            found.problem = problem("dotfiles", format!("{why}; nothing was cloned"));
            return found;
        }
    };
    match github::list(system, &options.github_api, &token, &rules) {
        Ok(listed) => {
            found.remotes = listed
                .into_iter()
                .filter(|remote| {
                    // Anything already there with that name stays as it is, a repository or not.
                    !have.iter().any(|name| name.eq_ignore_ascii_case(&remote.name))
                })
                .collect();
        }
        Err(why) => {
            found.problem = problem("github", format!("couldn't list your repositories: {why}"));
        }
    }
    found.token = token;
    found
}

/// Syncs the code directory: clones what is missing, fetches everything, and updates every
/// repository or leaves it exactly as it was. `report` is told each step as it happens; the
/// results are also appended to `sync.log` in the state directory.
///
/// # Errors
///
/// When the directories or the log cannot be used, or `only` names no repository. A repository
/// that could not be synced is not an error: it is in the results.
pub fn sync(
    system: &dyn System,
    options: &Options,
    report: &mut dyn FnMut(&Event),
) -> Result<Vec<RepoResult>, Error> {
    let io = |path: &PathBuf| {
        let path = path.clone();
        move |source| Error::Io { path, source }
    };
    system.create_dir_all(&options.code_dir, 0o755).map_err(io(&options.code_dir))?;
    system.create_dir_all(&options.state_dir, 0o755).map_err(io(&options.state_dir))?;
    let log_path = options.state_dir.join("sync.log");
    let now = || {
        let since = system.now().duration_since(UNIX_EPOCH).map_or(0, |since| since.as_secs());
        i64::try_from(since).unwrap_or(0)
    };
    let log = |line: &str| {
        let line = format!("{} {line}\n", rfc3339(now()));
        system.append(&log_path, line.as_bytes(), 0o644).map_err(io(&log_path))
    };

    let have = system.read_dir(&options.code_dir).map_err(io(&options.code_dir))?;
    let mut repos: Vec<String> = have
        .iter()
        .filter(|name| !name.starts_with('.'))
        .filter(|name| devbox_core::system::is_dir(system, &options.code_dir.join(name)))
        .filter(|name| system.exists(&options.code_dir.join(name).join(".git")))
        .cloned()
        .collect();
    let found = if let Some(only) = &options.only {
        if !repos.contains(only) {
            return Err(Error::NoSuchRepo {
                name: only.clone(),
                code_dir: options.code_dir.clone(),
            });
        }
        repos.retain(|name| name == only);
        Missing { remotes: Vec::new(), token: String::new(), problem: None }
    } else {
        missing(system, options, &have)
    };

    let mut results = Vec::new();
    report(&Event::Start { local: repos.len(), new: found.remotes.len() });
    log(&format!("--- sync start ({} local, {} new)", repos.len(), found.remotes.len()))?;
    let mut finish = |result: RepoResult, report: &mut dyn FnMut(&Event)| {
        let logged = log(&format!("{} {} {}", result.name, result.status, result.detail));
        report(&Event::Repo(result.clone()));
        results.push(result);
        logged
    };
    if let Some(problem) = found.problem {
        finish(problem, report)?;
    }

    // Fetching is the slow part and independent per repository, so it runs in parallel; the
    // updates after it are local and quick, and run one by one.
    report(&Event::Begin("fetching".to_owned()));
    let fetch_errors: Mutex<Vec<Option<String>>> = Mutex::new(vec![None; repos.len()]);
    let next = AtomicUsize::new(0);
    std::thread::scope(|scope| {
        for _ in 0..FETCHES.min(repos.len()) {
            scope.spawn(|| {
                loop {
                    let index = next.fetch_add(1, Ordering::Relaxed);
                    let Some(name) = repos.get(index) else { break };
                    let error = repo::fetch(system, &options.code_dir.join(name));
                    if let Ok(mut errors) = fetch_errors.lock() {
                        errors[index] = error;
                    }
                }
            });
        }
    });
    let fetch_errors = fetch_errors.into_inner().unwrap_or_else(std::sync::PoisonError::into_inner);

    for remote in &found.remotes {
        report(&Event::Begin(remote.name.clone()));
        let dir = options.code_dir.join(&remote.name);
        let (status, detail) =
            match github::clone(system, &options.home, remote, &found.token, &dir) {
                Ok(()) => (SyncStatus::Cloned, "new on GitHub".to_owned()),
                Err(why) => (SyncStatus::Error, format!("clone failed: {why}")),
            };
        finish(RepoResult { name: remote.name.clone(), status, detail }, report)?;
    }
    for (name, fetch_error) in repos.iter().zip(&fetch_errors) {
        report(&Event::Begin(name.clone()));
        let dir = options.code_dir.join(name);
        finish(repo::update(system, &dir, name, fetch_error.as_deref(), now()), report)?;
    }
    report(&Event::Done);
    log("--- sync done")?;
    Ok(results)
}
