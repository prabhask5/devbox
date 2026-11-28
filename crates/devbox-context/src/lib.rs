//! Developer context behind `devbox context`: a factual, current description of how the owner
//! works, written for AI agents to read.
//!
//! [`run`] gathers the facts (git, GitHub, the hub's event log, agent transcripts, shell history,
//! the editor), writes them as markdown into the owner's dotfiles, optionally has the owner's
//! own Claude Code summarize them, and commits the result. Unchanged facts give byte-identical
//! files, and nothing is written without passing through [`redact::redact`].

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::time::Duration;

use devbox_core::proto::DotfilesManifest;
use devbox_core::system::{Command, System};
use devbox_core::{config, time};

mod gather;
pub mod redact;
mod render;
mod text;

/// The least time between two AI summaries.
const SUMMARY_EVERY: i64 = 20 * 3600;
const SUMMARY_PROMPT: &str = include_str!("summary_prompt.md");
/// What the commits of the context are called; also how they are recognized.
const COMMIT_MESSAGE: &str = "context: auto-update";

/// Where a run works and what it may do.
#[derive(Clone, Debug)]
pub struct Options {
    /// The owner's home directory.
    pub home: PathBuf,
    /// The directory whose subdirectories are the repositories.
    pub code_dir: PathBuf,
    /// Where the run marker and the time of the last summary are kept.
    pub state_dir: PathBuf,
    /// The dotfiles checkout; the context is written into it when it exists.
    pub dotfiles: PathBuf,
    /// Commit the context directory to the dotfiles repository and push it.
    pub commit: bool,
    /// Let the owner's Claude Code write `summary.md`.
    pub ai: bool,
    /// Return the files instead of writing them.
    pub print: bool,
}

impl Options {
    /// The usual places under the home directory, overridden by what the environment says:
    /// `DEVBOX_CODE_DIR`, `DEVBOX_STATE_DIR` (else `XDG_STATE_HOME/devbox`) and
    /// `DEVBOX_DOTFILES`. Nothing is committed, the summary is allowed, files are written.
    #[must_use]
    pub fn from_env(system: &dyn System) -> Self {
        let set = |name: &str| system.env(name).filter(|value| !value.is_empty());
        let home = PathBuf::from(set("HOME").unwrap_or_else(|| "/".to_owned()));
        let state = set("XDG_STATE_HOME").map_or_else(|| home.join(".local/state"), PathBuf::from);
        Self {
            code_dir: set("DEVBOX_CODE_DIR").map_or_else(|| home.join("code"), PathBuf::from),
            state_dir: set("DEVBOX_STATE_DIR").map_or_else(|| state.join("devbox"), PathBuf::from),
            dotfiles: set("DEVBOX_DOTFILES").map_or_else(|| home.join("dotfiles"), PathBuf::from),
            commit: false,
            ai: true,
            print: false,
            home,
        }
    }
}

/// What became of the commit a run was asked for.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Committed {
    /// No commit was asked for, or the manifest turns commits off.
    NotAsked,
    /// The context is not inside a git repository.
    NoRepository,
    /// Nothing changed and nothing waits to be pushed.
    Unchanged,
    /// The context is committed and pushed.
    Pushed,
    /// The context is committed here and not pushed, for this reason; the next run tries again.
    Kept(String),
}

/// What a run did.
#[derive(Clone, Debug)]
pub struct Outcome {
    /// The context directory.
    pub dir: PathBuf,
    /// Whether any file stating facts changed.
    pub changed: bool,
    /// Whether a new AI summary was written.
    pub summarized: bool,
    /// What became of the commit.
    pub committed: Committed,
    /// The files by their path in the context directory, when printing was asked for.
    pub printed: Option<BTreeMap<String, String>>,
    /// Things the owner should know that did not stop the run.
    pub warnings: Vec<String>,
}

/// Why a run could not do its work.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A file or directory could not be written or removed.
    #[error("{path}: {source}")]
    Io {
        /// What was being accessed.
        path: PathBuf,
        /// What went wrong.
        source: std::io::Error,
    },
}

