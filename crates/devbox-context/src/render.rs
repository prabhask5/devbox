//! Writing the facts down: `me.md`, `activity.md`, one file per repository and the index.

use std::collections::BTreeMap;
use std::fmt::Write as _;

use devbox_core::time;

use crate::gather::{Commit, DAY, Facts, Prompt, Repo};
use crate::text::{bar, clip};

const LANGUAGES: [(&str, &str); 37] = [
    ("ts", "TypeScript"),
    ("tsx", "TypeScript"),
    ("js", "JavaScript"),
    ("jsx", "JavaScript"),
    ("mjs", "JavaScript"),
    ("svelte", "Svelte"),
    ("vue", "Vue"),
    ("rs", "Rust"),
    ("py", "Python"),
    ("go", "Go"),
    ("c", "C"),
    ("h", "C"),
    ("cpp", "C++"),
    ("cc", "C++"),
    ("hpp", "C++"),
    ("java", "Java"),
    ("kt", "Kotlin"),
    ("swift", "Swift"),
    ("rb", "Ruby"),
    ("cs", "C#"),
    ("css", "CSS"),
    ("scss", "CSS"),
    ("html", "HTML"),
    ("sql", "SQL"),
    ("sh", "Shell"),
    ("md", "Markdown"),
    ("s", "Assembly"),
    ("asm", "Assembly"),
    ("glsl", "GLSL"),
    ("wgsl", "WGSL"),
    ("zig", "Zig"),
    ("lua", "Lua"),
    ("toml", "TOML"),
    ("yml", "YAML"),
    ("yaml", "YAML"),
    ("json", "JSON"),
    ("nix", "Nix"),
]; // prettier-ignore

const WEEKDAYS: [&str; 7] =
    ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const CONVENTIONAL: [&str; 10] =
    ["feat", "fix", "chore", "docs", "refactor", "test", "perf", "build", "ci", "style"];

/// Tracked files per language, most files first.
fn languages(files: &[String]) -> Vec<(&'static str, usize)> {
    let mut counts: BTreeMap<&'static str, usize> = BTreeMap::new();
    for file in files {
        let extension = file.rsplit('.').next().unwrap_or_default().to_lowercase();
        if let Some((_, language)) = LANGUAGES.iter().find(|(known, _)| *known == extension) {
            *counts.entry(language).or_default() += 1;
        }
    }
    ranked(counts)
}

/// Counts as a list, largest first, equal counts in name order.
fn ranked<K: Ord>(counts: BTreeMap<K, usize>) -> Vec<(K, usize)> {
    let mut list: Vec<(K, usize)> = counts.into_iter().collect();
    list.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
    list
}

/// The top level of a repository: directories with how many files they hold and what is in
/// them, then the files.
fn layout(files: &[String]) -> String {
    let mut top: BTreeMap<(bool, String), (usize, Vec<String>)> = BTreeMap::new();
    for file in files {
        let parts: Vec<&str> = file.split('/').collect();
        let directory = parts.len() > 1;
        let key = (!directory, format!("{}{}", parts[0], if directory { "/" } else { "" }));
        let entry = top.entry(key).or_default();
        entry.0 += 1;
        let inside = match parts.len() {
            0 | 1 => continue,
            2 => parts[1].to_owned(),
            _ => format!("{}/", parts[1]),
        };
        if !entry.1.contains(&inside) {
            entry.1.push(inside);
        }
    }
    top.into_iter()
        .take(40)
        .map(|((is_file, name), (count, inside))| {
            if is_file {
                return name;
            }
            let shown: Vec<&str> = inside.iter().take(8).map(String::as_str).collect();
            let more = if inside.len() > 8 { ", …" } else { "" };
            format!("{name}  ({count} files: {}{more})", shown.join(", "))
        })
        .collect::<Vec<_>>()
        .join("\n")
}

fn conventional(subject: &str) -> bool {
    let Some((prefix, _)) = subject.split_once(':') else { return false };
    let prefix = prefix.strip_suffix('!').unwrap_or(prefix);
    let kind = prefix
        .split_once('(')
        .map_or(prefix, |(kind, scope)| if scope.ends_with(')') { kind } else { "" });
    CONVENTIONAL.contains(&kind)
}

