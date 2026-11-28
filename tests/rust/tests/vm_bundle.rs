//! The scripts `devbox up` runs inside the VM, run for real: `install.sh` and `dotfiles.sh`
//! from the bundle, in a temporary home, with the programs that would change a machine (apt,
//! systemctl, sudo, …) replaced by stand-ins that write down how they were called.

use std::path::{Path, PathBuf};
use std::process::Command;

use devbox_tests::sandbox::TempDir;

const BUNDLE: &str = "crates/devbox-core/vm";

/// A pretend VM: a home for the user `dev`, a copy of the bundle, and stand-ins on `PATH`.
struct Vm {
    dir: TempDir,
}

impl Vm {
    fn new() -> Self {
        let dir = TempDir::new();
        let vm = Self { dir };
        for path in ["home", "bundle", "stubs", "root/etc/sudoers.d", "root/usr/local/sbin"] {
            std::fs::create_dir_all(vm.path(path)).unwrap();
        }
        copy_dir(&devbox_tests::repo(BUNDLE), &vm.path("bundle"));
        // sudo -u dev -H env VAR=… PATH=… program…: run the program, as the "user", with the
        // stand-ins still in front.
        vm.stub("sudo", "shift 3\n[ \"$1\" = env ] && shift\nwhile case \"$1\" in *=*) true ;; *) false ;; esac; do\n  case \"$1\" in PATH=*) export PATH=\"$STUBS:${1#PATH=}:$REAL_PATH\" ;; *) export \"$1\" ;; esac\n  shift\ndone\nexport HOME=\"$FAKE_HOME\"\nexec \"$@\"");
        vm.stub("getent", "echo \"dev:x:1000:1000::$FAKE_HOME:/bin/bash\"");
        vm.stub("id", "echo 1000");
        vm.stub("dpkg", "echo amd64");
        for logged in ["apt-get", "systemctl", "loginctl", "npm", "gh", "code-server"] {
            vm.stub(logged, &format!("echo \"{logged} $*\" >> \"$CALLS\"\n[ \"{logged} $1\" != 'gh auth' ] || cat > /dev/null"));
        }
        // An extension that is not on Open VSX cannot be installed, which must not stop anything.
        vm.stub(
            "code-server",
            "echo \"code-server $*\" >> \"$CALLS\"\n[ \"$2\" != not.on-open-vsx ]",
        );
        vm.stub("claude", "true");
        vm
    }

    fn path(&self, relative: &str) -> PathBuf {
        self.dir.path().join(relative)
    }

    fn home(&self, relative: &str) -> PathBuf {
        self.path("home").join(relative)
    }

    fn stub(&self, name: &str, body: &str) {
        use std::os::unix::fs::PermissionsExt;
        let path = self.path("stubs").join(name);
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
    }

    fn write(&self, relative: &str, content: &str) {
        let path = self.path(relative);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, content).unwrap();
    }

    /// Runs a script of the bundle as root would; returns whether it succeeded and what it printed.
    fn run(&self, script: &str) -> (bool, String) {
        let real_path = std::env::var("PATH").unwrap();
        let output = Command::new("bash")
            .arg(self.path("bundle").join(script))
            .env("PATH", format!("{}:{real_path}", self.path("stubs").display()))
            .env("REAL_PATH", &real_path)
            .env("STUBS", self.path("stubs"))
            .env("FAKE_HOME", self.path("home"))
            .env("CALLS", self.path("calls"))
            .env("DEVBOX_ROOT", self.path("root"))
            .env("GIT_CONFIG_GLOBAL", self.home(".gitconfig"))
            .env("GIT_CONFIG_NOSYSTEM", "1")
            .output()
            .unwrap();
        let text = format!(
            "{}{}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        (output.status.success(), text)
    }

    fn calls(&self) -> String {
        std::fs::read_to_string(self.path("calls")).unwrap_or_default()
    }

    fn read(&self, relative: &str) -> String {
        let path = self.home(relative);
        std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("{}: {err}", path.display()))
    }
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap().flatten() {
        let target = to.join(entry.file_name());
        if entry.path().is_dir() {
            copy_dir(&entry.path(), &target);
        } else {
            std::fs::copy(entry.path(), target).unwrap();
        }
    }
}

fn mode(path: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(path).unwrap().permissions().mode() & 0o777
}

