//! Gathering the facts: the repositories and what git knows about them, GitHub, the hub's event
//! log, agent transcripts, shell history, the machine and the editor. Everything is read; nothing
//! here changes anything.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use devbox_core::system::{Command, System};
use devbox_core::time;
use serde_json::Value;

use crate::Options;
use crate::text::clip;

/// Seconds in a day.
pub(crate) const DAY: i64 = 86400;
/// How far back commits and prompts are looked at.
const HISTORY_DAYS: i64 = 120;
/// Repositories looked at at the same time.
const WORKERS: usize = 6;

/// A commit of the owner's.
pub(crate) struct Commit {
    pub repo: String,
    pub at: i64,
    pub subject: String,
    pub insertions: u64,
    pub deletions: u64,
}

/// Something the owner asked an agent.
pub(crate) struct Prompt {
    pub at: i64,
    pub agent: String,
    pub repo: Option<String>,
    pub text: String,
}

/// An open pull request or issue.
pub(crate) struct Ticket {
    pub number: u64,
    pub title: String,
    pub labels: Vec<String>,
    pub draft: bool,
    pub updated: String,
    pub pull_request: bool,
}

/// What GitHub says about a repository.
#[derive(Default)]
pub(crate) struct Remote {
    pub description: String,
    pub url: String,
    pub private: bool,
    pub homepage: String,
    pub topics: Vec<String>,
    pub tickets: Vec<Ticket>,
}

/// A local branch.
pub(crate) struct Branch {
    pub name: String,
    pub date: String,
    pub track: String,
}

/// A repository of the code directory.
#[derive(Default)]
pub(crate) struct Repo {
    pub name: String,
    pub branch: Option<String>,
    pub upstream: String,
    pub ahead: u64,
    pub behind: u64,
    pub dirty: Vec<String>,
    pub stashes: Vec<String>,
    pub branches: Vec<Branch>,
    /// Date, author and subject of the latest commits of the checked-out branch.
    pub recent: Vec<(String, String, String)>,
    pub last_commit: i64,
    pub files: Vec<String>,
    pub todos: Vec<String>,
    pub origin: String,
    pub stack: Vec<String>,
    pub readme: String,
    pub remote: Remote,
}

/// A repo sync that left something for the owner to do.
pub(crate) struct SyncProblem {
    pub at: i64,
    pub line: String,
}

/// The machine.
#[derive(Default)]
pub(crate) struct Machine {
    pub os: String,
    pub cpus: usize,
    pub memory_gib: u64,
    pub tools: Vec<(String, String)>,
    pub code_host: String,
    pub agents_host: String,
}

/// Everything the context files are written from.
pub(crate) struct Facts {
    /// Now, in seconds since the Unix epoch.
    pub now: i64,
    /// Seconds the owner's clock is east of UTC, and what to call that.
    pub offset: i64,
    pub zone: String,
    pub name: String,
    pub email: String,
    pub git_config: BTreeMap<String, String>,
    pub repos: Vec<Repo>,
    pub commits: Vec<Commit>,
    pub prompts: Vec<Prompt>,
    pub needs_input: usize,
    pub turn_seconds: Vec<u64>,
    pub sync_problems: Vec<SyncProblem>,
    /// Command heads of the shell history with how often each was run, most used first.
    pub commands: Vec<(String, usize)>,
    pub machine: Machine,
    pub settings: Value,
    pub extensions: Vec<String>,
}

fn text(system: &dyn System, path: &Path, limit: usize) -> String {
    let bytes = system.read(path).unwrap_or_default();
    String::from_utf8_lossy(&bytes[..bytes.len().min(limit)]).into_owned()
}

fn git(system: &dyn System, dir: &Path, args: &[&str]) -> String {
    let command = Command::new("git", args.iter().copied())
        .in_dir(dir)
        .env("GIT_TERMINAL_PROMPT", "0")
        .env("LC_ALL", "C")
        .timeout(Duration::from_secs(20));
    match system.run(&command) {
        Ok(output) if output.success() => {
            String::from_utf8_lossy(&output.stdout).trim_end().to_owned()
        }
        _ => String::new(),
    }
}