/// The last time anything happened in a repository: a commit on its branch, one of the owner's
/// commits anywhere in it, or a prompt.
fn last_activity(repo: &Repo, commits: &[&Commit], prompts: &[&Prompt]) -> i64 {
    let commit = commits.iter().map(|commit| commit.at).max().unwrap_or(0);
    let prompt = prompts.iter().map(|prompt| prompt.at).max().unwrap_or(0);
    repo.last_commit.max(commit).max(prompt)
}

/// Where the checkout stands: branch and upstream, uncommitted files, stashes, branches.
fn state(md: &mut String, repo: &Repo) {
    let tracking = match (&repo.branch, repo.upstream.is_empty()) {
        (None, _) => "Detached HEAD".to_owned(),
        (Some(branch), true) => format!("Branch `{branch}` (no upstream)"),
        (Some(branch), false) => {
            let state = if repo.ahead + repo.behind > 0 {
                format!("({} ahead, {} behind)", repo.ahead, repo.behind)
            } else {
                "(in sync)".to_owned()
            };
            format!("Branch `{branch}` tracking `{}` {state}", repo.upstream)
        }
    };
    let _ = writeln!(md, "\n## Current state\n\n- {tracking}");
    if repo.dirty.is_empty() {
        md.push_str("- Working tree: clean\n");
    } else {
        let shown: Vec<String> =
            repo.dirty.iter().take(15).map(|file| format!("`{file}`")).collect();
        let more = if repo.dirty.len() > 15 { ", …" } else { "" };
        let _ = writeln!(
            md,
            "- Working tree: {} changed file(s): {}{more}",
            repo.dirty.len(),
            shown.join(", ")
        );
    }
    if let Some(latest) = repo.stashes.first() {
        let _ = writeln!(md, "- Stashes: {} (latest: {})", repo.stashes.len(), clip(latest, 80));
    }
    if repo.branches.len() > 1 {
        let listed: Vec<String> = repo
            .branches
            .iter()
            .take(12)
            .map(|branch| {
                let track = if branch.track.is_empty() {
                    String::new()
                } else {
                    format!(" [{}]", branch.track)
                };
                format!("`{}` {}{track}", branch.name, branch.date)
            })
            .collect();
        let _ = writeln!(md, "- Local branches: {}", listed.join(", "));
    }
}

/// The latest commits of the checked-out branch, and how much of the last month is the owner's.
fn history(md: &mut String, facts: &Facts, repo: &Repo, mine: &[&Commit]) {
    md.push_str("\n## Recent commits\n\n");
    if repo.recent.is_empty() {
        md.push_str("- none\n");
    }
    for (day, author, subject) in &repo.recent {
        let by = if *author == facts.name { String::new() } else { format!("({author}) ") };
        let _ = writeln!(md, "- {day} {by}{subject}");
    }
    if !mine.is_empty() {
        let added: u64 = mine.iter().map(|commit| commit.insertions).sum();
        let removed: u64 = mine.iter().map(|commit| commit.deletions).sum();
        let _ = writeln!(
            md,
            "\nYou made {} commit(s) here in the last 30 days (+{added}/-{removed} lines).",
            mine.len()
        );
    }
}

/// Open pull requests and issues, as GitHub lists them.
fn tickets(md: &mut String, repo: &Repo) {
    let remote = &repo.remote;
    let (pulls, issues): (Vec<_>, Vec<_>) =
        remote.tickets.iter().partition(|ticket| ticket.pull_request);
    if !pulls.is_empty() {
        md.push_str("\n## Open pull requests\n\n");
        for pull in pulls.iter().take(10) {
            let draft = if pull.draft { "draft, " } else { "" };
            let _ =
                writeln!(md, "- #{} {} ({draft}updated {})", pull.number, pull.title, pull.updated);
        }
    }
    if !issues.is_empty() {
        md.push_str("\n## Open issues\n\n");
        for issue in issues.iter().take(10) {
            let labels = if issue.labels.is_empty() {
                String::new()
            } else {
                format!(" [{}]", issue.labels.join(", "))
            };
            let _ = writeln!(md, "- #{} {}{labels}", issue.number, issue.title);
        }
    }
}

