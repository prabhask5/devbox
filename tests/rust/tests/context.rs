//! `devbox context` on a fixture home: repositories with known history, agent transcripts, a
//! hub with an event log, shell history, a GitHub that knows two of the repositories. What it
//! writes is compared with `tests/fixtures/context/expected/` (`UPDATE_GOLDEN=1` rewrites it).

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::sync::atomic::{AtomicU32, Ordering};
use std::time::{Duration, SystemTime};

use devbox_tests::github::{FakeGitHub, TOKEN};
use devbox_tests::hub::FakeHub;
use devbox_tests::sandbox::Sandbox;
use serde_json::{Value, json};

/// The secrets of `tests/fixtures/context/secrets.txt`, in the order the fixture uses them.
fn secrets() -> Vec<String> {
    devbox_tests::read("tests/fixtures/context/secrets.txt").lines().map(str::to_owned).collect()
}

struct Home {
    s: Sandbox,
    github: FakeGitHub,
    hub: FakeHub,
}

fn ms(date: &str) -> i64 {
    devbox_core::time::parse_rfc3339(date).unwrap() * 1000
}

/// Numbers the fake hubs' sockets.
static NEXT: AtomicU32 = AtomicU32::new(0);

/// A commit of everything in `dir`, made at `date`.
fn commit(s: &mut Sandbox, dir: &Path, date: &str, subject: &str) {
    s.git(dir, &["add", "--all"]);
    s.git_on(date, dir, &["commit", "--quiet", "-m", subject]);
}