fn provisioned() -> Vm {
    let vm = Vm::new();
    vm.write("bundle/env", "DEVBOX_USER='dev'\nCODE_PORT=8080\nAGENTS='claude codex'\nRELEASE='local-abc'\nREPO='example/devbox'\n");
    vm.write(
        "bundle/hub.toml",
        "version = 1\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n",
    );
    vm.write("bundle/hub.secrets.toml", "version = 1\napp_password_hash = 'FAKE-hash'\ncookie_secret = \"FAKE-cookie\"\nhub_token = \"FAKE-token\"\n");
    vm.write("bundle/bin/devbox-hub", "#!/bin/sh\necho \"devbox-hub $*\" >> \"$CALLS\"\n");
    vm.write("bundle/bin/devbox", "#!/bin/sh\n");
    vm.write("bundle/github-token", "FAKE-github-token\n");
    vm
}

#[test]
fn the_installer_puts_everything_in_its_place_and_can_be_run_again() {
    let vm = provisioned();
    let (ok, out) = vm.run("install.sh");
    assert!(ok, "{out}");
    assert!(out.ends_with("== services\ndone\n"), "{out}");

    // The binaries of this release, the names people type, and the config.
    assert_eq!(mode(&vm.home(".local/lib/devbox/releases/local-abc/devbox-hub")), 0o755);
    assert_eq!(
        std::fs::read_link(vm.home(".local/lib/devbox/current")).unwrap(),
        Path::new("releases/local-abc")
    );
    assert!(vm.home(".local/bin/devbox").exists() && vm.home(".local/bin/devbox-hub").exists());
    assert_eq!(vm.read(".local/bin/agent"), "#!/bin/sh\nexec devbox-hub agent \"$@\"\n");
    assert!(vm.read(".config/devbox/hub.toml").contains("code.example.com"));
    assert_eq!(mode(&vm.home(".config/devbox/hub.secrets.toml")), 0o600);
    assert!(
        !vm.path("bundle/hub.secrets.toml").exists() && !vm.path("bundle/github-token").exists(),
        "secrets are not left in the bundle"
    );

    // code-server: bound for the gateway, with a password of its own behind it.
    let config = vm.read(".config/code-server/config.yaml");
    assert!(
        config.starts_with("bind-addr: 0.0.0.0:8080\nauth: password\npassword: ")
            && config.ends_with("\ncert: false\n"),
        "{config}"
    );
    assert_eq!(mode(&vm.home(".config/code-server/config.yaml")), 0o600);
    assert!(
        vm.read(".local/share/code-server/User/settings.json").contains("telemetry.telemetryLevel")
    );

    // Units, hooks, instructions, the one thing root lets the hub do.
    for unit in [
        "devbox-tmux.service",
        "devbox-hub.service",
        "devbox-context.service",
        "devbox-context.timer",
    ] {
        assert!(vm.home(".config/systemd/user").join(unit).exists(), "{unit}");
    }
    assert_eq!(vm.read(".claude/CLAUDE.md"), "@~/.config/devbox/AGENTS.md\n");
    assert_eq!(
        std::fs::read_link(vm.home(".codex/AGENTS.md")).unwrap(),
        vm.home(".config/devbox/AGENTS.md")
    );
    let sudoers = std::fs::read_to_string(vm.path("root/etc/sudoers.d/devbox")).unwrap();
    assert_eq!(sudoers, "dev ALL=(root) NOPASSWD: /usr/local/sbin/devbox-set-clock\n");
    // The one program the user may run as root takes a number and nothing else.
    let set_clock = vm.path("root/usr/local/sbin/devbox-set-clock");
    assert_eq!(mode(&set_clock), 0o755);
    let refused =
        std::process::Command::new("sh").arg(&set_clock).arg("1; reboot").output().unwrap();
    assert_eq!(refused.status.code(), Some(2));
    assert_eq!(mode(&vm.path("root/etc/sudoers.d/devbox")), 0o440);

    let calls = vm.calls();
    for call in [
        "apt-get install -y -qq git gh curl",
        "systemctl enable --now code-server@dev",
        "npm install --global --silent --prefix",
        "devbox-hub hooks install",
        "gh auth login --with-token",
        "gh auth setup-git",
        "loginctl enable-linger dev",
        "systemctl --user enable --now devbox-tmux.service devbox-hub.service devbox-context.timer",
        "systemctl --user restart devbox-hub.service",
    ] {
        assert!(calls.contains(call), "{call}\n{calls}");
    }
    assert!(
        calls.contains("@openai/codex") && !calls.contains("opencode-ai"),
        "only the agents that were asked for"
    );

    // A second provisioning: the hub has added its web push keys meanwhile, the owner has
    // their own Claude instructions, and code-server keeps its password.
    let secrets = vm.home(".config/devbox/hub.secrets.toml");
    let with_keys = std::fs::read_to_string(&secrets).unwrap()
        + "\n# Web push keys, generated by devbox-hub.\nvapid_public_key = \"FAKE-public\"\nvapid_private_key = \"FAKE-private\"\n";
    std::fs::write(&secrets, with_keys).unwrap();
    std::fs::write(vm.home(".claude/CLAUDE.md"), "my own\n").unwrap();
    vm.write("bundle/hub.secrets.toml", "version = 1\napp_password_hash = 'FAKE-new-hash'\ncookie_secret = \"FAKE-cookie\"\nhub_token = \"FAKE-token\"\n");
    let (ok, out) = vm.run("install.sh");
    assert!(ok, "{out}");
    let merged = vm.read(".config/devbox/hub.secrets.toml");
    assert!(
        merged.contains("app_password_hash = 'FAKE-new-hash'")
            && merged.contains(
                "vapid_public_key = \"FAKE-public\"\nvapid_private_key = \"FAKE-private\"\n"
            ),
        "{merged}"
    );
    assert_eq!(merged.matches("vapid_public_key").count(), 1);
    assert_eq!(vm.read(".claude/CLAUDE.md"), "my own\n");
    assert_eq!(vm.read(".config/code-server/config.yaml"), config);
}