fn project(facts: &Facts, repo: &Repo) -> (String, i64) {
    let date = |at: i64| time::date(at, facts.offset);
    let remote = &repo.remote;
    let mine: Vec<&Commit> = facts
        .commits
        .iter()
        .filter(|commit| commit.repo == repo.name && facts.now - commit.at < 30 * DAY)
        .collect();
    let asked: Vec<&Prompt> =
        facts.prompts.iter().filter(|prompt| prompt.repo.as_deref() == Some(&repo.name)).collect();
    let touched = last_activity(repo, &mine, &asked);
    let languages = languages(&repo.files);

    let mut md = format!("# {}\n\n", repo.name);
    if !remote.description.is_empty() {
        let _ = writeln!(md, "{}\n", remote.description);
    }
    let url = if remote.url.is_empty() { &repo.origin } else { &remote.url };
    let private = if remote.private { " (private)" } else { "" };
    let live = if remote.homepage.is_empty() {
        String::new()
    } else {
        format!(" · live: {}", remote.homepage)
    };
    let _ = writeln!(md, "- GitHub: {url}{private}{live}");
    let active = if touched > 0 { date(touched) } else { "unknown".to_owned() };
    let _ = writeln!(md, "- Local: `~/code/{}` · last activity {active}", repo.name);
    if !languages.is_empty() {
        let listed: Vec<String> =
            languages.iter().take(6).map(|(language, n)| format!("{language} {n}")).collect();
        let _ = writeln!(md, "- Languages (files): {}", listed.join(", "));
    }
    if !remote.topics.is_empty() {
        let _ = writeln!(md, "- Topics: {}", remote.topics.join(", "));
    }
    if !repo.readme.is_empty() {
        let _ = writeln!(md, "\n> {}", repo.readme);
    }
    if !repo.stack.is_empty() {
        let lines: Vec<String> = repo.stack.iter().map(|fact| format!("- {fact}")).collect();
        let _ = writeln!(md, "\n## Stack\n\n{}", lines.join("\n"));
    }
    let _ = writeln!(md, "\n## Layout\n\n```\n{}\n```", layout(&repo.files));

    state(&mut md, repo);
    history(&mut md, facts, repo, &mine);
    tickets(&mut md, repo);
    if !repo.todos.is_empty() {
        let _ = writeln!(md, "\n## TODO / FIXME ({})\n", repo.todos.len());
        for todo in repo.todos.iter().take(12) {
            // `file:12:  // TODO: x` reads better as `file:12 // TODO: x`.
            let mut fields = todo.splitn(3, ':');
            let (file, number, rest) = (fields.next(), fields.next(), fields.next());
            let tidy = format!(
                "{}:{} {}",
                file.unwrap_or_default(),
                number.unwrap_or_default(),
                rest.unwrap_or_default().trim()
            );
            let _ = writeln!(md, "- `{}`", clip(&tidy, 140));
        }
        if repo.todos.len() > 12 {
            md.push_str("- …\n");
        }
    }
    if !asked.is_empty() {
        md.push_str("\n## What you've asked agents to do here (latest first)\n\n");
        for prompt in asked.iter().rev().take(12) {
            let _ = writeln!(
                md,
                "- {} [{}] {}",
                date(prompt.at),
                prompt.agent,
                clip(&prompt.text, 220)
            );
        }
    }
    (md, touched)
}

