//! Everything devbox does to the machine it runs on goes through [`System`]: running programs,
//! reading and writing files, the environment and the clock. [`Host`] is the real machine; tests
//! and embedders bring their own.

use std::collections::BTreeMap;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Stdio};
use std::sync::mpsc;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

/// A program to run.
#[derive(Clone, Debug, Default)]
pub struct Command {
    /// The program, looked up on `PATH`.
    pub program: String,
    /// Its arguments.
    pub args: Vec<String>,
    /// The directory to run it in; `None` is the current one.
    pub cwd: Option<PathBuf>,
    /// Variables added to the environment it inherits.
    pub env: Vec<(String, String)>,
    /// What it reads on its standard input; `None` is nothing.
    pub stdin: Option<Vec<u8>>,
    /// How long it may run before it is killed; `None` is as long as it takes.
    pub timeout: Option<Duration>,
}

impl Command {
    /// `program` with `args`.
    #[must_use]
    pub fn new<S: Into<String>>(program: &str, args: impl IntoIterator<Item = S>) -> Self {
        Self {
            program: program.to_owned(),
            args: args.into_iter().map(Into::into).collect(),
            ..Self::default()
        }
    }

    /// Runs it in `dir`.
    #[must_use]
    pub fn in_dir(mut self, dir: &Path) -> Self {
        self.cwd = Some(dir.to_owned());
        self
    }

    /// Adds a variable to its environment.
    #[must_use]
    pub fn env(mut self, name: &str, value: &str) -> Self {
        self.env.push((name.to_owned(), value.to_owned()));
        self
    }

    /// Gives it `input` on its standard input.
    #[must_use]
    pub fn stdin(mut self, input: impl Into<Vec<u8>>) -> Self {
        self.stdin = Some(input.into());
        self
    }

    /// Kills it when it runs longer than `limit`.
    #[must_use]
    pub fn timeout(mut self, limit: Duration) -> Self {
        self.timeout = Some(limit);
        self
    }
}

/// What a program left behind.
#[derive(Clone, Debug, Default)]
pub struct Output {
    /// Its exit code; `None` when a signal ended it.
    pub code: Option<i32>,
    /// Whether it was killed for running longer than its timeout.
    pub timed_out: bool,
    /// What it wrote to its standard output.
    pub stdout: Vec<u8>,
    /// What it wrote to its standard error.
    pub stderr: Vec<u8>,
}

impl Output {
    /// Whether it exited with code 0.
    #[must_use]
    pub fn success(&self) -> bool {
        self.code == Some(0)
    }

    /// Its standard output as text, without the whitespace around it.
    #[must_use]
    pub fn text(&self) -> String {
        String::from_utf8_lossy(&self.stdout).trim().to_owned()
    }

    /// Its standard error as text, without the whitespace around it.
    #[must_use]
    pub fn error_text(&self) -> String {
        String::from_utf8_lossy(&self.stderr).trim().to_owned()
    }
}

/// What a path is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FileKind {
    /// A regular file.
    File,
    /// A directory.
    Dir,
    /// A symbolic link (only when the link itself is asked about).
    Symlink,
    /// Anything else: a socket, a device.
    Other,
}

/// The parts of `stat` devbox uses.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Metadata {
    /// What it is.
    pub kind: FileKind,
    /// Its size in bytes.
    pub size: u64,
    /// Its permission bits (`0o7777` mask).
    pub mode: u32,
    /// The user id that owns it.
    pub uid: u32,
    /// The group id that owns it.
    pub gid: u32,
    /// When it was last changed.
    pub mtime: SystemTime,
}

/// Whether `path` is a directory (a link to one counts).
#[must_use]
pub fn is_dir(system: &dyn System, path: &Path) -> bool {
    system.metadata(path).is_ok_and(|meta| meta.kind == FileKind::Dir)
}

/// A connected stream socket: the Incus API's unix socket.
pub trait Conn: Read + Write + Send + std::fmt::Debug {
    /// Fails reads and writes that block longer than this; `None` waits forever.
    ///
    /// # Errors
    ///
    /// When the socket refuses the setting.
    fn set_timeout(&mut self, timeout: Option<Duration>) -> io::Result<()>;
}

/// The machine devbox runs on. Every method maps onto the method of the same name of moi's
/// `host::Host` (docs/spec/12-moi.md), so that moi can drive devbox through its own host.
pub trait System: Send + Sync {
    /// Runs a program to its end.
    ///
    /// # Errors
    ///
    /// When the program cannot be started. A program that ran and failed is an [`Output`].
    fn run(&self, command: &Command) -> io::Result<Output>;

    /// Reads a file.
    ///
    /// # Errors
    ///
    /// As [`std::fs::read`].
    fn read(&self, path: &Path) -> io::Result<Vec<u8>>;

    /// The names of a directory's entries, sorted.
    ///
    /// # Errors
    ///
    /// As [`std::fs::read_dir`].
    fn read_dir(&self, path: &Path) -> io::Result<Vec<String>>;

