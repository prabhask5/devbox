//! The adapter: `devbox_core::system::System` implemented by a moi host. Every method is the
//! moi method of the same name (the table in docs/spec/12-moi.md); only the types differ.

use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

use devbox_core::system::{Command, Conn, FileKind, Metadata, Output, System};

use crate::host::{self, Cmd, Host};

/// devbox's view of the machine a moi host stands for.
#[derive(Debug)]
pub struct OnMoi<'a>(pub &'a dyn Host);

#[derive(Debug)]
struct MoiConn(Box<dyn host::Conn>);

impl Read for MoiConn {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        self.0.read(buffer)
    }
}

impl Write for MoiConn {
    fn write(&mut self, data: &[u8]) -> io::Result<usize> {
        self.0.write(data)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.0.flush()
    }
}

impl Conn for MoiConn {
    fn set_timeout(&mut self, timeout: Option<Duration>) -> io::Result<()> {
        self.0.set_timeout(timeout)
    }
}

impl System for OnMoi<'_> {
    fn run(&self, command: &Command) -> io::Result<Output> {
        // moi's commands have no working directory: `env -C` gives them one.
        let mut cmd = match &command.cwd {
            Some(dir) => Cmd::new("env")
                .arg("-C")
                .arg(dir.to_string_lossy().into_owned())
                .arg(command.program.clone()),
            None => Cmd::new(command.program.clone()),
        };
        cmd = cmd.args(command.args.iter().cloned());
        for (name, value) in &command.env {
            cmd = cmd.env(name.clone(), value.clone());
        }
        if let Some(input) = &command.stdin {
            cmd = cmd.stdin(input.clone());
        }
        if let Some(limit) = command.timeout {
            cmd = cmd.timeout(limit);
        }
        let output = self.0.run(&cmd)?;
        Ok(Output {
            code: Some(output.status),
            timed_out: output.timed_out,
            stdout: output.stdout,
            stderr: output.stderr,
        })
    }

    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        self.0.read(path)
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<String>> {
        self.0.read_dir(path)
    }

    fn exists(&self, path: &Path) -> bool {
        self.0.exists(path)
    }

    fn metadata(&self, path: &Path) -> io::Result<Metadata> {
        let meta = self.0.metadata(path)?;
        Ok(Metadata {
            kind: match meta.kind {
                host::FileKind::File => FileKind::File,
                host::FileKind::Dir => FileKind::Dir,
                host::FileKind::Symlink => FileKind::Symlink,
                host::FileKind::Other => FileKind::Other,
            },
            size: meta.size,
            mode: meta.mode,
            uid: meta.uid,
            gid: meta.gid,
            mtime: meta.mtime,
        })
    }

    fn read_link(&self, path: &Path) -> io::Result<PathBuf> {
        self.0.read_link(path)
    }

    fn write(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
        // moi's write is atomic and does not create directories; devbox's creates them.
        if let Some(dir) = path.parent() {
            self.0.create_dir_all(dir, 0o755)?;
        }
        self.0.write(path, data, mode)
    }

    fn append(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
        if let Some(dir) = path.parent() {
            self.0.create_dir_all(dir, 0o755)?;
        }
        self.0.append(path, data, mode)
    }

    fn create_dir_all(&self, path: &Path, mode: u32) -> io::Result<()> {
        self.0.create_dir_all(path, mode)
    }

    fn remove_file(&self, path: &Path) -> io::Result<()> {
        match self.0.remove_file(path) {
            Err(error) if error.kind() != io::ErrorKind::NotFound => Err(error),
            _ => Ok(()),
        }
    }

    fn symlink(&self, target: &Path, path: &Path) -> io::Result<()> {
        self.0.symlink(target, path)
    }

    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()> {
        self.0.set_mode(path, mode)
    }

    fn set_owner(&self, path: &Path, uid: u32, gid: u32) -> io::Result<()> {
        self.0.set_owner(path, uid, gid)
    }

    fn connect_unix(&self, path: &Path) -> io::Result<Box<dyn Conn>> {
        Ok(Box::new(MoiConn(self.0.connect_unix(path)?)))
    }

    fn env(&self, name: &str) -> Option<String> {
        self.0.env(name)
    }

    fn now(&self) -> SystemTime {
        self.0.now()
    }

    fn monotonic(&self) -> Duration {
        self.0.monotonic()
    }

    fn sleep(&self, duration: Duration) {
        self.0.sleep(duration);
    }

    fn random_bytes(&self, count: usize) -> io::Result<Vec<u8>> {
        self.0.random_bytes(count)
    }
}