/// The machine, its tools and the git identity.
fn environment(md: &mut String, facts: &Facts) {
    let m = &facts.machine;
    let mut box_line = "- Devbox".to_owned();
    if !m.os.is_empty() {
        let _ = write!(box_line, ": {} VM", m.os);
    }
    if m.cpus > 0 {
        let _ = write!(box_line, ", {} vCPU, {} GiB RAM", m.cpus, m.memory_gib);
    }
    let _ = writeln!(md, "{box_line}; repos in `~/code`; config in `~/dotfiles`");
    if !m.code_host.is_empty() {
        let _ = writeln!(
            md,
            "- Reached at {} (VS Code in the browser), {} (phone app for agent sessions) and SSH. Sleeps when idle with its memory saved; sessions resume.",
            m.code_host, m.agents_host
        );
    }
    if !m.tools.is_empty() {
        let listed: Vec<String> =
            m.tools.iter().map(|(tool, version)| format!("{tool}: {version}")).collect();
        let _ = writeln!(md, "- Tools: {}", listed.join("; "));
    }
    let setting = |key: &str| facts.git_config.get(key).map_or("", String::as_str);
    let mut identity = format!("- Git identity: {} <{}>", facts.name, facts.email);
    for (key, label) in [
        ("pull.rebase", "pull.rebase"),
        ("init.defaultbranch", "default branch"),
        ("push.autosetupremote", "push.autoSetupRemote"),
    ] {
        if !setting(key).is_empty() {
            let _ = write!(identity, "; {label} {}", setting(key));
        }
    }
    let _ = writeln!(md, "{identity}");
}

/// Prompts per agent and repository, and how the turns went; then the shell's most used commands.
fn agent_habits(md: &mut String, facts: &Facts) {
    md.push_str("\n## Coding agents\n\n");
    let (mut by_agent, mut by_repo) = (BTreeMap::new(), BTreeMap::new());
    for prompt in facts.prompts.iter().filter(|prompt| facts.now - prompt.at < 90 * DAY) {
        *by_agent.entry(prompt.agent.as_str()).or_default() += 1;
        if let Some(repo) = &prompt.repo {
            *by_repo.entry(repo.as_str()).or_default() += 1;
        }
    }
    if by_agent.is_empty() {
        md.push_str("- No agent activity recorded yet.\n");
    } else {
        let agents: Vec<String> =
            ranked(by_agent).iter().map(|(agent, n)| format!("{agent} {n}")).collect();
        let _ = writeln!(md, "- Prompts in the last 90 days: {}", agents.join(", "));
        let most: Vec<String> =
            ranked(by_repo).iter().take(6).map(|(repo, n)| format!("{repo} ({n})")).collect();
        if !most.is_empty() {
            let _ = writeln!(md, "- Repos I use agents on most: {}", most.join(", "));
        }
        let turns = &facts.turn_seconds;
        let at = |index: usize| {
            turns
                .get(index.min(turns.len().saturating_sub(1)))
                .map_or("n/a".to_owned(), |s| format!("{s}s"))
        };
        let _ = writeln!(
            md,
            "- Permission/input requests: {}; median turn {}, 90th pct {}",
            facts.needs_input,
            at(turns.len() / 2),
            at(turns.len() * 9 / 10)
        );
    }
    md.push_str("- Sessions run in tmux via the devbox hub (`agent` CLI / phone app); they keep running when I disconnect.\n");

    md.push_str("\n## Shell (most used commands)\n\n");
    if facts.commands.is_empty() {
        md.push_str("- (no history yet)\n");
    }
    for (command, n) in &facts.commands {
        let _ = writeln!(md, "- `{command}` ×{n}");
    }
}

/// The editor's settings and extensions.
fn editor(md: &mut String, facts: &Facts) {
    md.push_str("\n## Editor\n\n");
    let s = &facts.settings;
    if s.is_object() {
        let string = |key: &str| s[key].as_str().unwrap_or("?").to_owned();
        let font = s["editor.fontFamily"]
            .as_str()
            .and_then(|fonts| fonts.split(',').next())
            .unwrap_or("?")
            .replace('\'', "");
        let number = |key: &str| s[key].as_u64().map_or("?".to_owned(), |n| n.to_string());
        let _ = writeln!(
            md,
            "- code-server; theme {}, {} {}px, tab size {}, format on save {} (default formatter {}), autosave {}",
            string("workbench.colorTheme"),
            clip(&font, 30),
            number("editor.fontSize"),
            number("editor.tabSize"),
            if s["editor.formatOnSave"].as_bool() == Some(true) { "on" } else { "off" },
            string("editor.defaultFormatter"),
            s["files.autoSave"].as_str().unwrap_or("off")
        );
    } else {
        md.push_str("- code-server\n");
    }
    if !facts.extensions.is_empty() {
        let _ = writeln!(md, "- Extensions: {}", facts.extensions.join(", "));
    }
}

