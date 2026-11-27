//! devbox's Rust crates are built to be embedded by moi, whose own `Host` is the machine as moi
//! sees it: files under a root prefix, programs it runs, a clock. This test is the proof that
//! `devbox_core::system::System` can be implemented on top of such a host by a thin shim, and
//! that `up`, `status` and `doctor` then behave exactly as they do through devbox's own systems:
//! the plan and the status are compared with the same golden files as in `setup.rs`.

use std::io;
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::time::{Duration, SystemTime};

use devbox_core::fake::FakeSystem;
use devbox_core::system::{Command, Entry, Host, HttpRequest, HttpResponse, Output, System};
use devbox_tests::sandbox::TempDir;

/// A host in moi's style. (moi's real `Host` has more; these are the conventions that matter.)
trait MoiHost: Sync {
    /// Every path the host is asked for is under this directory.
    fn root(&self) -> &Path;
    /// Runs a program and returns its exit code, output and errors.
    fn exec(&self, program: &str, args: &[String]) -> io::Result<(i32, String, String)>;
    fn clock(&self) -> SystemTime;
}

/// The shim: `System` on top of a `MoiHost`, with the Incus of the machine next to it.
struct OnMoi<'a, H: MoiHost> {
    host: &'a H,
    files: Host,
    incus: &'a FakeSystem,
}

impl<H: MoiHost> OnMoi<'_, H> {
    fn under_root(&self, path: &Path) -> PathBuf {
        self.host.root().join(path.strip_prefix("/").unwrap_or(path))
    }
}

impl<H: MoiHost> System for OnMoi<'_, H> {
    fn run(&self, command: &Command) -> io::Result<Output> {
        let (code, stdout, stderr) = self.host.exec(&command.program, &command.args)?;
        Ok(Output {
            code: Some(code),
            timed_out: false,
            stdout: stdout.into_bytes(),
            stderr: stderr.into_bytes(),
        })
    }
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        self.files.read(&self.under_root(path))
    }
    fn write(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        self.files.write(&self.under_root(path), data)
    }
    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        self.files.append(&self.under_root(path), data)
    }
    fn remove_file(&self, path: &Path) -> io::Result<()> {
        self.files.remove_file(&self.under_root(path))
    }
    fn create_dir_all(&self, path: &Path) -> io::Result<()> {
        self.files.create_dir_all(&self.under_root(path))
    }
    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()> {
        self.files.set_mode(&self.under_root(path), mode)
    }
    fn mode(&self, path: &Path) -> Option<u32> {
        self.files.mode(&self.under_root(path))
    }
    fn unix_http(&self, socket: &Path, request: &HttpRequest) -> io::Result<HttpResponse> {
        self.incus.unix_http(socket, request)
    }
    fn read_dir(&self, path: &Path) -> io::Result<Vec<Entry>> {
        self.files.read_dir(&self.under_root(path))
    }
    fn exists(&self, path: &Path) -> bool {
        self.files.exists(&self.under_root(path))
    }
    fn env(&self, name: &str) -> Option<String> {
        (name == "SUDO_USER").then(|| "dev".to_owned())
    }
    fn now(&self) -> SystemTime {
        self.host.clock()
    }
    fn sleep(&self, _: Duration) {}
    fn random(&self, buffer: &mut [u8]) -> io::Result<()> {
        self.incus.random(buffer)
    }
}

/// The fixture: a directory that is the host's root, and the programs a set-up server has.
struct Fixture {
    root: TempDir,
}