/// Runs `work` on every item, a few at a time, and returns the results in the items' order.
fn in_parallel<T: Sync, R: Send>(items: &[T], work: impl Fn(&T) -> R + Sync) -> Vec<R> {
    let results: Mutex<Vec<Option<R>>> = Mutex::new(items.iter().map(|_| None).collect());
    let next = AtomicUsize::new(0);
    std::thread::scope(|scope| {
        for _ in 0..WORKERS.min(items.len()) {
            scope.spawn(|| {
                loop {
                    let index = next.fetch_add(1, Ordering::Relaxed);
                    let Some(item) = items.get(index) else { break };
                    let result = work(item);
                    if let Ok(mut results) = results.lock() {
                        results[index] = Some(result);
                    }
                }
            });
        }
    });
    let results = results.into_inner().unwrap_or_else(std::sync::PoisonError::into_inner);
    results.into_iter().flatten().collect()
}

// ---------------------------------------------------------------- repositories

const FRAMEWORKS: [&str; 24] = [
    "svelte",
    "@sveltejs/kit",
    "next",
    "react",
    "preact",
    "vue",
    "nuxt",
    "astro",
    "solid-js",
    "express",
    "fastify",
    "hono",
    "vite",
    "tailwindcss",
    "prisma",
    "drizzle-orm",
    "@supabase/supabase-js",
    "vitest",
    "jest",
    "playwright",
    "@playwright/test",
    "typescript",
    "electron",
    "three",
];

fn keys(value: &Value) -> Vec<String> {
    value.as_object().map_or_else(Vec::new, |map| map.keys().cloned().collect())
}

/// Whether a file name ends in `.extension`, whatever its case.
fn has_extension(name: &str, extension: &str) -> bool {
    Path::new(name).extension().is_some_and(|found| found.eq_ignore_ascii_case(extension))
}

/// What `package.json` says: the package manager, known frameworks, scripts and dependencies.
fn node(system: &dyn System, dir: &Path, facts: &mut Vec<String>) {
    let has = |file: &str| system.exists(&dir.join(file));
    let Ok(package) =
        serde_json::from_str::<Value>(&text(system, &dir.join("package.json"), 1 << 20))
    else {
        return;
    };
    let dependencies = keys(&package["dependencies"]);
    let all: BTreeSet<String> =
        dependencies.iter().cloned().chain(keys(&package["devDependencies"])).collect();
    let frameworks: Vec<&str> =
        FRAMEWORKS.iter().copied().filter(|name| all.contains(*name)).collect();
    let manager = if has("pnpm-lock.yaml") {
        "pnpm"
    } else if has("bun.lockb") || has("bun.lock") {
        "bun"
    } else if has("yarn.lock") {
        "yarn"
    } else {
        "npm"
    };
    let mut line = format!("**Node/JS** ({manager})");
    if !frameworks.is_empty() {
        line = format!("{line}: {}", frameworks.join(", "));
    }
    facts.push(line);
    if let Some(scripts) = package["scripts"].as_object().filter(|scripts| !scripts.is_empty()) {
        let listed: Vec<String> = scripts
            .iter()
            .take(14)
            .map(|(name, script)| {
                format!("`{name}` ({})", clip(script.as_str().unwrap_or_default(), 60))
            })
            .collect();
        facts.push(format!("Scripts: {}", listed.join(", ")));
    }
    if !dependencies.is_empty() {
        let more = dependencies.len().saturating_sub(30);
        let shown: Vec<&str> = dependencies.iter().take(30).map(String::as_str).collect();
        let rest = if more > 0 { format!(" +{more}") } else { String::new() };
        facts.push(format!("Dependencies: {}{rest}", shown.join(", ")));
    }
}

/// What `Cargo.toml` says: whether it is a workspace, and the dependencies.
fn rust(system: &dyn System, dir: &Path, facts: &mut Vec<String>) {
    let cargo = text(system, &dir.join("Cargo.toml"), 100_000);
    let Ok(manifest) = cargo.parse::<toml::Table>() else { return };
    if cargo.is_empty() {
        return;
    }
    let table =
        |path: &[&str]| path.iter().try_fold(&manifest, |table, key| table.get(*key)?.as_table());
    let mut dependencies: BTreeSet<String> = BTreeSet::new();
    for path in [&["dependencies"][..], &["workspace", "dependencies"], &["dev-dependencies"]] {
        dependencies.extend(table(path).into_iter().flat_map(|table| table.keys().cloned()));
    }
    let mut line = "**Rust**".to_owned();
    if manifest.contains_key("workspace") {
        line.push_str(" (workspace)");
    }
    if !dependencies.is_empty() {
        let shown: Vec<&str> = dependencies.iter().take(30).map(String::as_str).collect();
        line = format!("{line}: {}", shown.join(", "));
    }
    facts.push(line);
}