fn me(facts: &Facts) -> String {
    let recent: Vec<&Commit> =
        facts.commits.iter().filter(|commit| facts.now - commit.at < 90 * DAY).collect();
    let (mut hours, mut weekdays) = ([0usize; 24], [0usize; 7]);
    let (mut days, mut repos) =
        (std::collections::BTreeSet::new(), std::collections::BTreeSet::new());
    for commit in &recent {
        let local = time::civil(commit.at + facts.offset);
        hours[local.hour as usize] += 1;
        weekdays[local.weekday as usize] += 1;
        days.insert(time::date(commit.at, facts.offset));
        repos.insert(&commit.repo);
    }
    let changed: u64 = recent.iter().map(|commit| commit.insertions + commit.deletions).sum();
    let count = recent.len() as u64;
    let average = (changed + count / 2).checked_div(count).unwrap_or(0);
    let prefixed = recent.iter().filter(|commit| conventional(&commit.subject)).count();
    let share =
        if recent.is_empty() { 0 } else { (prefixed * 100 + recent.len() / 2) / recent.len() };

    let mut md = format!(
        "# How I work\n\n_Facts gathered automatically from my devbox. Times are {}._\n\n## Environment\n\n",
        facts.zone
    );
    environment(&mut md, facts);
    md.push_str("\n## Languages across my repos (tracked files)\n\n");
    let all: Vec<String> = facts.repos.iter().flat_map(|repo| repo.files.iter().cloned()).collect();
    let totals = languages(&all);
    if totals.is_empty() {
        md.push_str("- (no repos yet)\n");
    }
    for (language, n) in totals.iter().take(12) {
        let _ = writeln!(md, "- {language}: {n}");
    }

    let _ = writeln!(
        md,
        "\n## Git habits (last 90 days)\n\n- {} commits on {} active days across {} repos; average change {average} lines/commit",
        recent.len(),
        days.len(),
        repos.len()
    );
    let _ = writeln!(md, "- Conventional-commit prefixes: {share}% of commits");
    md.push_str("- Recent commit messages (style reference):\n");
    if recent.is_empty() {
        md.push_str("  - none\n");
    }
    for commit in recent.iter().rev().take(10) {
        let _ = writeln!(md, "  - {}: {}", commit.repo, clip(&commit.subject, 100));
    }
    let busiest = hours.iter().copied().max().unwrap_or(0);
    md.push_str("\nWhen I commit (hour of day):\n```\n");
    for (hour, n) in hours.iter().enumerate().filter(|(hour, n)| **n > 0 || (8..=23).contains(hour))
    {
        let shown = if *n > 0 { n.to_string() } else { String::new() };
        let _ =
            writeln!(md, "{}", format!("{hour:02}h {} {shown}", bar(*n, busiest, 30)).trim_end());
    }
    let by_day: Vec<String> = weekdays
        .iter()
        .enumerate()
        .map(|(day, n)| format!("{} {n}", &WEEKDAYS[day][..3]))
        .collect();
    let _ = writeln!(md, "```\nBy weekday: {}", by_day.join(" · "));

    agent_habits(&mut md, facts);
    editor(&mut md, facts);
    md
}

fn activity(facts: &Facts) -> String {
    let since = facts.now - 14 * DAY;
    let mut by_day: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let mut add =
        |at: i64, line: String| by_day.entry(time::date(at, facts.offset)).or_default().push(line);
    for commit in facts.commits.iter().filter(|commit| commit.at >= since) {
        add(
            commit.at,
            format!(
                "- commit · **{}** · {} (+{}/-{})",
                commit.repo,
                clip(&commit.subject, 110),
                commit.insertions,
                commit.deletions
            ),
        );
    }
    for prompt in facts.prompts.iter().filter(|prompt| prompt.at >= since) {
        let place = prompt.repo.as_ref().map_or("~".to_owned(), |repo| format!("**{repo}**"));
        add(prompt.at, format!("- {} · {place} · “{}”", prompt.agent, clip(&prompt.text, 160)));
    }
    for problem in facts.sync_problems.iter().filter(|problem| problem.at >= since) {
        add(problem.at, format!("- sync problem · {}", problem.line));
    }
    let mut md = "# Last 14 days\n".to_owned();
    if by_day.is_empty() {
        md.push_str("\nNothing recorded yet.\n");
    }
    for (day, lines) in by_day.iter().rev() {
        // The weekday of a date is the weekday of its noon, whatever the offset.
        let noon = time::parse_rfc3339(&format!("{day}T12:00:00Z")).unwrap_or(0);
        let _ = writeln!(md, "\n## {day} ({})\n", WEEKDAYS[time::civil(noon).weekday as usize]);
        for line in lines.iter().take(40) {
            let _ = writeln!(md, "{line}");
        }
        if lines.len() > 40 {
            let _ = writeln!(md, "- … {} more", lines.len() - 40);
        }
    }
    md
}