#[allow(clippy::too_many_lines)] // one fixture, told from top to bottom
fn home() -> Home {
    let secret = secrets();
    let mut s = Sandbox::new();
    s.only_tools(&["git", "curl", "date", "sh", "cat"]);
    s.set_env("TZ", "UTC");
    s.program("node", "echo v24.1.0");
    s.program("pnpm", "echo 12.23.0");
    s.program("rustc", "echo 'rustc 1.100.0 (0ef1bc386 2026-08-04)'");
    s.program(
        "gh",
        "[ \"$1\" = --version ] && echo 'gh version 2.80.0 (2026-07-01)' && exit 0; exit 1",
    );

    // The machine.
    let rootfs = s.root().join("rootfs");
    s.write(&rootfs, "etc/os-release", "NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 26.04 LTS\"\n");
    s.write(&rootfs, "proc/cpuinfo", "processor\t: 0\nmodel name\t: x\n\n".repeat(4));
    s.write(&rootfs, "proc/meminfo", "MemTotal:        8132456 kB\nMemFree:          100000 kB\n");
    s.write(
        &s.home(),
        ".config/devbox/hub.toml",
        "version = 1\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n",
    );
    s.git(&s.home(), &["config", "--global", "pull.rebase", "true"]);
    s.git(&s.home(), &["config", "--global", "push.autoSetupRemote", "true"]);

    // site: a web app, on GitHub, with unpushed work, a stash and a safety ref of devbox sync.
    let site = s.repository("site");
    s.write(&site, "README.md", "# site\n\n[![ci](https://example.com/badge.svg)](https://example.com)\n\nThe marketing site: a landing page and a blog, statically built.\n\n## Develop\n\n- pnpm dev\n");
    s.write(&site, "package.json", r#"{"name": "site", "scripts": {"dev": "vite dev", "build": "vite build", "test": "vitest run"}, "dependencies": {"@sveltejs/kit": "^3", "svelte": "^6"}, "devDependencies": {"vite": "^8", "vitest": "^5", "typescript": "~6"}}"#);
    s.write(&site, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n");
    s.write(&site, "src/index.ts", "export const title = 'site';\n");
    s.write(&site, "src/routes/+page.svelte", "<h1>site</h1>\n");
    s.write(&site, ".github/workflows/ci.yml", "name: ci\n");
    s.write(&site, "CLAUDE.md", "# site\nUse pnpm.\n");
    commit(&mut s, &site, "2026-09-10T09:00:00Z", "feat: landing page");
    s.write(
        &site,
        "src/index.ts",
        devbox_tests::read("tests/fixtures/context/index.ts.txt").replace("{password}", &secret[6]),
    );
    commit(&mut s, &site, "2026-09-18T15:30:00Z", "fix(nav): close the menu on route change");
    s.write(&site, "README.md", "# site\n\n[![ci](https://example.com/badge.svg)](https://example.com)\n\nThe marketing site: a landing page and a blog, statically built.\n\n## Develop\n\n- pnpm dev\n- pnpm build\n");
    s.git(&site, &["add", "--all"]);
    s.git_on(
        "2026-09-19T11:00:00Z",
        &site,
        &[
            "-c",
            "user.name=Robin",
            "-c",
            "user.email=robin@example.com",
            "commit",
            "--quiet",
            "-m",
            "docs: how to build",
        ],
    );
    s.git(&site, &["push", "--quiet", "origin", "main"]);
    s.write(&site, "src/nav.ts", "export const nav = [];\n");
    commit(
        &mut s,
        &site,
        "2026-09-20T22:10:00Z",
        &format!("wip navigation, deploy with {}", secret[2]),
    );
    // A stash and devbox sync's safety ref: commits of the owner's, but not history.
    s.write(&site, "src/index.ts", "export const title = 'stashed experiment';\n");
    s.git_on(
        "2026-09-20T23:00:00Z",
        &site,
        &["stash", "push", "--quiet", "-m", "an experiment set aside"],
    );
    let stash = s.git(&site, &["rev-parse", "refs/stash"]);
    s.git(&site, &["update-ref", "refs/devbox-sync/backup-1789900000", &stash]);
    s.write(&site, "src/draft.ts", "export {};\n");

    // engine: Rust, on GitHub, with a second branch.
    let engine = s.repository("engine");
    s.write(&engine, "Cargo.toml", "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.dependencies]\nserde = \"1\"\nthiserror = \"2\"\n");
    s.write(&engine, "crates/matcher/src/lib.rs", "pub fn matches() {}\n");
    s.write(
        &engine,
        "Makefile",
        ".PHONY: test\ntest:\n\tcargo test\nbench: test\n\tcargo bench\nVERSION := 1\n",
    );
    s.write(&engine, "Dockerfile", "FROM scratch\n");
    commit(&mut s, &engine, "2026-08-30T08:00:00Z", "refactor: split the matcher into a crate");
    s.git(&engine, &["push", "--quiet", "origin", "main"]);
    s.git(&engine, &["checkout", "--quiet", "-b", "feature/levels"]);
    s.write(&engine, "crates/matcher/src/levels.rs", "pub struct Level;\n");
    commit(&mut s, &engine, "2026-09-15T19:45:00Z", "feat: price levels");
    s.git(&engine, &["checkout", "--quiet", "main"]);

    // notes: local only.
    let notes = s.repo("notes");
    s.git(&s.code(), &["init", "--quiet", "notes"]);
    s.write(&notes, "ideas.md", "# Ideas\n");
    s.write(&notes, "2026/june.md", "# June\n");
    commit(&mut s, &notes, "2026-06-01T07:15:00Z", "Meeting notes for June");
    // Not a repository: not in the context.
    s.write(&s.code(), "scratch/todo.txt", "x\n");

    // What the owner asked agents: through the hub, and in the agents' own transcripts.
    let event =
        |at: &str, kind: &str, data: Value| json!({"at": ms(at), "type": kind, "data": data});
    let prompt = |at: &str, agent: &str, repo: &str, text: &str| {
        event(
            at,
            "prompt",
            json!({"sessionId": "a1b2c3d4", "agent": agent, "repo": repo, "prompt": text}),
        )
    };
    let events = vec![
        prompt("2026-09-12T10:00:00Z", "claude", "site", "Make the landing page hero responsive"),
        event(
            "2026-09-12T10:01:00Z",
            "needs_input",
            json!({"agent": "claude", "repo": "site", "what": "Run pnpm test?"}),
        ),
        event(
            "2026-09-12T10:03:00Z",
            "turn.done",
            json!({"agent": "claude", "repo": "site", "seconds": 95}),
        ),
        prompt("2026-09-15T19:00:00Z", "codex", "engine", "Add price levels to the matcher"),
        event(
            "2026-09-15T19:20:00Z",
            "turn.done",
            json!({"agent": "codex", "repo": "engine", "seconds": 40}),
        ),
        event(
            "2026-09-19T06:00:00Z",
            "sync",
            json!({"reason": "wake", "results": [
                {"name": "site", "status": "conflict", "detail": "your 1 unpushed commit conflicts with 1 upstream commit; left exactly as it was"},
                {"name": "engine", "status": "current", "detail": "up to date on main"},
            ]}),
        ),
        event(
            "2026-09-19T06:05:00Z",
            "sync",
            json!({"reason": "requested", "results": [{"name": "site", "status": "updated", "detail": "pulled 1 commit"}]}),
        ),
        prompt(
            "2026-09-20T21:30:00Z",
            "claude",
            "site",
            &format!(
                "Deploy it. The token is {} and the hook is https://deploy:{}@hooks.example.com/site",
                secret[0], secret[7]
            ),
        ),
        event(
            "2026-09-20T21:31:00Z",
            "needs_input",
            json!({"agent": "claude", "repo": "site", "what": "Push to main?"}),
        ),
        event(
            "2026-09-20T21:40:00Z",
            "turn.done",
            json!({"agent": "claude", "repo": "site", "seconds": 12}),
        ),
        prompt("2026-09-21T08:00:00Z", "opencode", "", "What changed upstream this week?"),
        event("2026-09-21T08:00:30Z", "session.kill", json!({"sessionId": "a1b2c3d4"})),
    ];
    let user = |at: &str, cwd: &Path, content: Value, extra: Value| {
        let mut line = json!({"type": "user", "timestamp": at, "cwd": cwd, "message": {"role": "user", "content": content}});
        line.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
        line.to_string()
    };
    let transcript = [
        // Already in the hub's log: counted once.
        user("2026-09-12T10:00:01Z", &site, json!("Make the landing page hero responsive"), json!({})),
        user("2026-09-13T14:00:00.250Z", &site, json!([{"type": "text", "text": "Write the blog index"}, {"type": "text", "text": format!("with the Slack hook {}", secret[3])}]), json!({})),
        user("2026-09-13T14:05:00Z", &site, json!("<command-name>/clear</command-name>"), json!({})),
        user("2026-09-13T14:06:00Z", &site, json!("[Request interrupted by user]"), json!({})),
        user("2026-09-13T14:07:00Z", &site, json!("injected by a hook"), json!({"isMeta": true})),
        user("2026-09-13T14:08:00Z", &site, json!("a subagent's task"), json!({"isSidechain": true})),
        json!({"type": "assistant", "timestamp": "2026-09-13T14:09:00Z", "message": {"content": "Done."}}).to_string(),
        "not json at all".to_owned(),
        // Long ago: outside what is looked at.
        user("2026-01-05T09:00:00Z", &site, json!("Set up the repository"), json!({})),
        user("2026-09-14T16:00:00Z", &s.home(), json!(format!("Why does my shell say AWS key {} is invalid?", secret[4])), json!({})),
    ];
    s.write(
        &s.home(),
        ".claude/projects/-home-dev-code-site/0f1e2d3c.jsonl",
        transcript.join("\n"),
    );
    let rollout = [
        json!({"timestamp": "2026-09-16T09:00:00Z", "type": "session_meta", "payload": {"cwd": engine}}).to_string(),
        json!({"timestamp": "2026-09-16T09:00:05Z", "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Benchmark the matcher"}]}}).to_string(),
        json!({"timestamp": "2026-09-16T09:01:00Z", "type": "response_item", "payload": {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Running."}]}}).to_string(),
        json!({"timestamp": "2026-09-16T09:05:00Z", "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": format!("Use the JWT {}", secret[5])}]}}).to_string(),
    ];
    s.write(
        &s.home(),
        ".codex/sessions/2026/09/16/rollout-2026-09-16T09-00-00-abc.jsonl",
        rollout.join("\n"),
    );
    s.write(
        &s.home(),
        ".bash_history",
        format!("#1789990000\ngit status\ngit status\ngit commit -m 'checkpoint-zz'\npnpm dev\npnpm dev\npnpm dev\ncargo test --workspace\nGITHUB_TOKEN={} gh pr create\ncurl -H 'Authorization: Bearer {}' https://api.example.com\nexport API_PASSWORD={}\nls -Zq\ncd code/site\n", secret[0], secret[1], secret[6]),
    );

    // The dotfiles: a repository with an upstream, a manifest, and the editor's settings.
    s.upstream("dotfiles");
    let dotfiles = s.home().join("dotfiles");
    s.git(&s.home(), &["clone", "--quiet", &s.remote("dotfiles").to_string_lossy(), "dotfiles"]);
    s.write(&dotfiles, "devbox.toml", "version = 1\n\n[context]\ndir = \"context\"\n");
    s.write(&dotfiles, "code-server/settings.json", r#"{"workbench.colorTheme": "GitHub Dark", "editor.fontFamily": "'JetBrains Mono', monospace", "editor.fontSize": 13, "editor.tabSize": 2, "editor.formatOnSave": true, "editor.defaultFormatter": "esbenp.prettier-vscode", "files.autoSave": "onFocusChange"}"#);
    s.write(
        &dotfiles,
        "code-server/extensions.txt",
        "# one per line\nesbenp.prettier-vscode\nrust-lang.rust-analyzer\n\nsvelte.svelte-vscode\n",
    );
    commit(&mut s, &dotfiles, "2026-09-01T00:00:00Z", "dotfiles");
    s.git(&dotfiles, &["push", "--quiet", "origin", "main"]);

    // GitHub knows site and engine, and something that is not on this machine.
    let listed = |name: &str, extra: Value| {
        let mut repo = json!({"name": name, "full_name": format!("dev/{name}"), "owner": {"login": "dev"}, "html_url": format!("https://github.com/dev/{name}"), "private": false, "archived": false, "homepage": null, "description": null, "topics": [], "open_issues_count": 0});
        repo.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
        repo
    };
    let github = FakeGitHub::with_issues(
        "dev",
        vec![
            listed(
                "site",
                json!({"private": true, "description": "Marketing site and blog", "homepage": "https://site.example.com", "topics": ["svelte", "static-site"], "open_issues_count": 3}),
            ),
            listed("engine", json!({"description": "An order book | matching engine"})),
            listed("elsewhere", json!({"description": "Not cloned here", "open_issues_count": 5})),
        ],
        json!({"dev/site": [
            {"number": 12, "title": "Blog index", "draft": true, "updated_at": "2026-09-18T15:31:00Z", "labels": [], "pull_request": {"url": "x"}},
            {"number": 9, "title": "Images are not lazy-loaded", "updated_at": "2026-09-02T10:00:00Z", "labels": [{"name": "bug"}, {"name": "performance"}]},
            {"number": 7, "title": format!("Rotate the deploy key, token: {}", secret[1]), "updated_at": "2026-08-20T10:00:00Z", "labels": []},
        ]}),
    );
    s.set_env("GITHUB_TOKEN", TOKEN);
    s.set_env("DEVBOX_GITHUB_API", &github.url);

    // Unix socket paths are short: not under the sandbox, whose own path is long already.
    let socket = PathBuf::from(format!(
        "/tmp/dbx-hub-{}-{}.sock",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    let _ = std::fs::remove_file(&socket);
    let hub = FakeHub::start(&socket, events);
    s.set_env("DEVBOX_HUB_SOCKET", &socket.to_string_lossy());
    Home { s, github, hub }
}

/// `content` as it is on any machine: the sandbox's own directory and the version of the git
/// that happens to be installed are replaced.
fn portable(s: &Sandbox, content: &str) -> String {
    let scrubbed = s.scrub(content);
    let Some(start) = scrubbed.find("git: git version ") else { return scrubbed };
    let end = scrubbed[start..].find([';', '\n']).map_or(scrubbed.len(), |at| start + at);
    format!("{}git: git version <installed>{}", &scrubbed[..start], &scrubbed[end..])
}

/// Every file under `dir`, by its path relative to it.
fn tree(dir: &Path) -> BTreeMap<String, String> {
    fn walk(root: &Path, dir: &Path, files: &mut BTreeMap<String, String>) {
        for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
            let path = entry.path();
            if path.is_dir() {
                walk(root, &path, files);
            } else {
                let name = path.strip_prefix(root).unwrap().to_string_lossy().into_owned();
                files.insert(name, std::fs::read_to_string(&path).unwrap());
            }
        }
    }
    let mut files = BTreeMap::new();
    walk(dir, dir, &mut files);
    files
}

fn context_dir(home: &Home) -> PathBuf {
    home.s.home().join("dotfiles/context")
}

#[test]
fn the_context_of_the_fixture_home() {
    let home = home();
    let (code, out, err) = home.s.devbox(&["context", "--no-ai"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert_eq!(home.s.scrub(&out), "context written to <sandbox>/home/dotfiles/context\n");

    let written = tree(&context_dir(&home));
    let expected_dir = devbox_tests::repo("tests/fixtures/context/expected");
    if std::env::var_os("UPDATE_GOLDEN").is_some() {
        let _ = std::fs::remove_dir_all(&expected_dir);
        for (name, content) in &written {
            let path = expected_dir.join(name);
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(path, portable(&home.s, content)).unwrap();
        }
    }
    let expected = tree(&expected_dir);
    assert_eq!(written.keys().collect::<Vec<_>>(), expected.keys().collect::<Vec<_>>());
    for (name, content) in &written {
        assert_eq!(&portable(&home.s, content), &expected[name], "{name}");
    }
    assert_eq!(
        written.keys().map(String::as_str).collect::<Vec<_>>(),
        [
            "README.md",
            "activity.md",
            "me.md",
            "projects/engine.md",
            "projects/notes.md",
            "projects/site.md"
        ]
    );

    // Sources were asked the way they should be.
    assert_eq!(home.hub.requests().len(), 1);
    assert!(home.hub.requests()[0].starts_with("/internal/events?since="));
    assert_eq!(
        home.github.targets(),
        [
            "/user/repos?affiliation=owner&per_page=100&page=1",
            "/repos/dev/site/issues?state=open&per_page=20"
        ]
    );
    let marker =
        std::fs::read_to_string(home.s.home().join(".local/state/devbox/context-last-run"))
            .unwrap();
    assert_eq!(marker, "2026-09-21T14:13:20Z\n");

    // The same facts again: the same bytes, and nothing rewritten.
    let index = context_dir(&home).join("README.md");
    let long_ago = SystemTime::UNIX_EPOCH + Duration::from_secs(1_000_000_000);
    std::fs::File::options().write(true).open(&index).unwrap().set_modified(long_ago).unwrap();
    let (code, out, _) = home.s.devbox(&["context", "--no-ai"]);
    assert_eq!((code, out.as_str()), (ExitCode::SUCCESS, "context unchanged\n"));
    assert_eq!(tree(&context_dir(&home)), written);
    assert_eq!(
        std::fs::metadata(&index).unwrap().modified().unwrap(),
        long_ago,
        "README.md was rewritten"
    );
}

#[test]
fn stashes_and_safety_refs_are_not_history() {
    let home = home();
    let _ = home.s.devbox(&["context", "--no-ai"]);
    let files = tree(&context_dir(&home));
    // Five commits of the owner's in the last ninety days: three in site (one of them unpushed)
    // and two in engine (one on a branch that is not checked out) — and not the stash's two
    // commits, although they are the owner's, recent, and kept alive by two refs.
    assert!(
        files["me.md"].contains("- 5 commits on 5 active days across 2 repos"),
        "{}",
        files["me.md"]
    );
    assert!(files["projects/site.md"].contains("You made 3 commit(s) here in the last 30 days"));
    for (name, content) in &files {
        assert!(
            !content.contains("stashed experiment") && !content.contains("WIP on main"),
            "{name}"
        );
        assert!(!content.contains("index on main"), "{name}");
    }
    // The stash is part of the checkout's state, as a count and its message.
    assert!(
        files["projects/site.md"]
            .contains("- Stashes: 1 (latest: On main: an experiment set aside)")
    );
    // Commits of others are in the repository's history, and not in the owner's habits.
    assert!(files["projects/site.md"].contains("- 2026-09-19 (Robin) docs: how to build"));
    assert!(!files["me.md"].contains("docs: how to build"));
}

#[test]
fn no_secret_reaches_any_file() {
    let home = home();
    let (_, printed, _) = home.s.devbox(&["context", "--print"]);
    let _ = home.s.devbox(&["context", "--no-ai"]);
    let files = tree(&context_dir(&home));
    for secret in secrets() {
        assert!(!printed.contains(&secret), "{secret} is printed");
        for (name, content) in &files {
            assert!(!content.contains(&secret), "{secret} is in {name}");
        }
    }
    // What stood around the secrets is still there, so it is the secrets that were taken out.
    let all = files.values().cloned().collect::<Vec<_>>().join("\n");
    for kept in [
        "Deploy it. The token is <redacted-token> and the hook is https://<redacted>@hooks.example.com/site",
        "with the Slack hook <redacted-token>",
        "Why does my shell say AWS key <redacted-key> is invalid?",
        "Use the JWT <redacted-jwt>",
        "wip navigation, deploy with <redacted-key>",
        "Rotate the deploy key, token: <redacted>",
        "remove the test login, password=<redacted>",
    ] {
        assert!(all.contains(kept), "{kept}\n{all}");
    }
    // The shell history gives command heads and nothing typed after them.
    let me = &files["me.md"];
    assert!(me.contains("- `pnpm dev` ×3\n- `git status` ×2\n"), "{me}");
    for typed in [
        "checkpoint-zz",
        "api.example.com",
        "Authorization",
        "API_PASSWORD",
        "code/site",
        "--workspace",
        "-Zq",
    ] {
        assert!(!me.contains(typed), "{typed} of the shell history is in me.md");
    }
}

#[test]
fn printing_writes_nothing() {
    let home = home();
    let (code, out, err) = home.s.devbox(&["context", "--print"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    for file in
        ["activity.md", "me.md", "projects/engine.md", "projects/notes.md", "projects/site.md"]
    {
        assert!(out.contains(&format!("\n===== {file}\n# ")), "{file}");
    }
    assert!(!context_dir(&home).exists());
    assert!(!home.s.home().join(".local/state/devbox/context-last-run").exists());
}

#[test]
fn a_repository_that_is_gone_leaves_the_context() {
    let home = home();
    let _ = home.s.devbox(&["context", "--no-ai"]);
    std::fs::remove_dir_all(home.s.repo("notes")).unwrap();
    let (_, out, _) = home.s.devbox(&["context", "--no-ai"]);
    assert!(out.starts_with("context written to "), "{out}");
    let files = tree(&context_dir(&home));
    assert!(!files.contains_key("projects/notes.md"));
    assert!(!files["README.md"].contains("notes"));
}

#[test]
fn without_dotfiles_github_or_a_hub_the_context_is_still_written() {
    let mut s = Sandbox::new();
    s.only_tools(&["git", "curl", "date", "sh"]);
    s.set_env("TZ", "UTC");
    let _ = s.repository("solo");
    let (code, out, err) = s.devbox(&["context"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert_eq!(s.scrub(&out), "context written to <sandbox>/home/.local/share/devbox/context\n");
    let files = tree(&s.home().join(".local/share/devbox/context"));
    assert_eq!(
        files.keys().map(String::as_str).collect::<Vec<_>>(),
        ["README.md", "activity.md", "me.md", "projects/solo.md"]
    );
    assert!(files["me.md"].contains("- No agent activity recorded yet."));
    assert!(files["me.md"].contains("- (no history yet)"));
    assert!(files["activity.md"].contains("Nothing recorded yet."));
    assert!(s.scrub(&files["projects/solo.md"]).contains("- GitHub: <sandbox>/remotes/solo.git\n"));

    // An empty code directory is no reason to fail either.
    std::fs::remove_dir_all(s.repo("solo")).unwrap();
    let (code, _, err) = s.devbox(&["context"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
}

#[test]
fn an_invalid_manifest_is_said_and_its_settings_ignored() {
    let home = home();
    home.s.write(
        &home.s.home().join("dotfiles"),
        "devbox.toml",
        "version = 1\n[context]\ndir = 7\n",
    );
    let (code, out, err) = home.s.devbox(&["context", "--no-ai"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert!(
        err.starts_with("devbox context: devbox.toml")
            && err.ends_with("; its settings are ignored\n"),
        "{err}"
    );
    assert_eq!(home.s.scrub(&out), "context written to <sandbox>/home/dotfiles/context\n");
}

// ---------------------------------------------------------------- committing

fn dotfiles(home: &Home) -> PathBuf {
    home.s.home().join("dotfiles")
}

/// The subjects of the commits on the dotfiles' upstream, newest first.
fn pushed(home: &Home) -> String {
    home.s.git(&home.s.remote("dotfiles"), &["log", "--format=%s", "main"])
}

#[test]
fn committing_takes_only_the_context_and_leaves_other_changes_alone() {
    let home = home();
    let dir = dotfiles(&home);
    // The owner is in the middle of something in their dotfiles.
    home.s.write(&dir, "bashrc", "alias g=git\n");
    home.s.git(&dir, &["add", "bashrc"]);
    home.s.write(
        &dir,
        "devbox.toml",
        "version = 1\n\n[context]\ndir = \"context\"\n# a note to self\n",
    );
    home.s.write(&dir, "scratch.txt", "untracked\n");

    let (code, out, err) = home.s.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!(
        (code, out.as_str(), err.as_str()),
        (ExitCode::SUCCESS, "context updated and pushed\n", "")
    );
    assert_eq!(pushed(&home), "context: auto-update\ndotfiles\nfirst");
    let committed =
        home.s.git(&home.s.remote("dotfiles"), &["show", "--format=", "--name-only", "main"]);
    assert_eq!(
        committed,
        "context/README.md\ncontext/activity.md\ncontext/me.md\ncontext/projects/engine.md\ncontext/projects/notes.md\ncontext/projects/site.md"
    );
    // What was staged is staged, what was edited is edited, what was untracked is untracked.
    assert_eq!(
        home.s.git(&dir, &["status", "--porcelain"]),
        "A  bashrc\n M devbox.toml\n?? scratch.txt"
    );

    // Nothing changed: no commit.
    let (_, out, _) = home.s.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!(out, "context unchanged\n");
    assert_eq!(pushed(&home), "context: auto-update\ndotfiles\nfirst");
    assert_eq!(home.s.git(&dir, &["rev-list", "--count", "HEAD"]), "3");
}

#[test]
fn a_push_that_fails_keeps_the_commit_for_the_next_run() {
    let home = home();
    let dir = dotfiles(&home);
    let upstream = home.s.remote("dotfiles").to_string_lossy().into_owned();
    home.s.git(&dir, &["remote", "set-url", "origin", "/nowhere/dotfiles.git"]);

    let (code, out, _) = home.s.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert_eq!(
        home.s.scrub(&out),
        "context committed in <sandbox>/home/dotfiles/context, not pushed: the push failed\n"
    );
    assert_eq!(home.s.git(&dir, &["log", "--format=%s", "-1"]), "context: auto-update");
    assert_eq!(pushed(&home), "dotfiles\nfirst");

    // The next run has nothing new to write, and pushes what waits.
    home.s.git(&dir, &["remote", "set-url", "origin", &upstream]);
    let (_, out, _) = home.s.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!(out, "context updated and pushed\n");
    assert_eq!(pushed(&home), "context: auto-update\ndotfiles\nfirst");
}

#[test]
fn the_owners_own_unpushed_commits_are_never_pushed_for_them() {
    let mut home = home();
    let dir = dotfiles(&home);
    home.s.write(&dir, "bashrc", "alias g=git\n");
    commit(&mut home.s, &dir, "2026-09-21T10:00:00Z", "shell: an alias, not ready to share");

    let (_, out, _) = home.s.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!(
        home.s.scrub(&out),
        "context committed in <sandbox>/home/dotfiles/context, not pushed: the dotfiles have other commits that are not pushed\n"
    );
    assert_eq!(pushed(&home), "dotfiles\nfirst");
    assert_eq!(
        home.s.git(&dir, &["log", "--format=%s", "-2"]),
        "context: auto-update\nshell: an alias, not ready to share"
    );
}

#[test]
fn what_others_pushed_to_the_dotfiles_is_taken_in_before_pushing() {
    let home = home();
    let dir = dotfiles(&home);
    let _ = home.s.git(&home.s.seed("dotfiles"), &["pull", "--quiet"]);
    home.s.push_upstream("dotfiles", "main", "from-the-laptop.txt", "x\n");
    home.s.write(&dir, "notes.txt", "uncommitted, and still here afterwards\n");

    let (_, out, _) = home.s.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!(out, "context updated and pushed\n");
    assert_eq!(
        pushed(&home),
        "context: auto-update\nupstream: from-the-laptop.txt\ndotfiles\nfirst"
    );
    assert!(dir.join("from-the-laptop.txt").exists());
    assert_eq!(home.s.git(&dir, &["status", "--porcelain"]), "?? notes.txt");
}

#[test]
fn the_manifest_can_turn_commits_off_and_without_a_repository_there_is_none() {
    let home = home();
    let dir = dotfiles(&home);
    home.s.write(
        &dir,
        "devbox.toml",
        "version = 1\n[context]\ndir = \"context\"\ncommit = false\n",
    );
    let (_, out, _) = home.s.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!(home.s.scrub(&out), "context written to <sandbox>/home/dotfiles/context\n");
    assert_eq!(home.s.git(&dir, &["log", "--format=%s", "-1"]), "dotfiles");

    // Dotfiles that are a plain directory.
    let plain = Sandbox::new();
    plain.write(&plain.home(), "dotfiles/devbox.toml", "version = 1\n");
    let (code, out, err) = plain.devbox(&["context", "--no-ai", "--commit"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert_eq!(plain.scrub(&out), "context written to <sandbox>/home/dotfiles/context\n");
}

// ---------------------------------------------------------------- the AI summary

/// Installs a `claude` that records how it was called and answers with `answer`.
fn claude(home: &Home, answer: &str) {
    let record = home.s.root().join("claude-calls");
    home.s.program(
        "claude",
        &format!(
            "[ \"$1\" = --version ] && echo '2.5.0 (Claude Code)' && exit 0\nprintf '%s\\n' \"$*\" >> '{0}'\ncat > '{0}.input'\ncat <<'ANSWER'\n{answer}\nANSWER",
            record.display()
        ),
    );
}

fn claude_calls(home: &Home) -> Vec<String> {
    let record = std::fs::read_to_string(home.s.root().join("claude-calls")).unwrap_or_default();
    record.lines().filter(|line| line.starts_with("-p ")).map(str::to_owned).collect()
}

fn summary(home: &Home) -> Option<String> {
    std::fs::read_to_string(context_dir(home).join("summary.md")).ok()
}

const ANSWER: &str = "## Who I am as a developer\nA web and systems developer: Svelte with pnpm on the site, a Rust workspace for the engine.\n\n## What I'm working on right now\n- site: navigation, unpushed.\n- engine: price levels, on a branch.\n\n## How to work with me\nConventional commits; small changes.\n\n## Open threads\nThe deploy key, password=FAKEpasswordgggg, needs rotating.";

#[test]
fn the_summary_is_written_by_the_owners_claude_at_most_once_in_twenty_hours() {
    let mut home = home();
    claude(&home, ANSWER);
    let (code, out, err) = home.s.devbox(&["context"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert!(out.starts_with("context written to "), "{out}");

    let written = summary(&home).unwrap();
    assert!(
        written.starts_with("# Summary (AI-written)\n\n_Generated 2026-09-21 by Claude from the files in this folder._\n\n## Who I am as a developer\n"),
        "{written}"
    );
    // What the model says is redacted like everything else.
    assert!(
        written.contains("password=<redacted>") && !written.contains("FAKEpasswordgggg"),
        "{written}"
    );
    let calls = claude_calls(&home);
    assert_eq!(calls.len(), 1);
    assert!(calls[0].starts_with("-p You are maintaining a context file"), "{}", calls[0]);
    let record = std::fs::read_to_string(home.s.root().join("claude-calls")).unwrap();
    assert!(record.trim_end().ends_with("Under 600 words. --output-format text"), "{record}");
    let input = std::fs::read_to_string(home.s.root().join("claude-calls.input")).unwrap();
    for part in ["# How I work", "# Last 14 days", "# site\n", "# engine\n", "# notes\n"] {
        assert!(input.contains(part), "{part} is not in what Claude was given");
    }
    for secret in secrets() {
        assert!(!input.contains(&secret), "{secret} was given to Claude");
    }
    assert!(tree(&context_dir(&home))["me.md"].contains("claude: 2.5.0 (Claude Code)"));

    // Nothing changed: not asked again.
    let (_, out, _) = home.s.devbox(&["context"]);
    assert_eq!(out, "context unchanged\n");
    assert_eq!(claude_calls(&home).len(), 1);

    // The facts change within the twenty hours: still not asked again.
    let site = home.s.repo("site");
    home.s.write(&site, "src/blog.ts", "export {};\n");
    commit(&mut home.s, &site, "2026-09-21T14:00:00Z", "feat: blog index");
    home.s.system.now += Duration::from_hours(1);
    let (_, out, _) = home.s.devbox(&["context"]);
    assert!(out.starts_with("context written to "), "{out}");
    assert_eq!(claude_calls(&home).len(), 1);
    assert_eq!(summary(&home).unwrap(), written);

    // Twenty hours on, with nothing new: no reason to ask.
    home.s.system.now += Duration::from_hours(20);
    let before = tree(&context_dir(&home));
    let _ = home.s.devbox(&["context"]);
    if tree(&context_dir(&home)) == before {
        assert_eq!(claude_calls(&home).len(), 1);
    }
    // And once the facts have changed again, a new summary.
    home.s.write(&site, "src/feed.ts", "export {};\n");
    commit(&mut home.s, &site, "2026-09-22T11:00:00Z", "feat: feed");
    claude(&home, &ANSWER.replace("navigation, unpushed", "the blog and its feed"));
    let _ = home.s.devbox(&["context"]);
    let refreshed = summary(&home).unwrap();
    assert!(
        refreshed.contains("_Generated 2026-09-22 by Claude")
            && refreshed.contains("the blog and its feed"),
        "{refreshed}"
    );
}

#[test]
fn the_summary_can_be_turned_off_and_a_poor_answer_is_not_one() {
    let home = home();
    claude(&home, ANSWER);
    let _ = home.s.devbox(&["context", "--no-ai"]);
    assert_eq!((claude_calls(&home).len(), summary(&home)), (0, None));

    let dir = dotfiles(&home);
    home.s.write(&dir, "devbox.toml", "version = 1\n[context]\ndir = \"context\"\nai = false\n");
    let _ = home.s.devbox(&["context"]);
    assert_eq!((claude_calls(&home).len(), summary(&home)), (0, None));

    // Asked, but what comes back is no summary: an error message, a refusal, nothing.
    home.s.write(&dir, "devbox.toml", "version = 1\n[context]\ndir = \"context\"\n");
    claude(&home, "Not logged in.");
    let (code, out, err) = home.s.devbox(&["context"]);
    assert_eq!((code, out.as_str(), err.as_str()), (ExitCode::SUCCESS, "context unchanged\n", ""));
    assert_eq!((claude_calls(&home).len(), summary(&home)), (1, None));
    home.s.program(
        "claude",
        "[ \"$1\" = --version ] && echo '2.5.0 (Claude Code)' && exit 0\ncat > /dev/null\nexit 1",
    );
    let (code, _, err) = home.s.devbox(&["context"]);
    assert_eq!((code, err.as_str(), summary(&home)), (ExitCode::SUCCESS, "", None));
}