/// What a repository is built with, read from the files that say so.
fn stack(system: &dyn System, dir: &Path) -> Vec<String> {
    let has = |file: &str| system.exists(&dir.join(file));
    let mut facts = Vec::new();
    node(system, dir, &mut facts);
    rust(system, dir, &mut facts);
    if has("pyproject.toml") || has("requirements.txt") {
        let project = if has("pyproject.toml") { " (pyproject)" } else { "" };
        let tool = if has("uv.lock") {
            ", uv"
        } else if has("poetry.lock") {
            ", poetry"
        } else {
            ""
        };
        facts.push(format!("**Python**{project}{tool}"));
    }
    if has("go.mod") {
        let module = text(system, &dir.join("go.mod"), 2000);
        facts.push(format!("**Go**: {}", clip(module.lines().next().unwrap_or_default(), 80)));
    }
    if has("Makefile") {
        let makefile = text(system, &dir.join("Makefile"), 100_000);
        let targets: Vec<&str> = makefile
            .lines()
            .filter_map(|line| {
                let (target, rest) = line.split_once(':')?;
                let named = !target.is_empty()
                    && !target.starts_with('.')
                    && target.chars().all(|c| c.is_ascii_alphanumeric() || "_.-".contains(c));
                (named && !rest.starts_with('=')).then_some(target)
            })
            .take(15)
            .collect();
        facts.push(format!("Makefile targets: {}", targets.join(", ")));
    }
    if has("CMakeLists.txt") {
        facts.push("**C/C++** (CMake)".to_owned());
    }
    let mut containers = Vec::new();
    if has("Dockerfile") {
        containers.push("Dockerfile");
    }
    if has("docker-compose.yml") || has("compose.yml") || has("docker-compose.yaml") {
        containers.push("compose");
    }
    if !containers.is_empty() {
        facts.push(format!("Containers: {}", containers.join(", ")));
    }
    let workflows: Vec<String> = system
        .read_dir(&dir.join(".github/workflows"))
        .unwrap_or_default()
        .into_iter()
        .map(|entry| entry.name)
        .filter(|name| has_extension(name, "yml") || has_extension(name, "yaml"))
        .collect();
    if !workflows.is_empty() {
        facts.push(format!("CI: GitHub Actions ({})", workflows.join(", ")));
    }
    let instructions: Vec<&str> =
        ["CLAUDE.md", "AGENTS.md", ".claude/settings.json", ".cursorrules"]
            .into_iter()
            .filter(|file| has(file))
            .collect();
    if !instructions.is_empty() {
        facts.push(format!("Agent instructions in repo: {}", instructions.join(", ")));
    }
    facts
}

