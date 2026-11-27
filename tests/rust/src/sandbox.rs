//! A place to run `devbox sync` against real git without touching the machine: a temporary home
//! with a code directory, bare repositories that play GitHub, and a `System` that sees only them.

// Test helpers: they are called for what they do as often as for what they return, and a helper
// that cannot do its job panics, which fails the test that called it.
#![allow(clippy::must_use_candidate, clippy::missing_panics_doc)]

use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime};

use devbox_core::system::{self, Entry, Host, HttpRequest, HttpResponse, Output, System};
use devbox_sync::{Event, Options, RepoResult};

/// A directory that is removed when dropped.
pub struct TempDir(PathBuf);

impl TempDir {
    /// A new, empty directory.
    ///
    /// # Panics
    ///
    /// When it cannot be created.
    #[must_use]
    pub fn new() -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let nanos = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .map_or(0, |since| since.subsec_nanos());
        let name = format!(
            "devbox-test-{}-{}-{nanos}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        );
        let path = std::env::temp_dir().join(name);
        std::fs::create_dir_all(&path).unwrap_or_else(|err| panic!("{}: {err}", path.display()));
        // Paths are compared with what git prints, which has links resolved.
        Self(path.canonicalize().unwrap_or(path))
    }

    /// Where it is.
    #[must_use]
    pub fn path(&self) -> &Path {
        &self.0
    }
}

impl Default for TempDir {
    fn default() -> Self {
        Self::new()
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// The real machine at a time of the test's choosing, with its files confined: a path outside
/// `root` is looked for under `root/rootfs`, so `/etc/os-release` or `/proc/meminfo` are what
/// the test put there and never what the machine running the test has.
pub struct At {
    /// The machine.
    pub host: Host,
    /// What its clock says.
    pub now: SystemTime,
    /// The directory files are confined to.
    pub root: PathBuf,
}

impl At {
    fn confined(&self, path: &Path) -> PathBuf {
        if path.starts_with(&self.root) {
            path.to_owned()
        } else {
            self.root.join("rootfs").join(path.strip_prefix("/").unwrap_or(path))
        }
    }
}

impl System for At {
    fn run(&self, command: &system::Command) -> io::Result<Output> {
        self.host.run(command)
    }
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        self.host.read(&self.confined(path))
    }
    fn write(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        self.host.write(&self.confined(path), data)
    }
    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        self.host.append(&self.confined(path), data)
    }
    fn remove_file(&self, path: &Path) -> io::Result<()> {
        self.host.remove_file(&self.confined(path))
    }
    fn create_dir_all(&self, path: &Path) -> io::Result<()> {
        self.host.create_dir_all(&self.confined(path))
    }
    fn read_dir(&self, path: &Path) -> io::Result<Vec<Entry>> {
        self.host.read_dir(&self.confined(path))
    }
    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()> {
        self.host.set_mode(&self.confined(path), mode)
    }
    fn mode(&self, path: &Path) -> Option<u32> {
        self.host.mode(&self.confined(path))
    }
    fn unix_http(&self, socket: &Path, request: &HttpRequest) -> io::Result<HttpResponse> {
        self.host.unix_http(socket, request)
    }
    fn exists(&self, path: &Path) -> bool {
        self.host.exists(&self.confined(path))
    }
    fn env(&self, name: &str) -> Option<String> {
        self.host.env(name)
    }
    fn now(&self) -> SystemTime {
        self.now
    }
    fn sleep(&self, duration: Duration) {
        self.host.sleep(duration);
    }
    fn random(&self, buffer: &mut [u8]) -> io::Result<()> {
        self.host.random(buffer)
    }
}

/// Everything about a repository that a sync must not lose: where HEAD is, what is staged, every
/// file of the working tree with its mode, what is untracked, what is ignored, and the stashes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Snapshot {
    /// The commit HEAD is at and the branch it is on.
    pub head: String,
    /// The index: mode, blob and path of every entry.
    pub index: String,
    /// Every file outside `.git`: path → (executable, content; a link's target for links).
    pub files: BTreeMap<String, (bool, Vec<u8>)>,
    /// Paths git reports as untracked.
    pub untracked: String,
    /// The stash list, as commits.
    pub stashes: String,
}

