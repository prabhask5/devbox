//! The Incus client: against the fake Incus of `FakeSystem`, against the reference responses
//! in `tests/fixtures/incus/`, and — for the transport — against a real unix socket.

use std::collections::BTreeMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::net::UnixListener;
use std::path::{Path, PathBuf};
use std::time::Duration;

use devbox_core::fake::{FakeInstance, FakeSystem};
use devbox_core::incus::{Error, Exec, Incus, SOCKET};
use devbox_core::system::{Host, HttpRequest, HttpResponse, System};
use devbox_tests::sandbox::TempDir;
use serde_json::{Value, json};

fn incus(system: &FakeSystem) -> Incus<'_> {
    Incus::new(system, Path::new(SOCKET))
}

#[test]
fn an_instance_is_described_with_what_its_profiles_add() {
    let system = FakeSystem::new();
    assert_eq!(incus(&system).instance("devbox").unwrap(), None);
    system.incus(|incus| {
        let instance = FakeInstance::stopped()
            .config("limits.memory", "5GiB")
            .device("eth0", &[("ipv4.address", "10.77.0.10")]);
        incus.instances.insert("devbox".to_owned(), instance);
    });
    let instance = incus(&system).instance("devbox").unwrap().unwrap();
    assert_eq!(
        (instance.status.as_str(), instance.kind.as_str(), instance.stateful),
        ("Stopped", "virtual-machine", false)
    );
    assert_eq!(instance.config["limits.memory"], "5GiB");
    assert_eq!(instance.devices.keys().collect::<Vec<_>>(), ["eth0"]);
    // The profile's root disk and the rest of its NIC are part of what the instance runs with.
    assert_eq!(instance.expanded_devices["root"]["pool"], "default");
    assert_eq!(instance.expanded_devices["eth0"]["network"], "incusbr0");
    assert_eq!(instance.expanded_devices["eth0"]["ipv4.address"], "10.77.0.10");
    assert_eq!(incus(&system).version().unwrap(), "6.0.5");
    assert_eq!(
        incus(&system).network("incusbr0").unwrap().unwrap()["ipv4.address"],
        "10.77.0.1/24"
    );
    assert_eq!(incus(&system).network("nope").unwrap(), None);
}

#[test]
fn creating_configuring_and_changing_state() {
    let system = FakeSystem::new();
    let client = incus(&system);
    client
        .create(&json!({"name": "devbox", "type": "virtual-machine", "config": {"limits.cpu": "3"},
            "devices": {"root": {"path": "/", "pool": "default", "type": "disk", "size": "60GiB"}},
            "source": {"type": "image", "alias": "ubuntu/26.04/cloud", "server": "https://images.linuxcontainers.org", "protocol": "simplestreams", "mode": "pull"}}))
        .unwrap();
    let created = client.instance("devbox").unwrap().unwrap();
    assert_eq!((created.status.as_str(), created.config["limits.cpu"].as_str()), ("Stopped", "3"));
    assert!(
        matches!(client.create(&json!({"name": "devbox"})), Err(Error::Refused(why)) if why == "Instance already exists")
    );

    // Configuring writes exactly what is given: a key that is left out is gone.
    let mut config = created.config.clone();
    config.insert("migration.stateful".to_owned(), "true".to_owned());
    config.remove("limits.cpu");
    let mut devices = created.devices.clone();
    devices.get_mut("root").unwrap().insert("size.state".to_owned(), "6GiB".to_owned());
    client.configure("devbox", &config, &devices).unwrap();
    let configured = client.instance("devbox").unwrap().unwrap();
    assert_eq!(
        configured.config,
        BTreeMap::from([("migration.stateful".to_owned(), "true".to_owned())])
    );
    assert_eq!(configured.devices["root"]["size.state"], "6GiB");
    assert!(
        matches!(client.configure("nope", &config, &devices), Err(Error::Refused(why)) if why == "there is no instance nope")
    );

    assert_eq!(client.addresses("devbox").unwrap(), [] as [&str; 0]);
    client.change_state("devbox", "start", false, Duration::from_secs(30)).unwrap();
    assert_eq!(client.instance("devbox").unwrap().unwrap().status, "Running");
    client.change_state("devbox", "stop", true, Duration::from_secs(30)).unwrap();
    let asleep = client.instance("devbox").unwrap().unwrap();
    assert_eq!((asleep.status.as_str(), asleep.stateful), ("Stopped", true));

    // An operation that fails says why.
    system.incus(|incus| {
        incus.failures.insert("start".to_owned(), "Failed to start device \"eth0\"".to_owned())
    });
    let failed =
        client.change_state("devbox", "start", false, Duration::from_secs(30)).unwrap_err();
    assert_eq!(failed.to_string(), "Incus: Failed to start device \"eth0\"");

    client.delete("devbox").unwrap();
    assert_eq!(client.instance("devbox").unwrap(), None);
    let asked = system.incus(|incus| incus.requests.clone());
    assert_eq!(
        &asked[..3],
        [
            "POST /1.0/instances",
            "GET /1.0/operations/op-1/wait?timeout=900",
            "GET /1.0/instances/devbox"
        ]
    );
}