/// The first paragraph of the README that is prose: not a heading, badge, image, list or code.
fn readme(system: &dyn System, dir: &Path) -> String {
    let Some(file) = ["README.md", "readme.md", "README"]
        .into_iter()
        .map(|name| dir.join(name))
        .find(|path| system.exists(path))
    else {
        return String::new();
    };
    let content = text(system, &file, 30_000).replace("\r\n", "\n");
    let prose = content.split("\n\n").map(str::trim).find(|paragraph| {
        !paragraph.is_empty()
            && !["#", "![", "[![", "<", "```", "-", "*", "|"]
                .iter()
                .any(|start| paragraph.starts_with(start))
    });
    clip(prose.unwrap_or_default(), 600)
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

/// The owner's commits of the last months in one repository, from its branches and remotes only:
/// stashes and devbox's own safety refs are not history.
fn commits(
    system: &dyn System,
    dir: &Path,
    name: &str,
    author: &str,
    since: i64,
) -> Vec<(String, Commit)> {
    let log = git(
        system,
        dir,
        &[
            "log",
            "--branches",
            "--remotes",
            "--no-merges",
            &format!("--since={}", time::rfc3339(since)),
            &format!("--author={author}"),
            "--pretty=format:@@%H|%at|%s",
            "--shortstat",
        ],
    );
    let mut found: Vec<(String, Commit)> = Vec::new();
    for line in log.lines() {
        if let Some(header) = line.strip_prefix("@@") {
            let mut fields = header.splitn(3, '|');
            let (hash, at, subject) = (fields.next(), fields.next(), fields.next());
            found.push((
                hash.unwrap_or_default().to_owned(),
                Commit {
                    repo: name.to_owned(),
                    at: at.and_then(|at| at.parse().ok()).unwrap_or(0),
                    subject: subject.unwrap_or_default().to_owned(),
                    insertions: 0,
                    deletions: 0,
                },
            ));
        } else if line.contains("changed")
            && let Some((_, commit)) = found.last_mut()
        {
            let count = |word: &str| {
                line.split(", ")
                    .find(|part| part.contains(word))
                    .and_then(|part| part.trim().split(' ').next()?.parse().ok())
                    .unwrap_or(0)
            };
            commit.insertions = count("insertion");
            commit.deletions = count("deletion");
        }
    }
    found
}

fn repo(system: &dyn System, dir: &Path, name: &str) -> Repo {
    let lines = |output: String| output.lines().map(str::to_owned).collect::<Vec<_>>();
    let mut repo = Repo { name: name.to_owned(), ..Repo::default() };
    repo.files = lines(git(system, dir, &["ls-files"]));
    let format = "--format=%(HEAD)%00%(refname:short)%00%(committerdate:short)%00%(upstream:short)%00%(upstream:track,nobracket)";
    for line in git(system, dir, &["for-each-ref", format, "refs/heads"]).lines() {
        let fields: Vec<&str> = line.split('\0').collect();
        let [head, branch, date, upstream, tracking] = fields[..] else { continue };
        if head == "*" {
            repo.branch = Some(branch.to_owned());
            upstream.clone_into(&mut repo.upstream);
            (repo.ahead, repo.behind) = track(tracking);
        }
        repo.branches.push(Branch {
            name: branch.to_owned(),
            date: date.to_owned(),
            track: tracking.to_owned(),
        });
    }
    repo.branches.sort_by(|a, b| b.date.cmp(&a.date).then_with(|| a.name.cmp(&b.name)));
    repo.dirty = git(system, dir, &["status", "--porcelain"])
        .lines()
        .map(|line| line.get(3..).unwrap_or_default().to_owned())
        .collect();
    repo.stashes = lines(git(system, dir, &["stash", "list", "--format=%gs"]));
    for line in
        git(system, dir, &["log", "-15", "--pretty=format:%ad|%an|%ct|%s", "--date=short"]).lines()
    {
        let mut fields = line.splitn(4, '|');
        let (date, author, at, subject) =
            (fields.next(), fields.next(), fields.next(), fields.next());
        if repo.recent.is_empty() {
            repo.last_commit = at.and_then(|at| at.parse().ok()).unwrap_or(0);
        }
        repo.recent.push((
            date.unwrap_or_default().to_owned(),
            author.unwrap_or_default().to_owned(),
            subject.unwrap_or_default().to_owned(),
        ));
    }
    repo.todos = lines(git(
        system,
        dir,
        &[
            "grep",
            "-n",
            "-I",
            "-E",
            r"\b(TODO|FIXME|HACK|XXX)\b",
            "--",
            ".",
            ":!*.lock",
            ":!*lock.json",
            ":!*.min.*",
        ],
    ));
    repo.origin = git(system, dir, &["config", "--get", "remote.origin.url"]);
    repo.stack = stack(system, dir);
    repo.readme = readme(system, dir);
    repo
}

// ---------------------------------------------------------------- GitHub

/// What GitHub knows about the owner's repositories, by name. Empty without a token or when
/// GitHub does not answer: the context is then written from what is on the machine.
fn github(system: &dyn System, local: &[String]) -> BTreeMap<String, Remote> {
    use devbox_core::github::{api, get, token};
    let mut remotes = BTreeMap::new();
    let Some(token) = token(system) else { return remotes };
    let api = api(system);
    let mut with_tickets: Vec<(String, String)> = Vec::new();
    for page in 1..=10 {
        let path = format!("/user/repos?affiliation=owner&per_page=100&page={page}");
        let Ok(listing) = get(system, &api, &token, &path) else { return remotes };
        let Some(repos) = listing.as_array() else { return remotes };
        for repo in repos {
            let field = |key: &str| repo[key].as_str().unwrap_or_default().to_owned();
            let name = field("name");
            if !local.contains(&name) {
                continue;
            }
            if repo["open_issues_count"].as_u64().unwrap_or(0) > 0 {
                with_tickets.push((name.clone(), field("full_name")));
            }
            let topics = repo["topics"].as_array().map_or_else(Vec::new, |topics| {
                topics.iter().filter_map(|topic| topic.as_str().map(str::to_owned)).collect()
            });
            remotes.insert(
                name,
                Remote {
                    description: field("description"),
                    url: field("html_url"),
                    private: repo["private"].as_bool().unwrap_or(false),
                    homepage: field("homepage"),
                    topics,
                    tickets: Vec::new(),
                },
            );
        }
        if repos.len() < 100 {
            break;
        }
    }
    // Open pull requests and issues come in one list per repository.
    let tickets = in_parallel(&with_tickets, |(_, full_name)| {
        let path = format!("/repos/{full_name}/issues?state=open&per_page=20");
        let listing = get(system, &api, &token, &path).unwrap_or(Value::Null);
        let items = listing.as_array().cloned().unwrap_or_default();
        items
            .iter()
            .map(|item| Ticket {
                number: item["number"].as_u64().unwrap_or(0),
                title: item["title"].as_str().unwrap_or_default().to_owned(),
                labels: item["labels"].as_array().map_or_else(Vec::new, |labels| {
                    labels
                        .iter()
                        .filter_map(|label| label["name"].as_str().map(str::to_owned))
                        .collect()
                }),
                draft: item["draft"].as_bool().unwrap_or(false),
                updated: item["updated_at"].as_str().unwrap_or_default().chars().take(10).collect(),
                pull_request: item.get("pull_request").is_some_and(|value| !value.is_null()),
            })
            .collect::<Vec<_>>()
    });
    for ((name, _), tickets) in with_tickets.iter().zip(tickets) {
        if let Some(remote) = remotes.get_mut(name) {
            remote.tickets = tickets;
        }
    }
    remotes
}

// ---------------------------------------------------------------- agents

/// Text that is not something the owner asked: markup the agent's own interface wrote into the
/// transcript, and markers of interruptions.
fn noise(text: &str) -> bool {
    let text = text.trim_start();
    text.is_empty()
        || text.starts_with('<')
        || text.starts_with("Caveat:")
        || text.starts_with("[Request interrupted")
}

/// The repository a working directory belongs to: the first directory under the code directory.
fn repo_of(code_dir: &Path, cwd: &str) -> Option<String> {
    let inside = Path::new(cwd).strip_prefix(code_dir).ok()?;
    Some(inside.components().next()?.as_os_str().to_string_lossy().into_owned())
}

/// Every file under `dir`, at most five directories deep, whose name satisfies `wanted`.
fn walk(
    system: &dyn System,
    dir: &Path,
    wanted: &dyn Fn(&str) -> bool,
    depth: usize,
    found: &mut Vec<PathBuf>,
) {
    for entry in system.read_dir(dir).unwrap_or_default() {
        let path = dir.join(&entry.name);
        if entry.is_dir {
            if depth < 5 {
                walk(system, &path, wanted, depth + 1, found);
            }
        } else if wanted(&entry.name) {
            found.push(path);
        }
    }
}

/// The hub's event log since `since` (Unix ms). Empty when the hub does not answer.
fn hub_events(system: &dyn System, since_ms: i64) -> Vec<Value> {
    let set = |name: &str| system.env(name).filter(|value| !value.is_empty());
    let Some(socket) = set("DEVBOX_HUB_SOCKET")
        .or_else(|| set("XDG_RUNTIME_DIR").map(|dir| format!("{dir}/devbox-hub.sock")))
    else {
        return Vec::new();
    };
    let url = format!("http://hub/internal/events?since={since_ms}");
    let curl = Command::new(
        "curl",
        ["--silent", "--fail", "--max-time", "10", "--unix-socket", &socket, &url],
    )
    .timeout(Duration::from_secs(15));
    let Ok(output) = system.run(&curl) else { return Vec::new() };
    let answer: Value = serde_json::from_slice(&output.stdout).unwrap_or(Value::Null);
    answer["events"].as_array().cloned().unwrap_or_default()
}

/// What the owner typed, as the agents' own transcripts on disk have it: when, to which agent,
/// in which repository, and the text.
fn transcripts(
    system: &dyn System,
    options: &Options,
) -> Vec<(i64, &'static str, Option<String>, String)> {
    let mut said_to = Vec::new();
    let mut files = Vec::new();
    walk(
        system,
        &options.home.join(".claude/projects"),
        &|name| has_extension(name, "jsonl"),
        0,
        &mut files,
    );
    for file in &files {
        for line in text(system, file, 20 << 20).lines() {
            let Ok(entry) = serde_json::from_str::<Value>(line) else { continue };
            let meta = |key: &str| entry[key].as_bool().unwrap_or(false);
            if entry["type"] != "user" || meta("isMeta") || meta("isSidechain") {
                continue;
            }
            let content = &entry["message"]["content"];
            let said = match content.as_array() {
                Some(parts) => parts
                    .iter()
                    .filter(|part| part["type"] == "text")
                    .filter_map(|part| part["text"].as_str())
                    .collect::<Vec<_>>()
                    .join(" "),
                None => content.as_str().unwrap_or_default().to_owned(),
            };
            let at = entry["timestamp"].as_str().and_then(time::parse_rfc3339).unwrap_or(0);
            let repo = entry["cwd"].as_str().and_then(|cwd| repo_of(&options.code_dir, cwd));
            said_to.push((at, "claude", repo, said));
        }
    }
    let mut rollouts = Vec::new();
    let rollout = |name: &str| name.starts_with("rollout-") && has_extension(name, "jsonl");
    walk(system, &options.home.join(".codex/sessions"), &rollout, 0, &mut rollouts);
    for file in &rollouts {
        let mut cwd: Option<String> = None;
        for line in text(system, file, 20 << 20).lines() {
            let Ok(entry) = serde_json::from_str::<Value>(line) else { continue };
            let payload = if entry["payload"].is_object() { &entry["payload"] } else { &entry };
            if let Some(dir) = payload["cwd"].as_str() {
                cwd = Some(dir.to_owned());
            }
            if payload["type"] == "message" && payload["role"] == "user" {
                let said = payload["content"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|part| part["text"].as_str())
                    .collect::<Vec<_>>()
                    .join(" ");
                let at = entry["timestamp"].as_str().and_then(time::parse_rfc3339).unwrap_or(0);
                let repo = cwd.as_deref().and_then(|cwd| repo_of(&options.code_dir, cwd));
                said_to.push((at, "codex", repo, said));
            }
        }
    }
    said_to
}

struct Agents {
    prompts: Vec<Prompt>,
    needs_input: usize,
    turn_seconds: Vec<u64>,
    sync_problems: Vec<SyncProblem>,
}

/// What the owner asked agents, from the hub's log (every agent) and from the transcripts the
/// agents keep on disk (which also cover sessions the hub never saw), each prompt once.
fn agents(system: &dyn System, options: &Options, now: i64) -> Agents {
    let since = now - HISTORY_DAYS * DAY;
    let mut found = Agents {
        prompts: Vec::new(),
        needs_input: 0,
        turn_seconds: Vec::new(),
        sync_problems: Vec::new(),
    };
    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut add = |found: &mut Agents, at: i64, agent: &str, repo: Option<String>, text: &str| {
        if noise(text) || at < since || !seen.insert(format!("{agent}:{}", clip(text, 80))) {
            return;
        }
        found.prompts.push(Prompt { at, agent: agent.to_owned(), repo, text: text.to_owned() });
    };

    for event in hub_events(system, since * 1000) {
        let at = event["at"].as_i64().unwrap_or(0) / 1000;
        let data = &event["data"];
        match event["type"].as_str().unwrap_or_default() {
            "prompt" => {
                let repo = data["repo"].as_str().filter(|repo| !repo.is_empty()).map(str::to_owned);
                let agent = data["agent"].as_str().unwrap_or("agent");
                add(&mut found, at, agent, repo, data["prompt"].as_str().unwrap_or_default());
            }
            "needs_input" => found.needs_input += 1,
            "turn.done" => {
                found.turn_seconds.extend(data["seconds"].as_u64().filter(|seconds| *seconds > 0));
            }
            "sync" => {
                let bad: Vec<String> = data["results"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter(|result| {
                        matches!(result["status"].as_str(), Some("conflict" | "error"))
                    })
                    .map(|result| {
                        format!(
                            "**{}**: {}",
                            result["name"].as_str().unwrap_or_default(),
                            clip(result["detail"].as_str().unwrap_or_default(), 90)
                        )
                    })
                    .collect();
                if !bad.is_empty() {
                    found.sync_problems.push(SyncProblem { at, line: bad.join("; ") });
                }
            }
            _ => {}
        }
    }

    for (at, agent, repo, said) in transcripts(system, options) {
        add(&mut found, at, agent, repo, &said);
    }
    found.prompts.sort_by(|a, b| a.at.cmp(&b.at).then_with(|| a.text.cmp(&b.text)));
    found.turn_seconds.sort_unstable();
    found
}

// ---------------------------------------------------------------- shell, machine, editor

const WITH_SUBCOMMAND: [&str; 14] = [
    "git",
    "npm",
    "pnpm",
    "cargo",
    "docker",
    "gh",
    "mise",
    "systemctl",
    "incus",
    "agent",
    "bun",
    "uv",
    "make",
    "devbox",
];

/// The heads of the commands in the shell history — `git commit`, `pnpm dev`, never an argument,
/// which is where secrets are typed — with how often each was run, most used first.
fn commands(history: &str) -> Vec<(String, usize)> {
    let mut counts: BTreeMap<String, usize> = BTreeMap::new();
    for line in history.lines().filter(|line| !line.is_empty() && !line.starts_with('#')) {
        let mut words = line.split_whitespace().peekable();
        // What comes before the command proper: sudo, time, and variables set for it.
        while words
            .peek()
            .is_some_and(|word| matches!(*word, "sudo" | "time" | "env") || word.contains('='))
        {
            words.next();
        }
        let Some(command) = words.next() else { continue };
        let simple = |word: &str| {
            !word.is_empty()
                && word.chars().all(|c| c.is_ascii_alphanumeric() || "._-/".contains(c))
        };
        if !simple(command) {
            continue;
        }
        let head = match words.next() {
            Some(sub)
                if WITH_SUBCOMMAND.contains(&command) && simple(sub) && !sub.starts_with('-') =>
            {
                format!("{command} {sub}")
            }
            _ => command.to_owned(),
        };
        *counts.entry(head).or_default() += 1;
    }
    let mut counted: Vec<(String, usize)> = counts.into_iter().collect();
    counted.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
    counted.truncate(25);
    counted
}

const TOOLS: [&str; 10] =
    ["git", "node", "pnpm", "rustc", "go", "gh", "docker", "claude", "codex", "opencode"];

fn machine(system: &dyn System, options: &Options) -> Machine {
    let release = text(system, Path::new("/etc/os-release"), 10_000);
    let os = release
        .lines()
        .find_map(|line| line.strip_prefix("PRETTY_NAME="))
        .map_or_else(String::new, |name| name.trim_matches('"').to_owned());
    let cpus = text(system, Path::new("/proc/cpuinfo"), 1 << 20)
        .lines()
        .filter(|line| line.starts_with("processor"))
        .count();
    let memory_kb: u64 = text(system, Path::new("/proc/meminfo"), 10_000)
        .lines()
        .find_map(|line| line.strip_prefix("MemTotal:"))
        .and_then(|rest| rest.split_whitespace().next()?.parse().ok())
        .unwrap_or(0);
    let versions = in_parallel(&TOOLS, |tool| {
        let command = Command::new(tool, ["--version"]).timeout(Duration::from_secs(8));
        let output = system.run(&command).ok().filter(devbox_core::system::Output::success)?;
        let line = output.text().lines().next().unwrap_or_default().to_owned();
        (!line.is_empty()).then(|| ((*tool).to_owned(), clip(&line, 60)))
    });
    let hub = text(system, &options.home.join(".config/devbox/hub.toml"), 100_000);
    let hosts = devbox_core::config::hub("hub.toml", &hub).ok().map(|parsed| parsed.value.hosts);
    Machine {
        os,
        cpus,
        memory_gib: (memory_kb + (1 << 19)) >> 20,
        tools: versions.into_iter().flatten().collect(),
        code_host: hosts.as_ref().map_or_else(String::new, |hosts| hosts.code.clone()),
        agents_host: hosts.as_ref().map_or_else(String::new, |hosts| hosts.agents.clone()),
    }
}

/// The owner's clock: seconds east of UTC, and its name (`UTC`, `UTC+02:00`).
fn clock(system: &dyn System) -> (i64, String) {
    let zone = system
        .run(&Command::new("date", ["+%z"]).timeout(Duration::from_secs(5)))
        .map_or_else(|_| String::new(), |output| output.text());
    let digits =
        |range: std::ops::Range<usize>| zone.get(range).and_then(|n| n.parse::<i64>().ok());
    let (Some(hours), Some(minutes)) = (digits(1..3), digits(3..5)) else {
        return (0, "UTC".to_owned());
    };
    let offset = hours * 3600 + minutes * 60;
    if offset == 0 {
        return (0, "UTC".to_owned());
    }
    let sign = if zone.starts_with('-') { '-' } else { '+' };
    (if sign == '-' { -offset } else { offset }, format!("UTC{sign}{hours:02}:{minutes:02}"))
}

/// Gathers everything the context files are written from.
pub(crate) fn facts(
    system: &dyn System,
    options: &Options,
    manifest: &devbox_core::proto::DotfilesManifest,
) -> Facts {
    let now = system
        .now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |since| i64::try_from(since.as_secs()).unwrap_or(0));
    let (offset, zone) = clock(system);

    let git_config: BTreeMap<String, String> =
        git(system, &options.home, &["config", "--global", "--list"])
            .lines()
            .filter_map(|line| line.split_once('='))
            .map(|(key, value)| (key.to_lowercase(), value.to_owned()))
            .collect();
    let setting = |key: &str| git_config.get(key).cloned().unwrap_or_default();
    let (name, email) = (setting("user.name"), setting("user.email"));
    let author = [&email, &name].into_iter().find(|value| !value.is_empty()).cloned();

    let names: Vec<String> = system
        .read_dir(&options.code_dir)
        .unwrap_or_default()
        .into_iter()
        .filter(|entry| entry.is_dir && !entry.name.starts_with('.'))
        .filter(|entry| system.exists(&options.code_dir.join(&entry.name).join(".git")))
        .map(|entry| entry.name)
        .collect();
    let since = now - HISTORY_DAYS * DAY;
    let gathered = in_parallel(&names, |name| {
        let dir = options.code_dir.join(name);
        let mine = author.as_ref().map(|author| commits(system, &dir, name, author, since));
        (repo(system, &dir, name), mine.unwrap_or_default())
    });
    let mut remotes = github(system, &names);
    let (mut repos, mut commits, mut seen) = (Vec::new(), Vec::new(), BTreeSet::new());
    for (mut repo, mine) in gathered {
        repo.remote = remotes.remove(&repo.name).unwrap_or_default();
        repos.push(repo);
        // A commit that is in two checkouts (a fork, a copy) is one commit.
        commits.extend(
            mine.into_iter()
                .filter(|(hash, _)| seen.insert(hash.clone()))
                .map(|(_, commit)| commit),
        );
    }
    commits.sort_by(|a, b| a.at.cmp(&b.at).then_with(|| a.subject.cmp(&b.subject)));

    let agents = agents(system, options, now);
    let dotfile =
        |relative: Option<&str>, usual: &str| options.dotfiles.join(relative.unwrap_or(usual));
    let settings = text(
        system,
        &dotfile(manifest.code_server.settings.as_deref(), "code-server/settings.json"),
        1 << 20,
    );
    let extensions = text(
        system,
        &dotfile(manifest.code_server.extensions.as_deref(), "code-server/extensions.txt"),
        1 << 20,
    );
    Facts {
        now,
        offset,
        zone,
        name,
        email,
        git_config,
        repos,
        commits,
        prompts: agents.prompts,
        needs_input: agents.needs_input,
        turn_seconds: agents.turn_seconds,
        sync_problems: agents.sync_problems,
        commands: commands(&text(system, &options.home.join(".bash_history"), 5 << 20)),
        machine: machine(system, options),
        settings: serde_json::from_str(&settings).unwrap_or(Value::Null),
        extensions: extensions
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty() && !line.starts_with('#'))
            .map(str::to_owned)
            .collect(),
    }
}