impl MoiHost for Fixture {
    fn root(&self) -> &Path {
        self.root.path()
    }
    fn exec(&self, program: &str, args: &[String]) -> io::Result<(i32, String, String)> {
        let last = args.last().map_or_default(String::as_str);
        let printed = |text: &str| Ok((0, text.to_owned(), String::new()));
        match program {
            "id" => printed("0\n"),
            "docker" if args.first().is_some_and(|arg| arg == "inspect") => printed("running\n"),
            "docker" => printed("29.1.0\n"),
            "chown" | "systemctl" => printed(""),
            "getent" => printed(&format!("203.0.113.7 {last}\n")),
            "sh" if args.get(1).is_some_and(|script| script.starts_with("getent passwd dev")) => {
                printed("/home/dev\n")
            }
            "curl" if last.ends_with("/__devbox/health") => {
                printed(r#"{"v":1,"status":"ok","problems":[]}"#)
            }
            "curl" if last.ends_with("/__devbox/status") => printed(
                r#"{"v":1,"version":"3f9c2d1","phase":"idle","vm":"running","lastStop":{"at":1789990000000,"reason":"idle","mode":"stateful","ms":4200}}"#,
            ),
            "curl" if last.ends_with("/releases/latest") => printed(r#"{"tag_name": "v1.2.3"}"#),
            _ => {
                Err(io::Error::new(io::ErrorKind::NotFound, format!("{program}: not on this host")))
            }
        }
    }
    fn clock(&self) -> SystemTime {
        SystemTime::UNIX_EPOCH + Duration::from_secs(1_790_000_000)
    }
}

fn devbox(system: &dyn System, args: &[&str]) -> (ExitCode, String, String) {
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = devbox_cli::run_on(
        system,
        std::iter::once("devbox").chain(args.iter().copied()),
        &mut out,
        &mut err,
    );
    (code, String::from_utf8(out).unwrap(), String::from_utf8(err).unwrap())
}

#[test]
fn up_status_and_doctor_work_through_a_moi_style_host() {
    let fixture = Fixture { root: TempDir::new() };
    let put = |path: &str, content: &str| {
        let path = fixture.root().join(path);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, content).unwrap();
    };
    put("etc/dokploy/traefik/dynamic/dokploy.yml", "http: {}\n");
    put("home/dev/.ssh/authorized_keys", "ssh-ed25519 FAKEkeyFAKEkeyFAKEkey dev@laptop\n");
    put("run/secrets/wake", "FAKE-wake-password\n");
    put("run/secrets/app", "FAKE-app-password\n");
    let incus = FakeSystem::new();
    incus.incus(|incus| {
        incus.inside = Some(Box::new(|command: &[String]| {
            let script = command.get(2).map_or_default(String::as_str);
            if script.starts_with("id -u") {
                (0, "1000\n1000\n/home/dev\n".to_owned(), String::new())
            } else {
                (0, String::new(), String::new())
            }
        }));
    });
    let system = OnMoi { host: &fixture, files: Host::new(), incus: &incus };

    // The plan is the plan devbox's own fake machine gets for the same host.
    let (code, plan, err) = devbox(&system, &["up", "--domain", "example.com", "--dry-run"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert_eq!(plan, devbox_tests::read("tests/fixtures/setup/plan-fresh.txt"));
    assert!(!fixture.root().join("etc/devbox").exists(), "a dry run wrote under the root");

    // Applying it writes under the root prefix, and nowhere else.
    let args = [
        "up",
        "--domain",
        "example.com",
        "--wake-password-file",
        "/run/secrets/wake",
        "--app-password-file",
        "/run/secrets/app",
        "--yes",
    ];
    let (code, out, err) = devbox(&system, &args);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""), "{out}");
    let written = |path: &str| std::fs::read_to_string(fixture.root().join(path)).unwrap();
    assert_eq!(
        written("etc/devbox/devbox.toml"),
        devbox_tests::read("tests/fixtures/setup/devbox.toml")
    );
    assert_eq!(
        written("etc/dokploy/traefik/dynamic/devbox.yml"),
        devbox_tests::read("tests/fixtures/setup/traefik-devbox.yml")
    );
    assert_eq!(system.mode(Path::new("/etc/devbox/secrets.toml")), Some(0o600));
    assert!(fixture.root().join("var/lib/devbox/provisioned.json").exists());

    let (code, status, _) = devbox(&system, &["status"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert_eq!(status, devbox_tests::read("tests/fixtures/setup/status.txt"));
    let (code, checks, _) = devbox(&system, &["doctor", "--json"]);
    let report: serde_json::Value = serde_json::from_str(&checks).unwrap();
    let failed: Vec<&str> = report["checks"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|check| check["result"] == "fail")
        .map(|check| check["id"].as_str().unwrap())
        .collect();
    // The fixture's VM answers nothing about its services; the host side is all in order.
    assert!(
        failed.iter().all(|id| ![
            "incus",
            "config",
            "secrets",
            "instance",
            "stateful",
            "autostart",
            "state-size",
            "gateway",
            "routing",
            "health"
        ]
        .contains(id)),
        "{failed:?}"
    );
    assert_eq!(code, ExitCode::from(1));

    let (code, down, _) = devbox(&system, &["down", "--yes"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert!(
        down.starts_with("removed /etc/devbox/secrets.toml\nremoved /etc/devbox/devbox.toml\n"),
        "{down}"
    );
    assert!(!fixture.root().join("etc/devbox/devbox.toml").exists());
}
