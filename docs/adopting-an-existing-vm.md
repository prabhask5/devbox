# Adopting a VM you already have

You already run a development VM on Incus — built by hand, reachable at `code.example.com` through
Traefik — and want devbox around it without rebuilding it. `devbox up --adopt` keeps the VM's disk
and everything on it and changes only what sleeping, waking and the agents app need. Every file
it replaces is saved first, and `devbox down` puts the host back exactly as it was.

The examples use the instance name `devbox`, the user `dev` and the domain `example.com`.

## What changes, and what does not

| | Before | After |
|---|---|---|
| The VM's disk, users, repositories, running services | yours | untouched |
| Incus settings of the instance | as you set them | `migration.stateful=true`, `size.state` on the root disk (memory + 1 GiB), `boot.autostart=false`; a snapshot schedule is kept |
| Inside the VM | what you installed | plus code-server settings devbox manages, the agents you choose, the hub and its services, hooks for the agents, one line in `~/.ssh/authorized_keys` for the gateway |
| Traefik | a route `code.example.com` → the VM | routes for `code.`, `agents.` and `devbox.example.com` → the gateway; TLS settings and entry point copied from your route |
| Firewall | containers → VM on your ports | the same rule with the hub's port added |
| Other containers, routes and rules | | untouched |

The VM is shut down once for the Incus settings and started again. Anything running in it at that
moment stops, as with any reboot.

## 1. Before you start

- **DNS.** Add records for `agents.example.com` and `devbox.example.com` next to `code.example.com`,
  pointing the same way (through the same proxy, if you use one). `devbox.` is the SSH host: it
  carries SSH inside HTTPS, so it needs nothing but ordinary HTTPS routing.
- **Disk.** Sleeping saves the VM's memory to the storage pool: have its memory plus 1 GiB free.
- **A snapshot,** if you want one more way back: `incus snapshot create devbox before-devbox`.
- **Your dotfiles,** if you keep any: give them a `devbox.toml` first ([dotfiles.md](dotfiles.md),
  "Converting an existing dotfiles repo").

## 2. Deploy the gateway

Follow [deploy-gateway.md](deploy-gateway.md). Until `devbox up` has run, the gateway is healthy and
says "devbox is not set up yet"; it does not touch the VM or your route.

## 3. See the plan

Install the CLI on the server, then ask for the plan without changing anything:

```bash
curl -fsSL https://github.com/prabhask5/devbox/releases/latest/download/install.sh | sudo sh
sudo devbox up --adopt --instance devbox --domain example.com --user dev \
  --dotfiles git@github.com:you/dotfiles --kuma-monitor Devbox --dry-run
```

`--user` is the user in the VM that owns your work; `--dotfiles` and `--kuma-monitor` are optional.
The plan lists every change by step. Check three things in it:

- step 3 says the instance **is kept**, and lists only the three Incus settings;
- step 7 says it **replaces** your Traefik file and that what is there is saved;
- step 8 names your firewall manager and the rule with the hub's port added. If it guessed the
  manager wrong, pass `--firewall host-firewall|ufw|nftables|moi|none`.

## 4. Apply

Run the same command without `--dry-run`. It asks for two passwords — the **wake password** (asked
by the boot screen when the VM sleeps) and the **app password** (for the agents app and for pairing
computers) — and ends by asking `https://code.example.com/__devbox/health` through the public path.

Then:

```bash
sudo devbox doctor      # every check, with what to do about each problem
devbox connect          # the URLs and the commands below
```

## 5. The monitor

If Uptime Kuma (or anything else) watches the VM, point it at
`https://code.example.com/__devbox/health`: HTTP, accepted status 200–299, every 60 seconds. A
sleeping devbox is healthy there, so the monitor no longer has to be paused when the VM is off.
A check that connects to the VM directly would report every sleep as an outage.

## 6. Your phone

Open `https://agents.example.com` in Safari, Share → Add to Home Screen, open it from there, sign in
with the app password, then Settings → Enable notifications.

## 7. Your computers

On each computer (macOS or Linux), once:

```bash
curl -fsSL https://devbox.example.com/setup | sh
```

It installs the `devbox-ssh` client, pairs the computer's key (it asks for the app password) and
writes a `Host devbox` entry. If `~/.ssh/config` already has a `Host devbox` block — a `ProxyJump`
through the server, say — the script comments that block out, marked so that
`curl -fsSL https://devbox.example.com/setup | sh -s -- remove` restores it byte for byte. A
`Host` line that lists `devbox` among other names is left alone, with a warning. After that
`ssh devbox` works from anywhere and wakes the VM.

The key you log in to the server with is let in by the first `devbox up`, so the computer you ran
it from can `ssh devbox` as soon as its config points there. `sudo devbox ssh keys` lists the
paired computers; `sudo devbox ssh keys remove <name>` revokes one.

## 8. Try a sleep and a wake

Close the editor and the agents app and wait for the idle time (15 minutes unless you passed
`--idle`), or stop the VM yourself: `incus stop devbox --stateful`. Then open
`https://code.example.com`: the boot screen shows the wake, and terminals, editors and agent
sessions are where they were.

## Going back

```bash
sudo devbox down --dry-run     # what it will restore
sudo devbox down
```

`down` restores every file `up` replaced (your Traefik route, the firewall rules) byte for byte,
puts the instance's Incus settings back and takes the gateway's key out of the VM. The VM itself,
its disk and what devbox installed inside it stay; nothing in your home directory is removed. Then
remove the gateway container (in Dokploy: delete the application; with Compose:
`docker compose -f deploy/gateway/compose.yml down`), point the monitor back, and on each computer
run the `remove` command above.

## If something is wrong

- `sudo devbox doctor` names the failing check and the fix.
- `curl https://code.example.com/__devbox/health` lists the gateway's problems.
- The editor answers but the agents app does not: the DNS record for `agents.` or the firewall
  rule (doctor checks both).
- `ssh devbox` says `Permission denied (publickey)`: the computer is not paired — run the setup
  command on it, or `sudo devbox ssh keys add <its public key>` on the server.