#[test]
fn an_unknown_user_or_agent_stops_the_installer_before_it_changes_anything() {
    let vm = provisioned();
    vm.write(
        "bundle/env",
        "DEVBOX_USER='dev'\nCODE_PORT=8080\nAGENTS='gemini'\nRELEASE='x'\nREPO='example/devbox'\n",
    );
    let (ok, out) = vm.run("install.sh");
    assert!(!ok && out.contains("unknown agent: gemini"), "{out}");
    vm.stub("getent", "true");
    let (ok, out) = vm.run("install.sh");
    assert!(!ok && out.contains("there is no user dev in this VM"), "{out}");
}

#[test]
fn the_example_dotfiles_are_applied_as_their_manifest_says() {
    let vm = provisioned();
    assert!(vm.run("install.sh").0);
    // What `devbox up` does first: the checkout, and the manifest read into dotfiles.env.
    copy_dir(&devbox_tests::repo("tests/fixtures/dotfiles"), &vm.home("dotfiles"));
    let manifest = devbox_core::config::dotfiles_manifest(
        "devbox.toml",
        &devbox_tests::read("tests/fixtures/dotfiles/devbox.toml"),
    )
    .unwrap();
    assert_eq!(manifest.warnings, [] as [&str; 0]);
    vm.write("bundle/dotfiles.env", "PACKAGES='fd-find htop'\nINSTALL='install.sh'\nBASHRC='bashrc'\nGITCONFIG='gitconfig'\nSETTINGS='code-server/settings.json'\nKEYBINDINGS='code-server/keybindings.json'\nEXTENSIONS='code-server/extensions.txt'\nINSTRUCTIONS='agents/AGENTS.md'\nCONTEXT_DIR='context'\n");
    std::fs::write(vm.home(".bashrc"), "# the distribution's own\n").unwrap();

    for run in 1..=2 {
        let (ok, out) = vm.run("dotfiles.sh");
        assert!(ok, "{out}");
        assert!(
            out.contains("not.on-open-vsx is not on Open VSX; skipped") && out.ends_with("done\n"),
            "{out}"
        );
        // Links, so an edit in either place is the same file.
        let dotfiles = vm.home("dotfiles");
        assert_eq!(
            std::fs::read_link(vm.home(".local/share/code-server/User/settings.json")).unwrap(),
            dotfiles.join("code-server/settings.json")
        );
        assert_eq!(
            std::fs::read_link(vm.home(".local/share/code-server/User/keybindings.json")).unwrap(),
            dotfiles.join("code-server/keybindings.json")
        );
        assert_eq!(
            std::fs::read_link(vm.home(".codex/AGENTS.md")).unwrap(),
            dotfiles.join("agents/AGENTS.md")
        );
        assert_eq!(
            vm.read(".claude/CLAUDE.md"),
            "@~/dotfiles/agents/AGENTS.md\n@~/dotfiles/context/summary.md\n"
        );
        // Sourced and included once, however often it is applied.
        assert_eq!(
            vm.read(".bashrc"),
            "# the distribution's own\n[ -f ~/dotfiles/bashrc ] && . ~/dotfiles/bashrc\n"
        );
        assert_eq!(vm.read(".gitconfig").matches("dotfiles/gitconfig").count(), 1);
        // The install hook runs on every provisioning, as the user.
        assert_eq!(vm.read(".local/share/example/runs").lines().count(), run);
    }
    let calls = vm.calls();
    assert!(calls.contains("apt-get install -y -qq fd-find htop"), "{calls}");
    assert!(calls.contains("code-server --install-extension rust-lang.rust-analyzer"), "{calls}");
    assert!(!calls.contains("--install-extension #"), "{calls}");
}
