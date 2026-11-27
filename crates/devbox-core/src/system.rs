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

/// An HTTP request to a service that listens on a unix socket.
#[derive(Clone, Debug, Default)]
pub struct HttpRequest {
    /// `GET`, `PUT`, …
    pub method: String,
    /// The path, with its query.
    pub path: String,
    /// Headers besides `Host`, `Content-Length` and `Connection`.
    pub headers: Vec<(String, String)>,
    /// The body; empty for none.
    pub body: Vec<u8>,
    /// How long the answer may take; `None` is as long as it takes.
    pub timeout: Option<Duration>,
}

/// The answer to an [`HttpRequest`].
#[derive(Clone, Debug, Default)]
pub struct HttpResponse {
    /// The status code.
    pub status: u16,
    /// The body.
    pub body: Vec<u8>,
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

    /// Writes a file, replacing what is there and creating its directories when missing.
    ///
    /// # Errors
    ///
    /// As [`std::fs::write`].
    fn write(&self, path: &Path, data: &[u8]) -> io::Result<()>;

    /// Adds `data` to the end of a file, creating it and its directories when missing.
    ///
    /// # Errors
    ///
    /// As [`std::fs::OpenOptions::open`] and [`std::io::Write::write_all`].
    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()>;

    /// Removes a file; one that is not there is not an error.
    ///
    /// # Errors
    ///
    /// As [`std::fs::remove_file`], except for a missing file.
    fn remove_file(&self, path: &Path) -> io::Result<()>;

    /// Creates a directory and those above it.
    ///
    /// # Errors
    ///
    /// As [`std::fs::create_dir_all`].
    fn create_dir_all(&self, path: &Path) -> io::Result<()>;

    /// Sets the permission bits of a file or directory (`0o600`, `0o755`).
    ///
    /// # Errors
    ///
    /// As [`std::fs::set_permissions`].
    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()>;

    /// The permission bits of what is at `path`; `None` when nothing is.
    fn mode(&self, path: &Path) -> Option<u32>;

    /// Sends an HTTP request to the service listening on the unix socket at `socket` and reads
    /// its answer to the end.
    ///
    /// # Errors
    ///
    /// When the socket cannot be reached or the answer is not HTTP.
    fn unix_http(&self, socket: &Path, request: &HttpRequest) -> io::Result<HttpResponse>;

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

    /// Waits.
    fn sleep(&self, duration: Duration);

    /// Fills `buffer` with bytes nobody can guess, for secrets.
    ///
    /// # Errors
    ///
    /// When the machine has no source of randomness.
    fn random(&self, buffer: &mut [u8]) -> io::Result<()>;
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

    fn write(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        std::fs::write(path, data)
    }

    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        std::fs::OpenOptions::new().append(true).create(true).open(path)?.write_all(data)
    }

    fn remove_file(&self, path: &Path) -> io::Result<()> {
        match std::fs::remove_file(path) {
            Err(error) if error.kind() != io::ErrorKind::NotFound => Err(error),
            _ => Ok(()),
        }
    }

    fn create_dir_all(&self, path: &Path) -> io::Result<()> {
        std::fs::create_dir_all(path)
    }

    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()> {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode))
    }

    fn mode(&self, path: &Path) -> Option<u32> {
        use std::os::unix::fs::PermissionsExt;
        std::fs::metadata(path).ok().map(|meta| meta.permissions().mode() & 0o7777)
    }

    fn unix_http(&self, socket: &Path, request: &HttpRequest) -> io::Result<HttpResponse> {
        let mut stream = std::os::unix::net::UnixStream::connect(socket)?;
        stream.set_read_timeout(request.timeout)?;
        stream.set_write_timeout(request.timeout)?;
        let mut head = format!(
            "{} {} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\nContent-Length: {}\r\n",
            request.method,
            request.path,
            request.body.len()
        );
        for (name, value) in &request.headers {
            head.push_str(name);
            head.push_str(": ");
            head.push_str(value);
            head.push_str("\r\n");
        }
        head.push_str("\r\n");
        stream.write_all(head.as_bytes())?;
        stream.write_all(&request.body)?;
        let mut answer = Vec::new();
        stream.read_to_end(&mut answer)?;
        parse_response(&answer).ok_or_else(|| io::Error::other("the answer is not HTTP"))
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

    fn sleep(&self, duration: Duration) {
        std::thread::sleep(duration);
    }

    fn random(&self, buffer: &mut [u8]) -> io::Result<()> {
        getrandom::fill(buffer).map_err(io::Error::other)
    }
}

/// Splits an HTTP/1.1 response read to its end into status and body, undoing chunked transfer
/// encoding.
fn parse_response(answer: &[u8]) -> Option<HttpResponse> {
    let split = answer.windows(4).position(|window| window == b"\r\n\r\n")?;
    let head = std::str::from_utf8(&answer[..split]).ok()?;
    let mut body = &answer[split + 4..];
    let mut lines = head.split("\r\n");
    let status = lines.next()?.split(' ').nth(1)?.parse().ok()?;
    let chunked = lines.any(|line| {
        line.split_once(':').is_some_and(|(name, value)| {
            name.eq_ignore_ascii_case("transfer-encoding")
                && value.trim().eq_ignore_ascii_case("chunked")
        })
    });
    if !chunked {
        return Some(HttpResponse { status, body: body.to_vec() });
    }
    let mut decoded = Vec::new();
    loop {
        let line_end = body.windows(2).position(|window| window == b"\r\n")?;
        let size_text = std::str::from_utf8(&body[..line_end]).ok()?;
        let size = usize::from_str_radix(size_text.split(';').next()?.trim(), 16).ok()?;
        body = &body[line_end + 2..];
        if size == 0 {
            return Some(HttpResponse { status, body: decoded });
        }
        decoded.extend_from_slice(body.get(..size)?);
        body = body.get(size + 2..)?;
    }
}

#[cfg(test)]
mod tests {
    use super::parse_response;

    #[test]
    fn responses_are_split_into_status_and_body() {
        let plain = parse_response(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}").unwrap();
        assert_eq!((plain.status, plain.body.as_slice()), (200, &b"{}"[..]));
        let empty = parse_response(b"HTTP/1.1 404 Not Found\r\n\r\n").unwrap();
        assert_eq!((empty.status, empty.body.len()), (404, 0));
        let chunked = parse_response(
            b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n4\r\n{\"a\"\r\n3;x=y\r\n:1}\r\n0\r\n\r\n",
        )
        .unwrap();
        assert_eq!(chunked.body, b"{\"a\":1}");
        assert!(parse_response(b"not http").is_none());
        assert!(
            parse_response(b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab")
                .is_none()
        );
    }
}