fn read(system: &dyn System, path: &Path) -> Option<String> {
    system.read(path).ok().map(|bytes| String::from_utf8_lossy(&bytes).into_owned())
}

fn git(system: &dyn System, dir: &Path, limit: u64, args: &[&str]) -> Option<String> {
    let command = Command::new("git", args.iter().copied())
        .in_dir(dir)
        .env("GIT_TERMINAL_PROMPT", "0")
        .env("GIT_SSH_COMMAND", "ssh -o BatchMode=yes -o ConnectTimeout=15")
        .env("LC_ALL", "C")
        .timeout(Duration::from_secs(limit));
    let output = system.run(&command).ok()?;
    output.success().then(|| output.text())
}

/// The owner's Claude Code, headless, on the facts. `None` when it is not installed, fails, or
/// says too little to be a summary.
fn summarize(system: &dyn System, files: &BTreeMap<String, String>) -> Option<String> {
    let mut input = format!("{}\n\n---\n\n{}", files.get("me.md")?, files.get("activity.md")?);
    for (_, project) in files.iter().filter(|(name, _)| name.starts_with("projects/")) {
        let part: String = project.chars().take(2500).collect();
        input = format!("{input}\n\n---\n\n{part}");
    }
    let input: String = input.chars().take(120_000).collect();
    let claude = Command::new("claude", ["-p", SUMMARY_PROMPT.trim(), "--output-format", "text"])
        .stdin(input)
        .timeout(Duration::from_secs(300));
    let output = system.run(&claude).ok()?;
    let summary = output.text();
    (output.success() && summary.chars().count() > 200).then_some(summary)
}

/// Commits the context directory, and only it, to the dotfiles repository and pushes — but only
/// when the commits waiting to be pushed are all the context's own: the owner's unpushed work
/// in their dotfiles is theirs to push.
fn commit(system: &dyn System, dotfiles: &Path, dir: &Path) -> Committed {
    let Ok(inside) = dir.strip_prefix(dotfiles) else { return Committed::NoRepository };
    if !system.exists(&dotfiles.join(".git")) {
        return Committed::NoRepository;
    }
    let path = inside.to_string_lossy();
    let run = |limit: u64, args: &[&str]| git(system, dotfiles, limit, args);
    let _ = run(20, &["add", "--all", "--", &path]);
    // `diff --quiet` fails when there is a difference.
    if run(20, &["diff", "--cached", "--quiet", "--", &path]).is_none()
        && run(20, &["commit", "--quiet", "-m", COMMIT_MESSAGE, "--", &path]).is_none()
    {
        return Committed::Kept("git could not commit".to_owned());
    }
    let Some(waiting) = run(20, &["log", "--format=%s", "@{u}..HEAD"]) else {
        return Committed::Kept("the branch has no upstream".to_owned());
    };
    if waiting.is_empty() {
        return Committed::Unchanged;
    }
    if waiting.lines().any(|subject| subject != COMMIT_MESSAGE) {
        return Committed::Kept("the dotfiles have other commits that are not pushed".to_owned());
    }
    let _ = run(60, &["fetch", "--quiet"]);
    let behind = run(20, &["rev-list", "--count", "HEAD..@{u}"]).is_some_and(|count| count != "0");
    if behind && run(60, &["rebase", "--quiet", "--autostash", "@{u}"]).is_none() {
        let _ = run(20, &["rebase", "--abort"]);
        return Committed::Kept("it could not be rebased onto what is upstream".to_owned());
    }
    match run(60, &["push", "--quiet"]) {
        Some(_) => Committed::Pushed,
        None => Committed::Kept("the push failed".to_owned()),
    }
}