#[test]
fn programs_run_inside_and_files_go_in_and_out() {
    let system = FakeSystem::new();
    system.incus(|incus| {
        incus.instances.insert(
            "devbox".to_owned(),
            FakeInstance::stopped().running().device("eth0", &[("ipv4.address", "10.77.0.10")]),
        );
        incus.inside = Some(Box::new(|command| match command {
            [program, ..] if program == "id" => (0, "uid=1000(dev)\n".to_owned(), String::new()),
            _ => (3, String::new(), "no such thing\n".to_owned()),
        }));
    });
    let client = incus(&system);
    assert_eq!(client.addresses("devbox").unwrap(), ["10.77.0.10"]);

    let exec = Exec {
        command: vec!["id".to_owned(), "-u".to_owned()],
        user: Some((1000, 1000)),
        ..Exec::default()
    };
    let ran = client.exec("devbox", &exec).unwrap();
    assert_eq!((ran.code, ran.stdout.as_str(), ran.stderr.as_str()), (0, "uid=1000(dev)\n", ""));
    let failed = client
        .exec("devbox", &Exec { command: vec!["false".to_owned()], ..Exec::default() })
        .unwrap();
    assert_eq!((failed.code, failed.stderr.as_str()), (3, "no such thing\n"));

    client
        .push_file(
            "devbox",
            "/home/dev/.config/devbox/hub secrets.toml",
            b"FAKE",
            0o600,
            (1000, 1000),
        )
        .unwrap();
    assert_eq!(
        client.pull_file("devbox", "/home/dev/.config/devbox/hub secrets.toml").unwrap().as_deref(),
        Some(&b"FAKE"[..])
    );
    assert_eq!(client.pull_file("devbox", "/nope").unwrap(), None);
    system.incus(|incus| {
        let instance = &incus.instances["devbox"];
        let pushed = &instance.files["/home/dev/.config/devbox/hub secrets.toml"];
        assert_eq!((pushed.mode, pushed.owner), (0o600, (1000, 1000)));
        assert_eq!(
            instance.executed,
            [
                (vec!["id".to_owned(), "-u".to_owned()], Some(1000)),
                (vec!["false".to_owned()], None)
            ]
        );
    });
}

#[test]
fn an_incus_that_is_not_there_is_said_with_its_socket() {
    let system = FakeSystem::new();
    system.incus(|incus| incus.version = None);
    let error = incus(&system).instance("devbox").unwrap_err();
    assert_eq!(
        error.to_string(),
        "Incus is not reachable at /var/lib/incus/unix.socket: connection refused"
    );
}

/// A machine whose only ability is to answer for the Incus socket with a recorded response.
struct Recorded(u16, String);

impl System for Recorded {
    fn unix_http(&self, _: &Path, _: &HttpRequest) -> std::io::Result<HttpResponse> {
        Ok(HttpResponse { status: self.0, body: self.1.clone().into_bytes() })
    }
    fn run(
        &self,
        _: &devbox_core::system::Command,
    ) -> std::io::Result<devbox_core::system::Output> {
        unreachable!()
    }
    fn read(&self, _: &Path) -> std::io::Result<Vec<u8>> {
        unreachable!()
    }
    fn write(&self, _: &Path, _: &[u8]) -> std::io::Result<()> {
        unreachable!()
    }
    fn append(&self, _: &Path, _: &[u8]) -> std::io::Result<()> {
        unreachable!()
    }
    fn remove_file(&self, _: &Path) -> std::io::Result<()> {
        unreachable!()
    }
    fn create_dir_all(&self, _: &Path) -> std::io::Result<()> {
        unreachable!()
    }
    fn set_mode(&self, _: &Path, _: u32) -> std::io::Result<()> {
        unreachable!()
    }
    fn mode(&self, _: &Path) -> Option<u32> {
        unreachable!()
    }
    fn read_dir(&self, _: &Path) -> std::io::Result<Vec<devbox_core::system::Entry>> {
        unreachable!()
    }
    fn exists(&self, _: &Path) -> bool {
        unreachable!()
    }
    fn env(&self, _: &str) -> Option<String> {
        unreachable!()
    }
    fn now(&self) -> std::time::SystemTime {
        unreachable!()
    }
}