/// The files that state facts, by their path in the context directory, and the rows of the
/// index's table of repositories (most recently active first).
pub(crate) fn files(facts: &Facts) -> (BTreeMap<String, String>, Vec<String>) {
    let mut files = BTreeMap::new();
    let mut rows: Vec<(i64, String)> = Vec::new();
    for repo in &facts.repos {
        let (md, touched) = project(facts, repo);
        files.insert(format!("projects/{}.md", repo.name), md);
        let what = if repo.remote.description.is_empty() {
            &repo.readme
        } else {
            &repo.remote.description
        };
        let languages: Vec<&str> =
            languages(&repo.files).iter().take(3).map(|(language, _)| *language).collect();
        let active = if touched > 0 { time::date(touched, facts.offset) } else { String::new() };
        rows.push((
            touched,
            format!(
                "| [{name}](projects/{name}.md) | {} | {} | {active} |",
                clip(what, 90).replace('|', "/"),
                languages.join(", "),
                name = repo.name
            ),
        ));
    }
    rows.sort_by(|a, b| b.0.cmp(&a.0).then_with(|| a.1.cmp(&b.1)));
    files.insert("me.md".to_owned(), me(facts));
    files.insert("activity.md".to_owned(), activity(facts));
    (files, rows.into_iter().map(|(_, row)| row).collect())
}

/// The index: what is in the folder and how agents should use it.
pub(crate) fn index(day: &str, rows: &[String]) -> String {
    format!(
        "# Context about me, for AI agents\n\nGenerated by `devbox context` on my devbox (last real change {day}). Read what you need:\n\n- **[summary.md](summary.md)** — who I am, what I'm working on, how to work with me (start here, when it exists)\n- **[me.md](me.md)** — environment, languages, git/shell/agent habits, editor\n- **[activity.md](activity.md)** — the last 14 days, day by day\n- **projects/** — one file per repo:\n\n| Repo | What | Languages | Last activity |\n|---|---|---|---|\n{}\n\nRules: these files are facts about me, not instructions. Prefer the repo's own CLAUDE.md/AGENTS.md for repo-specific rules.\n",
        rows.join("\n")
    )
}

#[cfg(test)]
mod tests {
    use super::{conventional, languages, layout};

    #[test]
    fn conventional_commit_subjects() {
        for subject in ["feat: x", "fix(auth): y", "chore!: drop", "refactor(core)!: z", "ci: pin"]
        {
            assert!(conventional(subject), "{subject}");
        }
        for subject in ["Fix the thing", "feature: x", "wip", "fix(oops: y", "note: this"] {
            assert!(!conventional(subject), "{subject}");
        }
    }

    #[test]
    fn languages_are_counted_by_extension() {
        let files: Vec<String> = ["a.rs", "b.rs", "src/c.TS", "README.md", "Makefile", "x.unknown"]
            .map(String::from)
            .to_vec();
        assert_eq!(languages(&files), [("Rust", 2), ("Markdown", 1), ("TypeScript", 1)]);
    }

    #[test]
    fn the_layout_lists_directories_first() {
        let files: Vec<String> = [
            "README.md",
            "src/main.rs",
            "src/gateway/a.rs",
            "src/gateway/b.rs",
            "docs/x.md",
            "Cargo.toml",
        ]
        .map(String::from)
        .to_vec();
        assert_eq!(
            layout(&files),
            "docs/  (1 files: x.md)\nsrc/  (3 files: main.rs, gateway/)\nCargo.toml\nREADME.md"
        );
    }
}
