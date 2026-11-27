//! A hub for tests: the one call `devbox context` makes on the internal socket, answered with
//! a fixed event log.

// Test helpers: one that cannot do its job panics, which fails the test that called it.
#![allow(clippy::missing_panics_doc)]

use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use serde_json::{Value, json};

/// The fake hub, listening on a unix socket until dropped.
pub struct FakeHub {
    socket: PathBuf,
    requests: Arc<Mutex<Vec<String>>>,
    stop: Arc<AtomicBool>,
}

impl FakeHub {
    /// Starts a hub at `socket` whose event log is `events`.
    #[must_use]
    pub fn start(socket: &Path, events: Vec<Value>) -> Self {
        let listener =
            UnixListener::bind(socket).unwrap_or_else(|err| panic!("{}: {err}", socket.display()));
        let requests = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let (seen, stopping) = (Arc::clone(&requests), Arc::clone(&stop));
        std::thread::spawn(move || {
            for stream in listener.incoming().flatten() {
                if stopping.load(Ordering::Relaxed) {
                    break;
                }
                let Ok(read_half) = stream.try_clone() else { continue };
                let mut lines = BufReader::new(read_half).lines();
                let Some(Ok(first)) = lines.next() else { continue };
                let target = first.split(' ').nth(1).unwrap_or_default().to_owned();
                for line in lines.map_while(Result::ok) {
                    if line.is_empty() {
                        break;
                    }
                }
                let (status, body) = if target.starts_with("/internal/events?since=") {
                    (200, json!({ "events": events }))
                } else {
                    (404, json!({"error": "not found"}))
                };
                if let Ok(mut seen) = seen.lock() {
                    seen.push(target);
                }
                let body = body.to_string();
                let mut stream = stream;
                let _ = write!(
                    stream,
                    "HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
            }
        });
        Self { socket: socket.to_owned(), requests, stop }
    }

    /// The paths (with queries) asked so far.
    #[must_use]
    pub fn requests(&self) -> Vec<String> {
        self.requests.lock().map_or_default(|seen| seen.clone())
    }
}

impl Drop for FakeHub {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        // Wakes the accept loop so it sees the flag.
        let _ = UnixStream::connect(&self.socket);
        let _ = std::fs::remove_file(&self.socket);
    }
}