    /// Whether something exists at `path`.
    fn exists(&self, path: &Path) -> bool;

    /// What is at `path`, following symbolic links.
    ///
    /// # Errors
    ///
    /// As [`std::fs::metadata`].
    fn metadata(&self, path: &Path) -> io::Result<Metadata>;

    /// The target of a symbolic link, as stored.
    ///
    /// # Errors
    ///
    /// As [`std::fs::read_link`].
    fn read_link(&self, path: &Path) -> io::Result<PathBuf>;

    /// Writes a file with these permission bits, replacing what is there and creating its
    /// directories when missing. The file has its mode before it has its content.
    ///
    /// # Errors
    ///
    /// As [`std::fs::write`].
    fn write(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()>;

    /// Adds `data` to the end of a file, creating it (with `mode`) and its directories when
    /// missing.
    ///
    /// # Errors
    ///
    /// As [`std::fs::OpenOptions::open`] and [`std::io::Write::write_all`].
    fn append(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()>;

    /// Creates a directory and those above it; the ones it creates get `mode`.
    ///
    /// # Errors
    ///
    /// As [`std::fs::create_dir_all`].
    fn create_dir_all(&self, path: &Path, mode: u32) -> io::Result<()>;

    /// Removes a file; one that is not there is not an error.
    ///
    /// # Errors
    ///
    /// As [`std::fs::remove_file`], except for a missing file.
    fn remove_file(&self, path: &Path) -> io::Result<()>;

    /// Creates a symbolic link at `path` pointing to `target`, replacing a file or link there.
    ///
    /// # Errors
    ///
    /// As [`std::os::unix::fs::symlink`].
    fn symlink(&self, target: &Path, path: &Path) -> io::Result<()>;

    /// Sets the permission bits of a file or directory (`0o600`, `0o755`).
    ///
    /// # Errors
    ///
    /// As [`std::fs::set_permissions`].
    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()>;

    /// Sets the user and group that own a file.
    ///
    /// # Errors
    ///
    /// As [`std::os::unix::fs::chown`].
    fn set_owner(&self, path: &Path, uid: u32, gid: u32) -> io::Result<()>;

    /// Connects to the service listening on a unix stream socket.
    ///
    /// # Errors
    ///
    /// When nothing listens there or it may not be reached.
    fn connect_unix(&self, path: &Path) -> io::Result<Box<dyn Conn>>;

    /// A variable of the environment; `None` when unset or not text.
    fn env(&self, name: &str) -> Option<String>;

    /// The time.
    fn now(&self) -> SystemTime;

    /// A clock that only moves forward, for measuring how long something took.
    fn monotonic(&self) -> Duration;

    /// Waits.
    fn sleep(&self, duration: Duration);

    /// Bytes nobody can guess, for secrets.
    ///
    /// # Errors
    ///
    /// When the machine has no source of randomness.
    fn random_bytes(&self, count: usize) -> io::Result<Vec<u8>>;
}

/// The real machine. Variables given to [`Host::with_env`] replace those of the process, for
/// what devbox reads and for every program it runs.
#[derive(Clone, Debug, Default)]
pub struct Host {
    env: BTreeMap<String, String>,
}

impl Host {
    /// The machine as this process sees it.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// The machine with `name` set to `value`.
    #[must_use]
    pub fn with_env(mut self, name: &str, value: &str) -> Self {
        self.env.insert(name.to_owned(), value.to_owned());
        self
    }
}

fn drain(mut pipe: impl Read + Send + 'static) -> std::thread::JoinHandle<Vec<u8>> {
    std::thread::spawn(move || {
        let mut data = Vec::new();
        // A pipe that breaks has given all it will give.
        let _ = pipe.read_to_end(&mut data);
        data
    })
}

fn lock(child: &Mutex<Child>) -> std::sync::MutexGuard<'_, Child> {
    child.lock().unwrap_or_else(std::sync::PoisonError::into_inner)
}

#[derive(Debug)]
struct UnixConn(std::os::unix::net::UnixStream);

impl Read for UnixConn {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        self.0.read(buffer)
    }
}

impl Write for UnixConn {
    fn write(&mut self, data: &[u8]) -> io::Result<usize> {
        self.0.write(data)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.0.flush()
    }
}

impl Conn for UnixConn {
    fn set_timeout(&mut self, timeout: Option<Duration>) -> io::Result<()> {
        self.0.set_read_timeout(timeout)?;
        self.0.set_write_timeout(timeout)
    }
}

