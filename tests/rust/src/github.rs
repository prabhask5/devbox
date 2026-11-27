//! A GitHub API for tests: the calls `devbox sync` and `devbox context` make, answered from a
//! list of repositories and their open issues, with everything asked of it remembered.

// Test helpers: one that cannot do its job panics, which fails the test that called it.
#![allow(clippy::missing_panics_doc)]

use std::io::{BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use serde_json::{Value, json};

/// The token the fake accepts.
pub const TOKEN: &str = "FAKE-github-token";

/// One request the fake received.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Request {
    /// Path and query.
    pub target: String,
    /// The `Authorization` header.
    pub authorization: String,
}

/// A repository as GitHub's API describes it; the URLs may be anything git can clone from.
#[must_use]
pub fn repo(owner: &str, name: &str, ssh_url: &str, https_url: &str) -> Value {
    json!({
        "name": name, "full_name": format!("{owner}/{name}"), "owner": {"login": owner},
        "archived": false, "ssh_url": ssh_url, "clone_url": https_url,
    })
}

/// The fake API, listening on a port of its own until dropped.
pub struct FakeGitHub {
    /// What to give `devbox sync` as `DEVBOX_GITHUB_API`.
    pub url: String,
    requests: Arc<Mutex<Vec<Request>>>,
    stop: Arc<AtomicBool>,
    address: std::net::SocketAddr,
}

fn answer(login: &str, repos: &[Value], issues: &Value, request: &Request) -> (u16, Value) {
    if request.authorization != format!("Bearer {TOKEN}") {
        return (401, json!({"message": "Bad credentials"}));
    }
    let (path, query) = request.target.split_once('?').unwrap_or((&request.target, ""));
    let param = |name: &str| {
        query.split('&').find_map(|pair| pair.strip_prefix(name)?.strip_prefix('=')).unwrap_or("")
    };
    match path {
        "/user" => (200, json!({"login": login})),
        "/user/repos" => {
            let members = param("affiliation").contains("organization_member");
            let per_page: usize = param("per_page").parse().unwrap_or(30);
            let page: usize = param("page").parse().unwrap_or(1);
            let visible: Vec<&Value> =
                repos.iter().filter(|repo| members || repo["owner"]["login"] == login).collect();
            let listed: Vec<&Value> =
                visible.into_iter().skip(per_page * (page.max(1) - 1)).take(per_page).collect();
            (200, json!(listed))
        }
        // GET /repos/<owner>/<name>/issues: open issues and pull requests in one list.
        _ => match path.strip_prefix("/repos/").and_then(|rest| rest.strip_suffix("/issues")) {
            Some(full_name) => (200, issues.get(full_name).cloned().unwrap_or_else(|| json!([]))),
            None => (404, json!({"message": "Not Found"})),
        },
    }
}

fn serve(
    mut stream: TcpStream,
    login: &str,
    repos: &[Value],
    issues: &Value,
    requests: &Mutex<Vec<Request>>,
) {
    let Ok(read_half) = stream.try_clone() else { return };
    let mut lines = BufReader::new(read_half).lines();
    let Some(Ok(first)) = lines.next() else { return };
    let target = first.split(' ').nth(1).unwrap_or_default().to_owned();
    let mut authorization = String::new();
    for line in lines.map_while(Result::ok) {
        if line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(": ")
            && name.eq_ignore_ascii_case("authorization")
        {
            value.clone_into(&mut authorization);
        }
    }
    let request = Request { target, authorization };
    let (status, body) = answer(login, repos, issues, &request);
    if let Ok(mut seen) = requests.lock() {
        seen.push(request);
    }
    let body = body.to_string();
    let _ = write!(
        stream,
        "HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
}

impl FakeGitHub {
    /// Starts a GitHub on which `login` is signed in and `repos` exist.
    #[must_use]
    pub fn start(login: &str, repos: Vec<Value>) -> Self {
        Self::with_issues(login, repos, json!({}))
    }

    /// Starts a GitHub on which `login` is signed in, `repos` exist and `issues` maps a
    /// repository's full name to its open issues and pull requests.
    #[must_use]
    pub fn with_issues(login: &str, repos: Vec<Value>, issues: Value) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap_or_else(|err| panic!("{err}"));
        let address = listener.local_addr().unwrap_or_else(|err| panic!("{err}"));
        let requests = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let (login, seen, stopping) = (login.to_owned(), Arc::clone(&requests), Arc::clone(&stop));
        std::thread::spawn(move || {
            for stream in listener.incoming().flatten() {
                if stopping.load(Ordering::Relaxed) {
                    break;
                }
                serve(stream, &login, &repos, &issues, &seen);
            }
        });
        Self { url: format!("http://{address}"), requests, stop, address }
    }

    /// Everything asked so far, in order.
    #[must_use]
    pub fn requests(&self) -> Vec<Request> {
        self.requests.lock().map_or_default(|seen| seen.clone())
    }

    /// The paths (with queries) asked so far, in order.
    #[must_use]
    pub fn targets(&self) -> Vec<String> {
        self.requests().into_iter().map(|request| request.target).collect()
    }
}

impl Drop for FakeGitHub {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        // Wakes the accept loop so it sees the flag.
        let _ = TcpStream::connect(self.address);
    }
}