fn walk(root: &Path, dir: &Path, files: &mut BTreeMap<String, (bool, Vec<u8>)>) {
    use std::os::unix::fs::PermissionsExt;
    let entries = std::fs::read_dir(dir).unwrap_or_else(|err| panic!("{}: {err}", dir.display()));
    for entry in entries.flatten() {
        let path = entry.path();
        let relative = path.strip_prefix(root).unwrap_or(&path).to_string_lossy().into_owned();
        if relative == ".git" {
            continue;
        }
        let meta = std::fs::symlink_metadata(&path)
            .unwrap_or_else(|err| panic!("{}: {err}", path.display()));
        if meta.file_type().is_symlink() {
            let target = std::fs::read_link(&path).unwrap_or_default();
            files.insert(relative, (false, target.to_string_lossy().into_owned().into_bytes()));
        } else if meta.is_dir() {
            walk(root, &path, files);
        } else {
            let content = std::fs::read(&path).unwrap_or_default();
            files.insert(relative, (meta.permissions().mode() & 0o100 != 0, content));
        }
    }
}

/// A sandbox for sync tests.
pub struct Sandbox {
    root: TempDir,
    /// The machine as the sync sees it.
    pub system: At,
}

impl Sandbox {
    /// A home with git configured, an empty code directory, and no way to reach GitHub: `gh`
    /// is one that knows no account, and the API address is one nothing listens on.
    ///
    /// # Panics
    ///
    /// When the sandbox cannot be set up.
    #[must_use]
    pub fn new() -> Self {
        let root = TempDir::new();
        let home = root.path().join("home");
        let bin = root.path().join("bin");
        for dir in [&home.join("code"), &bin, &root.path().join("remotes")] {
            std::fs::create_dir_all(dir).unwrap_or_else(|err| panic!("{}: {err}", dir.display()));
        }
        let write = |path: PathBuf, text: &str| {
            std::fs::write(&path, text).unwrap_or_else(|err| panic!("{}: {err}", path.display()));
            path
        };
        write(
            home.join(".gitconfig"),
            "[user]\n\tname = Dev\n\temail = dev@example.com\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n[protocol \"file\"]\n\tallow = always\n[advice]\n\tdetachedHead = false\n",
        );
        let gh = write(bin.join("gh"), "#!/bin/sh\nexit 1\n");
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&gh, std::fs::Permissions::from_mode(0o755))
                .unwrap_or_else(|err| panic!("{}: {err}", gh.display()));
        }
        let path = format!("{}:{}", bin.display(), std::env::var("PATH").unwrap_or_default());
        let mut host = Host::new()
            .with_env("HOME", &home.to_string_lossy())
            .with_env("PATH", &path)
            .with_env("GIT_CONFIG_NOSYSTEM", "1")
            .with_env("GIT_CONFIG_GLOBAL", &home.join(".gitconfig").to_string_lossy())
            .with_env("XDG_CONFIG_HOME", &home.join(".config").to_string_lossy())
            .with_env("XDG_STATE_HOME", "")
            .with_env("DEVBOX_GITHUB_API", "http://127.0.0.1:1");
        for unset in ["GITHUB_TOKEN", "GH_TOKEN", "DEVBOX_CODE_DIR", "DEVBOX_STATE_DIR"] {
            host = host.with_env(unset, "");
        }
        // Commits made by a test are the same commits on every run.
        for (name, value) in [
            ("GIT_AUTHOR_DATE", "2026-01-01T00:00:00Z"),
            ("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z"),
        ] {
            host = host.with_env(name, value);
        }
        let now = SystemTime::UNIX_EPOCH + Duration::from_secs(1_790_000_000);
        let confined = root.path().to_owned();
        Self { root, system: At { host, now, root: confined } }
    }

    /// Sets a variable of the environment the sync and git see.
    pub fn set_env(&mut self, name: &str, value: &str) {
        self.system.host = std::mem::take(&mut self.system.host).with_env(name, value);
    }

    /// Makes `gh` know an account with this token.
    pub fn gh_token(&self, token: &str) {
        let gh = self.root.path().join("bin/gh");
        let script = format!("#!/bin/sh\n[ \"$1 $2\" = \"auth token\" ] && echo {token}\n");
        std::fs::write(&gh, script).unwrap_or_else(|err| panic!("{}: {err}", gh.display()));
    }

    /// `text` with the sandbox's own directory replaced by `<sandbox>`, so it reads the same on
    /// every run.
    #[must_use]
    pub fn scrub(&self, text: &str) -> String {
        text.replace(&*self.root.path().to_string_lossy(), "<sandbox>")
    }

    /// The checkout through which a repository's upstream is changed.
    #[must_use]
    pub fn seed(&self, name: &str) -> PathBuf {
        self.root.path().join("seed").join(name)
    }

    /// Where the sandbox is.
    #[must_use]
    pub fn root(&self) -> &Path {
        self.root.path()
    }

    /// Limits the programs the sandbox can run to `tools` (found on the real `PATH`) and what
    /// the sandbox's own `bin` holds, so that what happens to be installed on the machine
    /// running the test — a `node`, a `claude` — is not there unless the test puts it there.
    pub fn only_tools(&mut self, tools: &[&str]) {
        let bin = self.root.path().join("bin");
        let path = std::env::var("PATH").unwrap_or_default();
        for tool in tools {
            let found = std::env::split_paths(&path)
                .map(|dir| dir.join(tool))
                .find(|candidate| candidate.is_file())
                .unwrap_or_else(|| panic!("{tool} is not on PATH"));
            // A script that runs the real one, not a link to it: some programs look for their
            // own files next to the path they were started by.
            self.program(tool, &format!("exec '{}' \"$@\"", found.display()));
        }
        self.set_env("PATH", &bin.to_string_lossy());
    }

    /// Puts a program into the sandbox's `bin`: a shell script with this body.
    pub fn program(&self, name: &str, body: &str) {
        use std::os::unix::fs::PermissionsExt;
        let path = self.root.path().join("bin").join(name);
        let _ = std::fs::remove_file(&path);
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n"))
            .unwrap_or_else(|err| panic!("{}: {err}", path.display()));
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755))
            .unwrap_or_else(|err| panic!("{}: {err}", path.display()));
    }

    /// The home directory.
    #[must_use]
    pub fn home(&self) -> PathBuf {
        self.root.path().join("home")
    }

    /// The code directory.
    #[must_use]
    pub fn code(&self) -> PathBuf {
        self.home().join("code")
    }

    /// The checkout of a repository in the code directory.
    #[must_use]
    pub fn repo(&self, name: &str) -> PathBuf {
        self.code().join(name)
    }

    /// The bare repository that is a repository's upstream.
    #[must_use]
    pub fn remote(&self, name: &str) -> PathBuf {
        self.root.path().join("remotes").join(format!("{name}.git"))
    }

    /// Runs git in `dir` with the sandbox's configuration and returns what it printed.
    ///
    /// # Panics
    ///
    /// When git fails.
    pub fn git(&self, dir: &Path, args: &[&str]) -> String {
        let (ok, out) = self.try_git(dir, args);
        assert!(ok, "git {args:?} in {}: {out}", dir.display());
        out
    }

    /// Runs git in `dir` and returns whether it succeeded and what it printed.
    ///
    /// # Panics
    ///
    /// When git cannot be started.
    pub fn try_git(&self, dir: &Path, args: &[&str]) -> (bool, String) {
        let output = self
            .system
            .run(&system::Command::new("git", args.iter().copied()).in_dir(dir))
            .unwrap_or_else(|err| panic!("git: {err}"));
        // Only the end is trimmed: `git status` puts meaning in a leading space.
        let mut text = String::from_utf8_lossy(&output.stdout).trim_end().to_owned();
        if !output.success() {
            text.push_str(&output.error_text());
        }
        (output.success(), text)
    }

    /// Writes a file under `dir`, creating its directories.
    ///
    /// # Panics
    ///
    /// When it cannot be written.
    pub fn write(&self, dir: &Path, file: &str, content: impl AsRef<[u8]>) {
        let path = dir.join(file);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)
                .unwrap_or_else(|err| panic!("{}: {err}", parent.display()));
        }
        std::fs::write(&path, content).unwrap_or_else(|err| panic!("{}: {err}", path.display()));
    }

    /// Creates an upstream with one commit (`README.md` and `src/lib.rs` of five lines) on
    /// `main`, without a checkout in the code directory.
    pub fn upstream(&self, name: &str) {
        let remote = self.remote(name);
        self.git(self.root.path(), &["init", "--quiet", "--bare", &remote.to_string_lossy()]);
        let seed = self.seed(name);
        self.git(
            self.root.path(),
            &["clone", "--quiet", &remote.to_string_lossy(), &seed.to_string_lossy()],
        );
        self.write(&seed, "README.md", format!("# {name}\n"));
        self.write(&seed, "src/lib.rs", "one\ntwo\nthree\nfour\nfive\n");
        self.git(&seed, &["add", "."]);
        self.git(&seed, &["commit", "--quiet", "-m", "first"]);
        self.git(&seed, &["push", "--quiet", "origin", "main"]);
    }

    /// Creates an upstream and its checkout in the code directory, and returns the checkout.
    pub fn repository(&self, name: &str) -> PathBuf {
        self.upstream(name);
        let dir = self.repo(name);
        self.git(&self.code(), &["clone", "--quiet", &self.remote(name).to_string_lossy(), name]);
        dir
    }

    /// Commits `content` as `file` on `branch` of a repository's upstream, as someone else
    /// pushing would.
    pub fn push_upstream(&self, name: &str, branch: &str, file: &str, content: impl AsRef<[u8]>) {
        let seed = self.seed(name);
        if !self.try_git(&seed, &["checkout", "--quiet", branch]).0 {
            self.git(&seed, &["checkout", "--quiet", "-b", branch]);
        }
        self.write(&seed, file, content);
        self.git(&seed, &["add", "."]);
        self.git(&seed, &["commit", "--quiet", "-m", &format!("upstream: {file}")]);
        self.git(&seed, &["push", "--quiet", "origin", branch]);
    }

    /// Everything about a checkout that a sync must not lose.
    #[must_use]
    pub fn snapshot(&self, dir: &Path) -> Snapshot {
        let mut files = BTreeMap::new();
        walk(dir, dir, &mut files);
        Snapshot {
            head: format!(
                "{} {}",
                self.try_git(dir, &["rev-parse", "HEAD"]).1,
                self.try_git(dir, &["symbolic-ref", "--quiet", "HEAD"]).1
            ),
            index: self.git(dir, &["ls-files", "--stage"]),
            files,
            untracked: self.git(dir, &["ls-files", "--others", "--exclude-standard"]),
            stashes: self.git(dir, &["stash", "list", "--format=%H"]),
        }
    }

    /// The options a sync in this sandbox runs with.
    #[must_use]
    pub fn options(&self) -> Options {
        Options::from_env(&self.system)
    }

    /// Runs git in `dir` as if it were `date` (RFC 3339), for commits with a known time.
    pub fn git_on(&mut self, date: &str, dir: &Path, args: &[&str]) -> String {
        let usual = self.system.host.env("GIT_AUTHOR_DATE").unwrap_or_default();
        for name in ["GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE"] {
            self.set_env(name, date);
        }
        let out = self.git(dir, args);
        for name in ["GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE"] {
            self.set_env(name, &usual);
        }
        out
    }

    /// Runs a sync and returns its results and every event it reported.
    ///
    /// # Panics
    ///
    /// When the sync fails as a whole.
    pub fn sync_events(&self, options: &Options) -> (Vec<RepoResult>, Vec<Event>) {
        let mut events = Vec::new();
        let results =
            devbox_sync::sync(&self.system, options, &mut |event| events.push(event.clone()))
                .unwrap_or_else(|err| panic!("sync: {err}"));
        (results, events)
    }

    /// Runs a sync and returns its results.
    pub fn sync(&self) -> Vec<RepoResult> {
        self.sync_events(&self.options()).0
    }

    /// Runs a sync and returns the result for one repository.
    ///
    /// # Panics
    ///
    /// When there is no result for it.
    pub fn sync_one(&self, name: &str) -> RepoResult {
        self.sync()
            .into_iter()
            .find(|result| result.name == name)
            .unwrap_or_else(|| panic!("no result for {name}"))
    }

    /// Runs `devbox` with `args` in this sandbox and returns its exit code, output and errors.
    pub fn devbox(&self, args: &[&str]) -> (std::process::ExitCode, String, String) {
        let (mut out, mut err) = (Vec::new(), Vec::new());
        let code = devbox_cli::run_on(
            &self.system,
            std::iter::once("devbox").chain(args.iter().copied()),
            &mut out,
            &mut err,
        );
        (
            code,
            String::from_utf8_lossy(&out).into_owned(),
            String::from_utf8_lossy(&err).into_owned(),
        )
    }
}

impl Default for Sandbox {
    fn default() -> Self {
        Self::new()
    }
}

/// Whether `git` can be run at all; without it the sync tests cannot say anything.
#[must_use]
pub fn git_available() -> bool {
    Command::new("git").arg("--version").output().is_ok_and(|output| output.status.success())
}