#[cfg(test)]
mod tests {
    use super::{commands, noise, repo_of, track};
    use std::path::Path;

    #[test]
    fn history_keeps_command_heads_only() {
        let history = "#1790000000\ngit commit -m 'secret plan'\ngit commit --amend\nsudo systemctl restart nginx\nGITHUB_TOKEN=FAKE gh pr create\ncurl -H 'Authorization: Bearer FAKE' https://example.com\npnpm dev\npnpm dev\ngit -C /x status\necho $(cat ~/.ssh/id_ed25519)\nls\n";
        assert_eq!(
            commands(history),
            [
                ("git commit".to_owned(), 2),
                ("pnpm dev".to_owned(), 2),
                ("curl".to_owned(), 1),
                ("echo".to_owned(), 1),
                ("gh pr".to_owned(), 1),
                ("git".to_owned(), 1),
                ("ls".to_owned(), 1),
                ("systemctl restart".to_owned(), 1),
            ]
        );
    }

    #[test]
    fn noise_is_not_a_prompt() {
        assert!(
            noise("")
                && noise("  <command-name>/clear</command-name>")
                && noise("Caveat: the messages below")
        );
        assert!(noise("[Request interrupted by user]"));
        assert!(!noise("fix the flaky test"));
    }

    #[test]
    fn working_directories_belong_to_repositories() {
        let code = Path::new("/home/dev/code");
        assert_eq!(repo_of(code, "/home/dev/code/site/src").as_deref(), Some("site"));
        assert_eq!(repo_of(code, "/home/dev/code/site").as_deref(), Some("site"));
        assert_eq!(repo_of(code, "/home/dev/code"), None);
        assert_eq!(repo_of(code, "/home/dev"), None);
        assert_eq!(repo_of(code, "/tmp/x"), None);
    }

    #[test]
    fn tracking_is_read_as_numbers() {
        assert_eq!(track("ahead 2, behind 3"), (2, 3));
        assert_eq!(track("behind 1"), (0, 1));
        assert_eq!(track(""), (0, 0));
        assert_eq!(track("gone"), (0, 0));
    }
}
