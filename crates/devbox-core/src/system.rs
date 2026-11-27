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

/// An entry of a directory.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Entry {
    /// Its file name.
    pub name: String,
    /// Whether it is a directory (a link to one counts).
    pub is_dir: bool,
}

/// The machine devbox runs on. Implementations are shared between threads.
pub trait System: Sync {
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

    /// Adds `data` to the end of a file, creating it and its directories when missing.
    ///
    /// # Errors
    ///
    /// As [`std::fs::OpenOptions::open`] and [`std::io::Write::write_all`].
    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()>;

    /// Creates a directory and those above it.
    ///
    /// # Errors
    ///
    /// As [`std::fs::create_dir_all`].
    fn create_dir_all(&self, path: &Path) -> io::Result<()>;

    /// Lists a directory, sorted by name.
    ///
    /// # Errors
    ///
    /// As [`std::fs::read_dir`].
    fn read_dir(&self, path: &Path) -> io::Result<Vec<Entry>>;

    /// Whether something exists at `path`.
    fn exists(&self, path: &Path) -> bool;

    /// A variable of the environment; `None` when unset or not text.
    fn env(&self, name: &str) -> Option<String>;

    /// The time.
    fn now(&self) -> SystemTime;
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

    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        std::fs::OpenOptions::new().append(true).create(true).open(path)?.write_all(data)
    }

    fn create_dir_all(&self, path: &Path) -> io::Result<()> {
        std::fs::create_dir_all(path)
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<Entry>> {
        let mut entries = Vec::new();
        for entry in std::fs::read_dir(path)? {
            let entry = entry?;
            entries.push(Entry {
                name: entry.file_name().to_string_lossy().into_owned(),
                is_dir: entry.path().is_dir(),
            });
        }
        entries.sort();
        Ok(entries)
    }

    fn exists(&self, path: &Path) -> bool {
        path.exists()
    }

    fn env(&self, name: &str) -> Option<String> {
        self.env.get(name).cloned().or_else(|| std::env::var(name).ok())
    }

    fn now(&self) -> SystemTime {
        SystemTime::now()
    }
}
