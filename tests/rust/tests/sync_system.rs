//! `devbox sync` on a machine that exists only in the test: what it runs, with what, and what it
//! keeps to itself. Also the real `Host`, for the few things the fake cannot say.

use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::{Duration, Instant, SystemTime};

use devbox_core::system::{Command, Entry, Host, HttpRequest, HttpResponse, Output, System};
use devbox_sync::{Options, SyncStatus};

const TOKEN: &str = "FAKE-github-token";
const HOST_KEY: &str = "github.com ssh-ed25519 FAKEkeyFAKEkeyFAKEkey\n";

/// A machine made of a map of files and answers to the programs a sync runs.
#[derive(Default)]
struct Machine {
    files: Mutex<BTreeMap<PathBuf, Vec<u8>>>,
    ran: Mutex<Vec<Command>>,
    env: BTreeMap<&'static str, &'static str>,
    /// Whether `git clone` over SSH works.
    ssh_works: bool,
}

impl Machine {
    fn ran(&self, program: &str) -> Vec<Command> {
        self.ran.lock().unwrap().iter().filter(|c| c.program == program).cloned().collect()
    }
    fn file(&self, path: &str) -> Option<String> {
        let files = self.files.lock().unwrap();
        files.get(Path::new(path)).map(|data| String::from_utf8_lossy(data).into_owned())
    }
}

fn ok(stdout: &str) -> Output {
    Output { code: Some(0), stdout: stdout.as_bytes().to_vec(), ..Output::default() }
}

fn failed(stderr: &str) -> Output {
    Output { code: Some(128), stderr: stderr.as_bytes().to_vec(), ..Output::default() }
}

impl System for Machine {
    fn run(&self, command: &Command) -> io::Result<Output> {
        self.ran.lock().unwrap().push(command.clone());
        let args: Vec<&str> = command.args.iter().map(String::as_str).collect();
        Ok(match (command.program.as_str(), args.as_slice()) {
            ("gh", _) => return Err(io::Error::new(io::ErrorKind::NotFound, "no gh here")),
            ("curl", _) => ok(r#"[{"name": "site", "owner": {"login": "dev"}, "archived": false,
                     "ssh_url": "git@github.com:dev/site.git", "clone_url": "https://github.com/dev/site.git"}]"#),
            ("ssh-keyscan", _) => ok(HOST_KEY),
            ("git", ["clone", "--quiet", url, _]) if url.starts_with("git@") && !self.ssh_works => {
                failed(
                    "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
                )
            }
            ("git", ["clone", ..]) => ok(""),
            other => panic!("unexpected command: {other:?}"),
        })
    }
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        let files = self.files.lock().unwrap();
        files.get(path).cloned().ok_or_else(|| io::Error::from(io::ErrorKind::NotFound))
    }
    fn write(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        self.files.lock().unwrap().insert(path.to_owned(), data.to_vec());
        Ok(())
    }
    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        self.files.lock().unwrap().entry(path.to_owned()).or_default().extend_from_slice(data);
        Ok(())
    }
    fn remove_file(&self, path: &Path) -> io::Result<()> {
        self.files.lock().unwrap().remove(path);
        Ok(())
    }
    fn create_dir_all(&self, _: &Path) -> io::Result<()> {
        Ok(())
    }
    fn read_dir(&self, _: &Path) -> io::Result<Vec<Entry>> {
        Ok(Vec::new())
    }
    fn set_mode(&self, _: &Path, _: u32) -> io::Result<()> {
        Ok(())
    }
    fn mode(&self, _: &Path) -> Option<u32> {
        None
    }
    fn unix_http(&self, _: &Path, _: &HttpRequest) -> io::Result<HttpResponse> {
        Err(io::Error::from(io::ErrorKind::NotFound))
    }
    fn exists(&self, path: &Path) -> bool {
        self.files.lock().unwrap().contains_key(path)
    }
    fn env(&self, name: &str) -> Option<String> {
        self.env.get(name).map(|value| (*value).to_owned())
    }
    fn now(&self) -> SystemTime {
        SystemTime::UNIX_EPOCH + Duration::from_secs(1_790_000_000)
    }
}

fn machine(ssh_works: bool) -> Machine {
    Machine {
        env: BTreeMap::from([("HOME", "/home/dev"), ("GITHUB_TOKEN", TOKEN)]),
        ssh_works,
        ..Machine::default()
    }
}

#[test]
fn the_default_places_are_under_home() {
    let options = Options::from_env(&machine(true));
    assert_eq!(options.home, Path::new("/home/dev"));
    assert_eq!(options.code_dir, Path::new("/home/dev/code"));
    assert_eq!(options.state_dir, Path::new("/home/dev/.local/state/devbox"));
    assert_eq!(options.github_api, "https://api.github.com");
    assert_eq!(options.only, None);
}