// The one place in the library crates that touches the operating system directly.
#[allow(clippy::disallowed_methods)]
impl System for Host {
    fn run(&self, command: &Command) -> io::Result<Output> {
        let mut process = std::process::Command::new(&command.program);
        process.args(&command.args).envs(&self.env).envs(command.env.iter().cloned());
        if let Some(dir) = &command.cwd {
            process.current_dir(dir);
        }
        process
            .stdin(if command.stdin.is_some() { Stdio::piped() } else { Stdio::null() })
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        let mut child = process.spawn()?;
        if let (Some(input), Some(mut pipe)) = (&command.stdin, child.stdin.take()) {
            // A program that does not read its input is not an error of ours.
            let _ = pipe.write_all(input);
        }
        let stdout = child.stdout.take().map(drain);
        let stderr = child.stderr.take().map(drain);

        // The pipes close when the program ends. Until then a watchdog waits with the timeout
        // and kills the program when it comes first.
        let child = Arc::new(Mutex::new(child));
        let (finished, watchdog) = mpsc::channel::<()>();
        let killer = command.timeout.map(|limit| {
            let child = Arc::clone(&child);
            std::thread::spawn(move || {
                let timed_out =
                    matches!(watchdog.recv_timeout(limit), Err(mpsc::RecvTimeoutError::Timeout));
                if timed_out {
                    // Already gone is as good as killed.
                    let _ = lock(&child).kill();
                }
                timed_out
            })
        });
        let collect = |reader: Option<std::thread::JoinHandle<Vec<u8>>>| {
            reader.and_then(|thread| thread.join().ok()).unwrap_or_default()
        };
        let (stdout, stderr) = (collect(stdout), collect(stderr));
        drop(finished);
        let timed_out = killer.and_then(|thread| thread.join().ok()).unwrap_or(false);
        let status = lock(&child).wait()?;
        Ok(Output { code: status.code(), timed_out, stdout, stderr })
    }

    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        std::fs::read(path)
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<String>> {
        let mut names = Vec::new();
        for entry in std::fs::read_dir(path)? {
            names.push(entry?.file_name().to_string_lossy().into_owned());
        }
        names.sort();
        Ok(names)
    }

    fn exists(&self, path: &Path) -> bool {
        std::fs::symlink_metadata(path).is_ok()
    }

    fn metadata(&self, path: &Path) -> io::Result<Metadata> {
        use std::os::unix::fs::MetadataExt;
        let meta = std::fs::metadata(path)?;
        let kind = if meta.is_dir() {
            FileKind::Dir
        } else if meta.is_file() {
            FileKind::File
        } else {
            FileKind::Other
        };
        Ok(Metadata {
            kind,
            size: meta.len(),
            mode: meta.mode() & 0o7777,
            uid: meta.uid(),
            gid: meta.gid(),
            mtime: meta.modified()?,
        })
    }

    fn read_link(&self, path: &Path) -> io::Result<PathBuf> {
        std::fs::read_link(path)
    }

    fn write(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
        use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        // Created with its mode, and given it if it was there with another, before any content.
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(false)
            .mode(mode)
            .open(path)?;
        file.set_permissions(std::fs::Permissions::from_mode(mode))?;
        file.set_len(0)?;
        file.write_all(data)
    }

    fn append(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
        use std::os::unix::fs::OpenOptionsExt;
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        std::fs::OpenOptions::new().append(true).create(true).mode(mode).open(path)?.write_all(data)
    }

    fn create_dir_all(&self, path: &Path, mode: u32) -> io::Result<()> {
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new().recursive(true).mode(mode).create(path)
    }

    fn remove_file(&self, path: &Path) -> io::Result<()> {
        match std::fs::remove_file(path) {
            Err(error) if error.kind() != io::ErrorKind::NotFound => Err(error),
            _ => Ok(()),
        }
    }

    fn symlink(&self, target: &Path, path: &Path) -> io::Result<()> {
        if std::fs::symlink_metadata(path).is_ok() {
            std::fs::remove_file(path)?;
        }
        std::os::unix::fs::symlink(target, path)
    }

    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()> {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode))
    }

    fn set_owner(&self, path: &Path, uid: u32, gid: u32) -> io::Result<()> {
        std::os::unix::fs::chown(path, Some(uid), Some(gid))
    }

    fn connect_unix(&self, path: &Path) -> io::Result<Box<dyn Conn>> {
        Ok(Box::new(UnixConn(std::os::unix::net::UnixStream::connect(path)?)))
    }

    fn env(&self, name: &str) -> Option<String> {
        self.env.get(name).cloned().or_else(|| std::env::var(name).ok())
    }

    fn now(&self) -> SystemTime {
        SystemTime::now()
    }

    fn monotonic(&self) -> Duration {
        static START: std::sync::OnceLock<std::time::Instant> = std::sync::OnceLock::new();
        START.get_or_init(std::time::Instant::now).elapsed()
    }

    fn sleep(&self, duration: Duration) {
        std::thread::sleep(duration);
    }

    fn random_bytes(&self, count: usize) -> io::Result<Vec<u8>> {
        // The kernel's generator; never short of entropy once the system is up.
        let mut bytes = vec![0; count];
        std::fs::File::open("/dev/urandom")?.read_exact(&mut bytes)?;
        Ok(bytes)
    }
}
