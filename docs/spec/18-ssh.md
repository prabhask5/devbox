# 18 — SSH from anywhere

`ssh devbox` works from any computer, through the same public path as the editor and the agents app, and it wakes the VM with progress shown in the terminal. Setting it up takes:

- **on the server:** nothing beyond `devbox up`. The SSH host, its route, the gateway's SSH keys and the VM's side are all part of setup (spec 09).
- **on each computer (macOS or Linux):** one command, `curl -fsSL https://devbox.example.com/setup | sh`. It installs a ~5 MB client, pairs the computer's key and writes one block of SSH config. Nothing else is installed. It doesn't need the `devbox` CLI, cloudflared or a VPN.

```
ssh devbox ─► devbox-ssh proxy ══ wss://devbox.example.com ══► Cloudflare/Traefik ─► gateway :8080 ─► gateway SSH server ─► VM:22
                (ProxyCommand)        (SSH bytes in WebSocket binary frames)              (Host = hosts.ssh)   (auth, wake, relay)
LAN / tunnel ─► gateway 127.0.0.1:2222 ──────────────────────────────────────────────────────────────────────► the same SSH server
```

The transport is a small, open convention (§ Wire protocol), and moi uses the same client and the same setup script for its own main-server SSH (moi spec 16). One tool on the client covers both, and either side can be swapped for anything that speaks the convention.

## Wire protocol: SSH over WebSocket

One TCP byte stream carried in a WebSocket (RFC 6455):

- The client opens `GET wss://<host>/` (path `/`, HTTP/1.1, no subprotocol, no extensions). It may add headers (for example Cloudflare Access service-token headers); nothing else is required.
- After `101 Switching Protocols`, every **binary** message carries the next bytes of the stream in that direction. Message boundaries mean nothing. A text message is a protocol error (close 1003).
- Either side may send pings; the other answers with pongs. The client pings every 20 s, which keeps proxies with idle timeouts (Cloudflare: 100 s) from cutting a quiet session.
- A close frame, or the TCP connection ending, ends the stream in both directions.

This is exactly what Cloudflare Tunnel's `ssh://` and `tcp://` ingress speaks to `cloudflared access ssh`. So `devbox-ssh` also reaches any Cloudflare Tunnel SSH hostname without cloudflared on the client, and the gateway's endpoint could be put behind such a tunnel unchanged (an ingress rule for the SSH host pointing at `ssh://<gateway>:2222`). devbox itself routes the SSH host like `code.` and `agents.`, through the routing provider (09), so it needs no Cloudflare account or API.

## Gateway SSH server

The gateway answers SSH itself (`golang.org/x/crypto/ssh`) on both entries:

- a **WebSocket** upgrade on `hosts.ssh` (default `devbox.<domain>`), and
- the **TCP** listener `gateway.ssh_listen` (published on the host's loopback, spec 11) for local use and for owners who keep their own tunnel.

It is never a raw byte splice: every connection is authenticated by the gateway, and the session is relayed over a second SSH connection to the VM.

### Keys

All under the gateway's state directory, owned by the gateway's uid, created on first start if missing:

| File | Mode | What |
|---|---|---|
| `ssh/host_ed25519_key` (+ `.pub`) | 0600 / 0644 | the gateway's SSH host key: what clients pin |
| `ssh/upstream_ed25519_key` (+ `.pub`) | 0600 / 0644 | the key the gateway logs in to the VM with |
| `ssh/authorized_keys` | 0600 | the keys allowed in, in OpenSSH `authorized_keys` format; the comment is the device name. Options other than none are refused with a warning in the log (devbox does not interpret `command=`, `from=` …) |
| `ssh/keys-used.json` | 0600 | last use per key fingerprint (`gateway-ssh-keys-used`), for `devbox ssh keys` |

The private keys never leave the state directory. `devbox up` reads the `.pub` files, puts the upstream key in the VM user's `~/.ssh/authorized_keys` as `from="<bridge address>",no-X11-forwarding ssh-ed25519 … devbox-gateway`, and records the VM's host key in the host config (`vm.ssh_host_key`), which the gateway pins when it connects (a mismatch refuses the connection and is a health problem). `authorized_keys` is reloaded when it changes (the same 2 s polling as the config, spec 02), and the sessions of a key that is no longer in it are closed then. The files fail closed: a key line that is not `ssh-ed25519`, ECDSA on a NIST curve or RSA of at least 3072 bits is refused with a warning; an `authorized_keys` that group or others may write lets nobody in until it is `chmod 600`; a private key that others can read is made 0600 again at start, and the SSH server stays off if it cannot be. Because the state directory is identity data (moi backs it up), a rebuilt host keeps its host key and its paired devices.

### Accepting a connection

1. Algorithms: key exchange `mlkem768x25519-sha256`, `curve25519-sha256`; ciphers `chacha20-poly1305@openssh.com`, `aes256-gcm@openssh.com`, `aes128-gcm@openssh.com`; host key `ssh-ed25519`. Public-key authentication only, with SHA-2 signatures (`ssh-ed25519`, `ecdsa-sha2-nistp256/384/521`, `rsa-sha2-512/256`; never `ssh-rsa`), at most 6 attempts, 30 s to authenticate, at most 10 connections per source address per minute that do not log in (the WebSocket's `CF-Connecting-IP` or `X-Forwarded-For`, otherwise the peer), and at most 128 connections in the middle of logging in at any time. Every login (source, key fingerprint, device name) and every connection that did not log in is written to the gateway's log.
2. The user name must be the VM user (`provision.user`). Any other name, or a key not in `authorized_keys`, fails authentication: `Permission denied (publickey)`.
3. **Only an authenticated connection may wake the VM.** On success the gateway requests a wake if the VM is not ready (idempotent: it joins a running wake), counts the connection as activity (02), and opens the upstream connection as soon as the VM's sshd sends its `SSH-` banner (≤ 180 s, as before).
4. Upstream: TCP to `vm.address:vm.ssh_port`, the VM's host key checked against `vm.ssh_host_key`, user `provision.user`, the upstream key. One upstream connection per client connection.

### Relay

Everything is relayed generically, so features work without the gateway knowing them:

- **Channels**: each channel the client opens (`session`, `direct-tcpip`, `direct-streamlocal@openssh.com`, …) is opened on the upstream connection with the same type and extra data; accept/reject and the reason go back to the client. Channels the VM opens (`forwarded-tcpip`, `auth-agent@openssh.com`, `forwarded-streamlocal@openssh.com`) are opened towards the client the same way. Data, extended data (stderr), EOF and close are copied both ways.
- **Requests**: channel requests (`pty-req`, `env`, `shell`, `exec`, `subsystem`, `window-change`, `signal`, `exit-status`, `exit-signal`, `auth-agent-req@openssh.com`, …) and global requests (`tcpip-forward`, `cancel-tcpip-forward`, `streamlocal-forward@openssh.com`, …) are forwarded with their `want-reply` answered from the other side. `keepalive@openssh.com` is answered by the gateway; `x11-req` is refused.
- **Before the VM is ready**, channels the client opens are accepted by the gateway and their requests are queued and answered with success at once (a client that waits for each answer before sending the next request would otherwise never ask for its shell). When the upstream connection exists, each queued channel is opened upstream and its requests are replayed in order. A rejection at that point — of the channel, or of its `shell`, `exec` or `subsystem` request — closes the client's channel with the VM's reason on stderr and exit status 255. Connection-wide requests (`tcpip-forward`) wait and are answered by the VM. The VM's own `hostkeys-00@openssh.com` announcement is not passed on: the client knows the gateway's key, not the VM's.
- Client→server bytes count as activity, like proxied HTTP (02). Putting the VM to sleep closes every SSH connection (02).

So shells, `exec`, scp/sftp/rsync, `git` over SSH, `-L`/`-R`/`-D` forwards, agent forwarding and VS Code Remote-SSH work as they do against sshd.

### Boot-aware UX

Shown only when the connection had to wait for the VM. When the VM is awake there is no extra output and no delay beyond the relay.

**Interactive session** (a `pty-req` on the first session channel): the gateway draws the wake on the terminal from the same status the boot screen uses (phases, ETA from past wakes, 02):

```
devbox is asleep — waking it up (about 7s)

  ✓ VM           restored in 5.8s
  ✓ SSH          answers after 0.6s
    Repos        sync continues in the background

  Ready in 6.4s (restored).

```

- One line per phase that matters for SSH (`VM` = the `start` phase, `SSH` = the VM's sshd banner). A line appears when its phase begins and is redrawn in place while it runs — `●`, the time so far and what is left (`● VM  2.1s · about 4s`), at most 10 times a second, with `\r` and `ESC[K` — and gets its final text and its line end when the phase is over. The `Repos` line, which SSH does not wait for, follows when SSH answers. Nothing moves the cursor up, so the screen is right at any width.
- Colour (the boot screen's success/active/failure/muted tokens as ANSI 256 colours 114/75/203/245) and redrawing only when the client's `TERM` is set and not `dumb`; a dumb terminal gets each phase as one line when it is over. Plain characters (`*`, `+`, `-` for `●`, `✓`, `✗`, and `-` for the dashes) when none of `LC_ALL`, `LC_CTYPE`, `LANG` sent with `env` mentions UTF-8.
- When ready: `  Ready in 6.4s (restored).` (`(booted)` after a cold boot) followed by an empty line, then the VM's session starts normally (its motd and prompt follow).
- A cold boot says so: `devbox is off — starting it (about 22s)`, and its VM line ends `started in 18.2s`. A wake already running when you connect says `devbox is waking up (about 1s)` and shows its progress from where it is (`✓ VM  running` when the VM was up already).
- Failure: the step gets `✗` and the gateway's error, then an empty line and `See https://code.example.com for details.`, and the channel ends with exit status 255. Timeout (180 s): `✗ SSH  the VM's SSH server did not answer in 3 minutes`. Only the VM not starting fails the wait: a wake that fails later, because the editor does not come up, does not keep SSH from working.

**Non-interactive** (exec, subsystem, forwards only): two lines on the first session's stderr, nothing on stdout: `devbox: waking the VM (about 7s)…` and `devbox: ready in 6.4s`, or `devbox: wake failed: <error>` with exit status 255. Channels without a session (`ssh -N -L …`) just wait. A connection to an awake VM that then cannot be relayed (the VM refuses the gateway's key, its host key is not the recorded one) gets the same `devbox: wake failed: …` line with what to do, on a terminal as `devbox: …`.

The texts are golden-tested byte for byte (`internal/gateway/sshux_test.go`: transcripts with the ANSI sequences, for a restore, a cold boot, a joined wake, a failure, a timeout, a dumb terminal and a locale without UTF-8, and the lines without a terminal). The screen is a pure function of the wake's state, so the transcripts need no clock.

## The SSH host

`hosts.ssh` (default `devbox.<domain>`) is routed to the gateway like the other two hosts. On it, the gateway serves:

| Request | Auth | Answer |
|---|---|---|
| `GET /` with `Upgrade: websocket` | SSH itself | the SSH stream (§ Wire protocol) |
| `GET /` | none | a small page (HTML for browsers, plain text for curl): what this host is and the setup command. No secrets, no state |
| `GET /setup` | none | the setup script (§ Client setup) with this devbox's values filled in: alias `devbox`, URL `https://<hosts.ssh>`, user, the gateway's host key, `--pair`, client base `https://<hosts.ssh>/__devbox/ssh/client` |
| `GET /__devbox/ssh/client/SHA256SUMS` | none | checksums of the client builds below, each as its `.gz` and as the unpacked program (which is how the setup script knows an installed client is current without downloading it) |
| `GET /__devbox/ssh/client/devbox-ssh-<os>-<arch>.gz` | none | the client for `darwin`/`linux` × `amd64`/`arm64`, gzip-compressed, built from the same commit as the gateway. The gateway serves these six names from the directory `--ssh-client-dir` (`/usr/share/devbox/ssh-client` in the image) and nothing else from it |
| `POST /__devbox/ssh/pair` | app password | adds a key (below) |
| `/__devbox/health`, `/status`, `/events` | as on the other hosts | |

Any other path is 404. The page, the script and both `ssh/pair` answers are golden-tested.

**Pairing** (`POST /__devbox/ssh/pair`, `application/x-www-form-urlencoded`, at most 16 KB): fields `password`, `name` (the computer's name, 1–64 printable characters), `key` (one OpenSSH public key line; `ssh-ed25519`, `ecdsa-sha2-nistp256/384/521` or `ssh-rsa` ≥ 3072 bits). The password is the **agents app password**, checked against the host's copy of its hash (`app_password_hash` in the host secrets; the app password already lets you start agents that run commands in the VM, so pairing grants nothing new). Verification and rate limiting are the login's (02, 14): 6 attempts a minute per address, and the lockout after 50 failures an hour from anywhere. A request whose `Origin` is another site is refused (403). Success appends `<key> <name> paired <YYYY-MM-DD>` to `ssh/authorized_keys` (an identical key already there only gets its name updated) and answers `200 text/plain`, one `key=value` per line so a shell can read it: `ok=1`, `user=<vm user>`, `host=<hosts.ssh>`, `host_key=<gateway host key line>`. Errors answer `error=<message>`: `401` wrong password, `429` too many attempts, `400` a name or key that is not accepted (a key line with options or with anything after it is refused), `403` another site, `413` too large, `405` not a POST, `503` when the host has no app password hash yet. Pairing never wakes the VM, and every pairing and every refusal is in the gateway's log (never the password).

## Client: `devbox-ssh`

`cmd/devbox-ssh`, Go, standard library plus `internal/wsconn`, static, `-trimpath -ldflags "-s -w"`. ≤ 6 MB per build (budget, spec 13). It has no config file and keeps no state.

```
devbox-ssh proxy <https-url> [--header 'Name: value']...   # used as ProxyCommand: stdin/stdout ⇄ the WebSocket
devbox-ssh version
```

- TLS with the system's trust store; honours `HTTPS_PROXY`/`NO_PROXY`.
- Errors go to stderr as one line starting with `devbox-ssh:` and exit 255 (what `ssh` expects from a proxy): DNS, TCP, TLS, and handshake answers that are not 101, each with what to do (`HTTP 403: Cloudflare Access protects <host>; pass its service token with --header`, `HTTP 404: <host> does not serve SSH; check the URL`, `HTTP 502/503: the server is not reachable right now`).
- It works against the gateway and against any Cloudflare Tunnel SSH hostname.

`internal/wsconn` holds the RFC 6455 framing for both sides (the gateway's HTTP proxying stays a byte copy, 02): masking, fragmentation, control frames, close codes, at most 1 MiB per frame read and 32 KiB per frame written. It is fuzzed (`FuzzFrames` in `tests/go/wsconn`, whose seeds run with every `go test`).

## Client setup (`devbox-ssh-setup.sh`)

One POSIX `sh` script (`cmd/devbox-ssh/setup.sh`, `shellcheck`-clean). Its only dependencies are tools a Mac or a Linux desktop already has: `curl`, `ssh`, `ssh-keygen`, `uname`, `gzip`, and `shasum` or `sha256sum`. The gateway serves it at `/setup` with its defaults filled in. Each `ssh-v*` release (spec 11) also attaches it with generic defaults, and moi prints a command that runs it from there (moi spec 16). The same script is how any tool sets up an SSH alias, so all of them follow one contract.

```
sh devbox-ssh-setup.sh add --alias NAME --url https://HOST --user USER --host-key 'ssh-ed25519 AAAA…'
                           [--pair] [--client-base URL] [--header 'Name: value']... [--authorize-hint TEXT] [--yes]
sh devbox-ssh-setup.sh remove --alias NAME
sh devbox-ssh-setup.sh status [--alias NAME]
```

`curl -fsSL https://devbox.example.com/setup | sh` runs `add` with the served defaults (the script has a block of `DEFAULT_…=''` lines at its top, which whoever serves it fills in). Arguments after `sh -s --` replace them; the served alias stays the default, so `… | sh -s -- remove` removes `devbox`. Prompts read from `/dev/tty`, so they work when the script is piped.

### The client contract

What `add` does, in order, each step idempotent. Re-running it changes nothing and says so:

1. **Client.** If `~/.local/bin/devbox-ssh` is missing or its checksum differs from the one in `<client base>/SHA256SUMS` for this OS and architecture, download `devbox-ssh-<os>-<arch>.gz`, check its SHA-256, unpack and install it there (0755). The script refuses to continue when the checksum doesn't match. The SSH config refers to it by absolute path, so `PATH` does not matter.
2. **Key.** Use `~/.ssh/id_ed25519` if it exists; otherwise create it with `ssh-keygen -t ed25519`, letting `ssh-keygen` ask for a passphrase. Every tool that follows this contract uses the same key.
3. **Pair** (only with `--pair`). Ask for the agents app password (hidden; `DEVBOX_APP_PASSWORD` in the environment is used instead when set, for computers without a terminal), `POST <url>/__devbox/ssh/pair` with the computer's name (`scutil --get ComputerName` on macOS, `hostname` elsewhere) and the public key. Stop on an error, showing the server's message.
4. **SSH config.** Make sure `~/.ssh/config` starts with
   ```
   # devbox-ssh-setup: include
   Include ~/.ssh/config.d/*
   ```
   - If an `Include` that already covers `~/.ssh/config.d/*` appears before the first `Host`/`Match` line, it counts.
   - Back up `~/.ssh/config` to `~/.ssh/config.bak.devbox-ssh-setup.<unix time>` before the first change.
   - A `Host` block whose only pattern is the alias is commented out between `# devbox-ssh-setup: disabled begin <alias>` and `# devbox-ssh-setup: disabled end <alias>`, each of its lines prefixed `#- `, because options it sets but the new block doesn't would still apply. A `Host` line listing the alias among other patterns is left alone, with a warning.
   - Nothing else in `~/.ssh/config` is touched.
5. **Alias files.** Write `~/.ssh/config.d/<alias>` and `~/.ssh/known_hosts.d/<alias>` (directories 0700, files 0600):
   ```
   # Managed by devbox-ssh-setup. Undo: sh devbox-ssh-setup.sh remove --alias devbox
   Host devbox
     HostName devbox.example.com
     User dev
     ProxyCommand /Users/me/.local/bin/devbox-ssh proxy https://devbox.example.com
     HostKeyAlias devbox.example.com
     UserKnownHostsFile ~/.ssh/known_hosts.d/devbox
     StrictHostKeyChecking yes
     IdentityFile ~/.ssh/id_ed25519
     IdentitiesOnly yes
     ServerAliveInterval 30
     ServerAliveCountMax 4
     IgnoreUnknown UseKeychain
     UseKeychain yes
     AddKeysToAgent yes
   ```
   - `--header` values are appended to the `ProxyCommand`, shell-quoted.
   - The known-hosts file holds one line, `<HostKeyAlias> <host key>`, from `--host-key`. The host key comes from the server over HTTPS (or, for moi, inside the printed command), so you are never asked to trust an unknown key.
6. **Test.** Run `ssh -o BatchMode=yes <alias> true`; its stderr is shown, so a sleeping devbox shows its wake lines here.
   - On success, print `Done. Connect with: ssh <alias>`.
   - When the key is refused, print `This computer's key is not authorized on <host> yet.`, followed by `--authorize-hint` and the public key when a hint was given, and exit 1. The files written so far stay, so re-running after authorizing completes it.

`remove --alias NAME`:
- deletes `config.d/NAME` and `known_hosts.d/NAME`, and restores the block disabled for that alias;
- deletes the marked `Include` lines only when `config.d` holds no other file written by this script;
- leaves the client binary and the key, and prints the server command that revokes the key (`devbox ssh keys remove <name>`; for moi, its hint).

`status` lists each alias the script manages (the files in `config.d` that start with its `# Managed by devbox-ssh-setup.` line), one per line: alias, host, key fingerprint, and `works` or `does not connect` for `ssh -o BatchMode=yes -o ConnectTimeout=10 <alias> true`.

The script refuses what could write outside its files or into the config of another host: an alias that is not letters, digits, dots, dashes and underscores, a user or header of more than one line, a URL that is not `https://` (plain `http://` only for `127.0.0.1` and `localhost`).

## Server commands

| Command | What |
|---|---|
| `devbox ssh keys` | the paired keys: name, type, fingerprint, when paired, last used (`--json`) |
| `devbox ssh keys add <file\|key line> [--name N]` | add a key without pairing (for a computer that cannot reach the setup URL) |
| `devbox ssh keys remove <name\|fingerprint>` | revoke; open connections that used the key are closed within 2 s (on reload) |
| `devbox ssh status` | the SSH host and its route, the gateway host key fingerprint, whether the VM accepts the upstream key, the number of paired keys, the setup command (`--json`) |

They edit `ssh/authorized_keys` in the gateway's state directory, keeping its owner and mode, and are part of `devbox_core` like every host command, so moi exposes them as `moi devbox ssh …` (spec 12). `devbox connect` prints the setup command. `devbox doctor` checks: the route for the SSH host, DNS for it, a WebSocket handshake to `https://<hosts.ssh>/` answered with an `SSH-2.0-` banner through the public path, the VM accepting the upstream key (`devbox ssh status`), and `vm.ssh_host_key` matching the VM.

## Security

- The SSH host is a public surface protected by SSH public-key authentication. Unauthenticated connections can't wake the VM and never reach it.
- The gateway sees session plaintext, because it terminates SSH. This adds no trust: the Incus socket mounted into it is already root-equivalent over the VM (14). The gateway still uses Incus only through its allowlist; it never needs Incus to read or write VM files for SSH.
- The upstream key only works from the bridge address (`from=`), and only as the VM user.
- `/setup`, the page and the client downloads carry no secrets. The checksums come from the same HTTPS origin, which already has to be trusted to pair. Releases also carry build provenance attestations (11).
- Health problem `the VM rejected the gateway's SSH key` when an upstream login failed in the last 30 minutes, and `the VM's SSH host key changed` on a mismatch (02).

## Performance

- Keystroke round trip through the relay: ≤ 1 ms p99 added on loopback.
- Bulk transfer through the relay (scp of 1 GiB, loopback): ≥ 100 MB/s.
- A connection to an awake VM is relayed after ≤ 5 ms of gateway work (authentication + upstream handshake excluded).
- The client: ≤ 6 MB, ≤ 15 MB RSS while proxying.

All are measured by the `budgets` job (spec 13); `scripts/budgets.sh` learns minimums ending in `_mbps` for the throughput.

## Acceptance

- [x] `internal/wsconn`: framing round-trips, fragmentation, ping/pong, close codes, limits; fuzz target with a seed corpus; interop with a Cloudflare-Tunnel-style peer (a test server that behaves like the tunnel's `ssh://` ingress). (`tests/go/wsconn`)
- [x] Gateway SSH: keys created on first start with the right modes, `authorized_keys` reload, wrong user / unknown key / key with options refused, auth limits, **no wake before authentication**, upstream host-key pinning, VM rejecting the upstream key → health problem. (`tests/go/gateway/ssh_test.go`, fake VM sshd built on `x/crypto/ssh`)
- [x] Relay with the real OpenSSH client (`ssh`, `scp`, `sftp`) against a fake VM sshd: shell with pty, exec with exit status, stderr, scp and sftp both ways, `-L`, `-R`, `-D`, agent forwarding, `ProxyCommand devbox-ssh proxy` over the WebSocket (behind a TLS front) and the TCP listener, queued channels replayed after a wake. (`tests/go/gateway/openssh_test.go`; OpenSSH's client sends no signals, so a signal reaching the command is `TestASignalReachesTheCommandInTheVM` with the Go client.)
- [x] Boot-aware transcripts (restore, cold, joined, failure, timeout, dumb terminal, non-UTF-8, non-interactive) golden byte for byte; awake VM: zero extra bytes. (`internal/gateway/sshux_test.go`, `TestATerminalIsShownTheWakeAndThenTheShell`)
- [x] SSH host endpoints: page, `/setup` with filled-in defaults, client downloads and checksums, pairing (success, duplicate key, bad key types, short RSA, wrong password, rate limit, cross-site Origin) golden. (`tests/go/gateway/sshhost_test.go`, goldens in `tests/fixtures/gateway/`)
- [x] `devbox-ssh`: proxy against the gateway and the tunnel-style peer, `--header`, pings on a quiet stream, each error message golden, exit codes, size budget. (`tests/go/sshclient`; the size of the largest of the four builds is `devbox_ssh_mb` in `budgets.toml`, measured by the `budgets` job.)
- [x] Setup script under `dash` and `bash` on Linux and under `/bin/sh` on a macOS runner (CI job `ssh-client`), against a fake gateway and a fake `$HOME`: fresh machine, existing key, existing covering `Include`, conflicting `Host devbox` block, alias among other patterns, re-run unchanged, refused key with hint, checksum mismatch refused, `remove` restoring `~/.ssh/config` byte for byte, `status`. Generated files golden.
- [x] `devbox up` writes the SSH route and installs the upstream key and the recorded VM host key; `--dry-run` golden; `down` removes them; `devbox ssh keys|status` golden; doctor checks. (`tests/rust/tests/setup.rs`, `tests/rust/tests/embedding.rs`, goldens in `tests/fixtures/setup/`)
- [x] Budgets above in `budgets.toml`, measured in the `budgets` job. (`tests/perf/ssh`)