#[test]
fn the_reference_responses_of_a_real_incus_are_understood() {
    let fixture = |name: &str| devbox_tests::read(&format!("tests/fixtures/incus/{name}.json"));
    let socket = Path::new(SOCKET);

    let system = Recorded(200, fixture("instance"));
    let instance = Incus::new(&system, socket).instance("devbox").unwrap().unwrap();
    assert_eq!(
        (instance.status.as_str(), instance.kind.as_str(), instance.stateful),
        ("Stopped", "virtual-machine", true)
    );
    assert_eq!(instance.config["migration.stateful"], "true");
    assert_eq!(instance.devices["root"]["size.state"], "9GiB");
    assert_eq!(instance.expanded_devices["eth0"]["ipv4.address"], "10.77.0.10");
    assert_eq!(instance.raw["profiles"], json!(["default"]));

    let system = Recorded(404, fixture("error-not-found"));
    assert_eq!(Incus::new(&system, socket).instance("devbox").unwrap(), None);

    // The wait on an operation that failed: the operation's own error is what is said.
    let failure: Value = serde_json::from_str(&fixture("operation-failure")).unwrap();
    let said = failure["metadata"]["err"].as_str().unwrap().to_owned();
    let mut started = failure.clone();
    started["type"] = json!("async");
    started["operation"] = json!("/1.0/operations/x");
    let system = Recorded(200, started.to_string());
    let error = Incus::new(&system, socket)
        .change_state("devbox", "start", false, Duration::from_secs(5))
        .unwrap_err();
    assert_eq!(error.to_string(), format!("Incus: {said}"));
}

/// Serves one HTTP exchange per connection on a unix socket: answers with `response` and
/// returns what was asked through `asked`.
fn serve(socket: &Path, response: &'static [u8]) -> std::sync::mpsc::Receiver<String> {
    let listener = UnixListener::bind(socket).unwrap();
    let (tell, asked) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        for stream in listener.incoming().flatten() {
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let (mut head, mut length) = (String::new(), 0);
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).unwrap_or(0) == 0 || line == "\r\n" {
                    break;
                }
                if let Some(value) = line.to_lowercase().strip_prefix("content-length: ") {
                    length = value.trim().parse().unwrap();
                }
                head.push_str(&line);
            }
            let mut body = vec![0; length];
            reader.read_exact(&mut body).unwrap();
            let _ = tell.send(format!("{head}\r\n{}", String::from_utf8_lossy(&body)));
            let mut stream = stream;
            let _ = stream.write_all(response);
        }
    });
    asked
}

#[test]
fn the_real_host_speaks_http_over_a_unix_socket() {
    let dir = TempDir::new();
    // Unix socket paths are short; the temporary directory's may not be.
    let socket = PathBuf::from(format!("/tmp/dbx-incus-{}.sock", std::process::id()));
    let _ = std::fs::remove_file(&socket);
    // The answer comes in chunks, as a server that streams sends it.
    let chunks = ["{\"metadata\": {\"en", "vironment\": {\"server_version\": \"6.0.5\"}}}"];
    let mut response =
        "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Type: application/json\r\n\r\n"
            .to_owned();
    for chunk in chunks {
        response = format!("{response}{:x}\r\n{chunk}\r\n", chunk.len());
    }
    response.push_str("0\r\n\r\n");
    let asked = serve(&socket, response.leak().as_bytes());
    let host = Host::new();
    assert_eq!(Incus::new(&host, &socket).version().unwrap(), "6.0.5");
    assert_eq!(
        asked.recv().unwrap(),
        "GET /1.0 HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
    );

    let request = HttpRequest {
        method: "POST".to_owned(),
        path: "/1.0/instances/devbox/files?path=/etc/x".to_owned(),
        headers: vec![("X-Incus-mode".to_owned(), "0600".to_owned())],
        body: b"content".to_vec(),
        timeout: Some(Duration::from_secs(5)),
    };
    let _ = host.unix_http(&socket, &request).unwrap();
    assert_eq!(
        asked.recv().unwrap(),
        "POST /1.0/instances/devbox/files?path=/etc/x HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\nContent-Length: 7\r\nX-Incus-mode: 0600\r\n\r\ncontent"
    );
    let _ = std::fs::remove_file(&socket);
    assert!(host.unix_http(&dir.path().join("nothing.sock"), &request).is_err());
}