#[test]
fn githubs_host_key_is_learned_once_before_the_first_clone_over_ssh() {
    let m = machine(true);
    let results = devbox_sync::sync(&m, &Options::from_env(&m), &mut |_| {}).unwrap();
    assert_eq!(results[0].status, SyncStatus::Cloned);
    assert_eq!(m.ran("ssh-keyscan")[0].args, ["-t", "ed25519", "github.com"]);
    assert_eq!(m.file("/home/dev/.ssh/known_hosts").as_deref(), Some(HOST_KEY));
    let clones = m.ran("git");
    assert_eq!(clones.len(), 1);
    assert_eq!(
        clones[0].args,
        ["clone", "--quiet", "git@github.com:dev/site.git", "/home/dev/code/site"]
    );

    // Known already: not asked again.
    let _ = devbox_sync::sync(&m, &Options::from_env(&m), &mut |_| {}).unwrap();
    assert_eq!(m.ran("ssh-keyscan").len(), 1);
    assert_eq!(m.file("/home/dev/.ssh/known_hosts").as_deref(), Some(HOST_KEY));
}

#[test]
fn git_can_never_stop_to_ask_and_the_token_is_never_on_a_command_line() {
    let m = machine(false);
    let results = devbox_sync::sync(&m, &Options::from_env(&m), &mut |_| {}).unwrap();
    assert_eq!(results[0].status, SyncStatus::Cloned, "{}", results[0].detail);

    let everything = m.ran.lock().unwrap().clone();
    for command in &everything {
        for arg in &command.args {
            assert!(!arg.contains(TOKEN), "{} {:?}", command.program, command.args);
        }
    }
    // curl reads it from its standard input, with a deadline.
    let curl = &m.ran("curl")[0];
    assert_eq!(
        curl.args.last().unwrap(),
        "https://api.github.com/user/repos?affiliation=owner&per_page=100&page=1"
    );
    assert!(curl.args.windows(2).any(|pair| pair == ["--config", "-"]));
    let config = String::from_utf8(curl.stdin.clone().unwrap()).unwrap();
    assert!(config.contains(&format!("header = \"Authorization: Bearer {TOKEN}\"")), "{config}");
    assert!(curl.timeout.is_some());

    // SSH failed, so HTTPS was tried, with the token in that one git's environment only.
    let clones = m.ran("git");
    assert_eq!(clones.len(), 2);
    assert_eq!(clones[1].args[2], "https://github.com/dev/site.git");
    let env: BTreeMap<&str, &str> =
        clones[1].env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    assert_eq!(env["GIT_CONFIG_KEY_0"], "http.extraHeader");
    assert_eq!(
        env["GIT_CONFIG_VALUE_0"],
        "Authorization: Basic eC1hY2Nlc3MtdG9rZW46RkFLRS1naXRodWItdG9rZW4="
    );
    for clone in &clones {
        let env: BTreeMap<&str, &str> =
            clone.env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
        assert_eq!(env["GIT_TERMINAL_PROMPT"], "0");
        assert!(env["GIT_SSH_COMMAND"].contains("BatchMode=yes"));
        assert!(clone.timeout.is_some());
    }
    assert!(clones[0].env.iter().all(|(name, _)| !name.starts_with("GIT_CONFIG_")));
}

#[test]
fn the_real_host_runs_programs_with_input_environment_and_a_deadline() {
    let host = Host::new().with_env("DEVBOX_TEST_VALUE", "from the host");
    assert_eq!(host.env("DEVBOX_TEST_VALUE").as_deref(), Some("from the host"));

    let shell = |script: &str| Command::new("sh", ["-c", script]);
    let output = host
        .run(
            &shell("cat; echo \"$DEVBOX_TEST_VALUE/$OWN\"; echo oops >&2; exit 3")
                .stdin("typed in\n")
                .env("OWN", "from the command"),
        )
        .unwrap();
    assert_eq!(output.code, Some(3));
    assert!(!output.success() && !output.timed_out);
    assert_eq!(output.text(), "typed in\nfrom the host/from the command");
    assert_eq!(output.error_text(), "oops");

    let dir = std::env::temp_dir();
    let here = host.run(&shell("pwd -P").in_dir(&dir)).unwrap().text();
    assert_eq!(Path::new(&here), dir.canonicalize().unwrap());

    // A program that runs past its deadline is killed, and said to have been.
    let began = Instant::now();
    let slow = host.run(&shell("exec sleep 30").timeout(Duration::from_millis(200))).unwrap();
    assert!(slow.timed_out && !slow.success());
    assert!(began.elapsed() < Duration::from_secs(10), "{:?}", began.elapsed());
    // One that finishes in time is not.
    let quick = host.run(&shell("echo done").timeout(Duration::from_secs(30))).unwrap();
    assert!(quick.success() && !quick.timed_out);

    assert!(host.run(&Command::new("devbox-no-such-program", [""; 0])).is_err());
}
