//! A moi host that exists only in memory: files, canned programs and a fake Incus, kept by
//! devbox's own `FakeSystem` and offered through moi's trait. What moi's `Host` has and devbox
//! never calls answers `Unsupported`.

use std::fmt;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

use devbox_core::fake::FakeSystem;
use devbox_core::system::{self, Command, System};

use crate::host::{
    Cmd, CmdOutput, Conn, FileKind, FsUsage, Host, Listener, Metadata, Watch,
};

/// A server in memory, as moi sees servers.
pub struct FixtureHost {
    /// What is on it; tests fill and inspect it.
    pub machine: FakeSystem,
}

impl fmt::Debug for FixtureHost {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("FixtureHost")
    }
}

#[derive(Debug)]
struct FixtureConn(Box<dyn system::Conn>);

impl Read for FixtureConn {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        self.0.read(buffer)
    }
}

impl Write for FixtureConn {
    fn write(&mut self, data: &[u8]) -> io::Result<usize> {
        self.0.write(data)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.0.flush()
    }
}

impl Conn for FixtureConn {
    fn set_timeout(&mut self, timeout: Option<Duration>) -> io::Result<()> {
        self.0.set_timeout(timeout)
    }
}

fn unsupported<T>(what: &str) -> io::Result<T> {
    Err(io::Error::new(io::ErrorKind::Unsupported, format!("the fixture host has no {what}")))
}

impl Host for FixtureHost {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        self.machine.read(path)
    }

    fn open(&self, path: &Path) -> io::Result<Box<dyn io::Read + Send>> {
        Ok(Box::new(io::Cursor::new(self.machine.read(path)?)))
    }

    fn create(&self, _: &Path, _: u32) -> io::Result<Box<dyn io::Write + Send>> {
        unsupported("streamed writes")
    }

    fn append(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
        self.machine.append(path, data, mode)
    }

    fn lock(&self, _: &Path) -> io::Result<Option<Box<dyn std::any::Any + Send>>> {
        Ok(Some(Box::new(())))
    }

    fn metadata(&self, path: &Path) -> io::Result<Metadata> {
        let meta = self.machine.metadata(path)?;
        Ok(Metadata {
            kind: match meta.kind {
                system::FileKind::File => FileKind::File,
                system::FileKind::Dir => FileKind::Dir,
                system::FileKind::Symlink => FileKind::Symlink,
                system::FileKind::Other => FileKind::Other,
            },
            size: meta.size,
            mode: meta.mode,
            uid: meta.uid,
            gid: meta.gid,
            mtime: meta.mtime,
            inode: 0,
            dev: 0,
        })
    }

    fn symlink_metadata(&self, path: &Path) -> io::Result<Metadata> {
        self.metadata(path)
    }

    fn read_link(&self, path: &Path) -> io::Result<PathBuf> {
        self.machine.read_link(path)
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<String>> {
        self.machine.read_dir(path)
    }

    fn exists(&self, path: &Path) -> bool {
        self.machine.exists(path)
    }

    fn write(&self, path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
        self.machine.write(path, data, mode)
    }

    fn create_dir_all(&self, path: &Path, mode: u32) -> io::Result<()> {
        self.machine.create_dir_all(path, mode)
    }

    fn remove_file(&self, path: &Path) -> io::Result<()> {
        self.machine.remove_file(path)
    }

    fn remove_dir(&self, _: &Path) -> io::Result<()> {
        Ok(())
    }

    fn symlink(&self, target: &Path, path: &Path) -> io::Result<()> {
        self.machine.symlink(target, path)
    }

    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()> {
        self.machine.set_mode(path, mode)
    }

    fn set_owner(&self, path: &Path, uid: u32, gid: u32) -> io::Result<()> {
        self.machine.set_owner(path, uid, gid)
    }

    fn real_path(&self, _: &Path) -> io::Result<PathBuf> {
        unsupported("real paths")
    }

    fn run(&self, cmd: &Cmd) -> io::Result<CmdOutput> {
        let command = Command {
            program: cmd.program.clone(),
            args: cmd.args.clone(),
            cwd: None,
            env: cmd.env.clone(),
            stdin: cmd.stdin.clone(),
            timeout: cmd.timeout,
        };
        let output = self.machine.run(&command)?;
        Ok(CmdOutput {
            status: output.code.unwrap_or(128),
            stdout: output.stdout,
            stderr: output.stderr,
            timed_out: output.timed_out,
        })
    }

    fn run_interactive(&self, _: &Cmd) -> io::Result<i32> {
        unsupported("terminal")
    }

    fn connect_unix(&self, path: &Path) -> io::Result<Box<dyn Conn>> {
        Ok(Box::new(FixtureConn(self.machine.connect_unix(path)?)))
    }

    fn ask(&self, _: &str, _: Duration) -> io::Result<Option<String>> {
        Ok(None)
    }

    fn watch(&self, _: &[PathBuf]) -> io::Result<Box<dyn Watch>> {
        unsupported("inotify")
    }

    fn listen_unix(&self, _: &Path, _: u32) -> io::Result<Box<dyn Listener>> {
        unsupported("listening sockets")
    }

    fn notify(&self, _: &str) -> io::Result<bool> {
        Ok(false)
    }

    fn statvfs(&self, _: &Path) -> io::Result<FsUsage> {
        unsupported("statvfs")
    }

    fn connect_tcp(&self, _: &str, _: u16, _: Duration) -> io::Result<Box<dyn Conn>> {
        unsupported("network")
    }

    fn resolve(&self, _: &str, _: Duration) -> io::Result<Vec<std::net::IpAddr>> {
        unsupported("resolver")
    }

    fn sleep(&self, duration: Duration) {
        self.machine.sleep(duration);
    }

    fn monotonic(&self) -> Duration {
        self.machine.monotonic()
    }

    fn random_bytes(&self, count: usize) -> io::Result<Vec<u8>> {
        self.machine.random_bytes(count)
    }

    fn now(&self) -> SystemTime {
        self.machine.now()
    }

    fn env(&self, name: &str) -> Option<String> {
        self.machine.env(name)
    }

    fn journal(&self, _: &[(String, String)]) -> io::Result<()> {
        Ok(())
    }
}