/// Refreshes the developer context.
///
/// # Errors
///
/// When a context file cannot be written. Sources that cannot be read (GitHub offline, no hub,
/// no transcripts) are not errors: the context is written from what there is.
pub fn run(system: &dyn System, options: &Options) -> Result<Outcome, Error> {
    let io = |path: &Path| {
        let path = path.to_owned();
        move |source| Error::Io { path, source }
    };
    let mut warnings = Vec::new();
    let manifest_text = read(system, &options.dotfiles.join("devbox.toml"));
    let parse =
        |text: &str| config::dotfiles_manifest("devbox.toml", text).map(|parsed| parsed.value);
    let manifest: Option<DotfilesManifest> =
        match parse(manifest_text.as_deref().unwrap_or("version = 1\n")) {
            Ok(manifest) => Some(manifest),
            Err(error) => {
                let first = error.to_string().lines().next().unwrap_or_default().to_owned();
                warnings.push(format!("{first}; its settings are ignored"));
                parse("version = 1\n").ok()
            }
        };
    let Some(manifest) = manifest else {
        // The defaults always parse; without them there is nothing to go by.
        let path = options.dotfiles.join("devbox.toml");
        return Err(Error::Io { path, source: std::io::Error::other("no usable manifest") });
    };
    let dir = if system.read_dir(&options.dotfiles).is_ok() {
        options.dotfiles.join(manifest.context.dir.as_ref().map_or("context", |dir| dir.as_str()))
    } else {
        options.home.join(".local/share/devbox/context")
    };

    let facts = gather::facts(system, options, &manifest);
    let (rendered, rows) = render::files(&facts);
    let files: BTreeMap<String, String> =
        rendered.into_iter().map(|(name, content)| (name, redact::redact(&content))).collect();
    let mut outcome = Outcome {
        dir: dir.clone(),
        changed: false,
        summarized: false,
        committed: Committed::NotAsked,
        printed: None,
        warnings,
    };
    if options.print {
        outcome.printed = Some(files);
        return Ok(outcome);
    }

    for (name, content) in &files {
        let path = dir.join(name);
        if read(system, &path).as_deref() != Some(content) {
            system.write(&path, content.as_bytes(), 0o644).map_err(io(&path))?;
            outcome.changed = true;
        }
    }
    // A repository that is gone from the machine is gone from the context.
    for name in system.read_dir(&dir.join("projects")).unwrap_or_default() {
        if !files.contains_key(&format!("projects/{name}")) {
            let path = dir.join("projects").join(&name);
            system.remove_file(&path).map_err(io(&path))?;
            outcome.changed = true;
        }
    }

    let summary_path = dir.join("summary.md");
    let summarized_path = options.state_dir.join("context-summarized");
    let last_summary = read(system, &summarized_path).and_then(|at| at.trim().parse::<i64>().ok());
    let due = last_summary.is_none_or(|at| facts.now - at >= SUMMARY_EVERY);
    let wanted = options.ai && manifest.context.ai;
    if wanted
        && due
        && (outcome.changed || !system.exists(&summary_path))
        && let Some(summary) = summarize(system, &files)
    {
        let today = time::date(facts.now, facts.offset);
        let text = format!(
            "# Summary (AI-written)\n\n_Generated {today} by Claude from the files in this folder._\n\n{}\n",
            redact::redact(&summary)
        );
        system.write(&summary_path, text.as_bytes(), 0o644).map_err(io(&summary_path))?;
        system
            .write(&summarized_path, facts.now.to_string().as_bytes(), 0o644)
            .map_err(io(&summarized_path))?;
        outcome.summarized = true;
    }

    let index_path = dir.join("README.md");
    if outcome.changed || !system.exists(&index_path) {
        let index = redact::redact(&render::index(&time::date(facts.now, facts.offset), &rows));
        system.write(&index_path, index.as_bytes(), 0o644).map_err(io(&index_path))?;
    }
    let marker = options.state_dir.join("context-last-run");
    system
        .write(
            &marker,
            format!(
                "{}
",
                time::rfc3339(facts.now)
            )
            .as_bytes(),
            0o644,
        )
        .map_err(io(&marker))?;

    if options.commit && manifest.context.commit {
        outcome.committed = commit(system, &options.dotfiles, &dir);
    }
    Ok(outcome)
}
