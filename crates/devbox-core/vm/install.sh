#!/usr/bin/env bash
# Provisions the devbox VM. `devbox up` pushes this directory into the VM and runs this script
# as root; running it again changes nothing that is already as it should be.
#
# Next to it: `env` (what to install, written by `devbox up`), `units/`, `code-server/`,
# `agents/`, `hub.toml`, `hub.secrets.toml`, optionally `bin/` (devbox-hub and devbox, when
# they come from the host instead of a release) and `github-token`. `DEVBOX_ROOT` is put in
# front of the one path outside the user's home, for the tests.
set -euo pipefail
bundle=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
. "$bundle/env"   # DEVBOX_USER CODE_PORT AGENTS RELEASE REPO

home=$(getent passwd "$DEVBOX_USER" | cut -d: -f6)
uid=$(id -u "$DEVBOX_USER")
[ -n "$home" ] || { echo "there is no user $DEVBOX_USER in this VM" >&2; exit 1; }
as_user() {
  sudo -u "$DEVBOX_USER" -H env "XDG_RUNTIME_DIR=/run/user/$uid" "PATH=$home/.local/bin:/usr/local/bin:/usr/bin:/bin" "$@"
}
# The bundle is root's alone (it holds secrets), so the user cannot read it: its files are
# read by root and written by the user. give -m <mode> <file...> <target file or directory>
give() {
  local mode=$2 target file dest; shift 2
  target=${@: -1}
  for file in "${@:1:$#-1}"; do
    dest=$target
    [ ! -d "$target" ] || dest=${target%/}/$(basename "$file")
    # Written next to its place, the user's alone until it has its mode, then moved there: a
    # program that is running can be replaced, and nothing ever sees half a file.
    as_user sh -c 'umask 077 && cat >"$1.new"' sh "$dest" <"$file"
    chmod "$mode" "$dest.new"
    as_user mv -f "$dest.new" "$dest"
  done
}
step() { echo "== $*"; }

step "packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git gh curl wget unzip jq ripgrep fd-find tmux htop build-essential pkg-config libssl-dev ca-certificates sudo >/dev/null

step "code-server"
command -v code-server >/dev/null || curl -fsSL https://code-server.dev/install.sh | sh >/dev/null
as_user mkdir -p "$home/.config/code-server" "$home/.local/share/code-server/User"
config=$home/.config/code-server/config.yaml
# The password is made once and kept: the gateway is what guards the door, this is the lock behind it.
password=$(sed -n 's/^password: //p' "$config" 2>/dev/null || true)
[ -n "$password" ] || password=$(head -c 24 /dev/urandom | base64 | tr -d '/+=')
as_user tee "$config" >/dev/null <<CONFIG
bind-addr: 0.0.0.0:$CODE_PORT
auth: password
password: $password
cert: false
CONFIG
chmod 600 "$config"
for file in settings.json keybindings.json; do
  target=$home/.local/share/code-server/User/$file
  [ -e "$target" ] || [ ! -f "$bundle/code-server/$file" ] || give -m 644 "$bundle/code-server/$file" "$target"
done
systemctl enable --now "code-server@$DEVBOX_USER" >/dev/null 2>&1
systemctl restart "code-server@$DEVBOX_USER"

step "agents: ${AGENTS:-none}"
for agent in $AGENTS; do
  case $agent in
    claude)
      as_user sh -c 'command -v claude >/dev/null' || as_user bash -c 'curl -fsSL https://claude.ai/install.sh | bash' >/dev/null ;;
    codex | opencode)
      command -v npm >/dev/null || apt-get install -y -qq nodejs npm >/dev/null
      package=@openai/codex; [ "$agent" = opencode ] && package=opencode-ai
      as_user sh -c "command -v $agent >/dev/null" || as_user npm install --global --silent --prefix "$home/.local" "$package" ;;
    *) echo "unknown agent: $agent" >&2; exit 1 ;;
  esac
done

step "devbox $RELEASE"
releases=$home/.local/lib/devbox/releases
as_user mkdir -p "$releases/$RELEASE" "$home/.local/bin" "$home/.config/devbox" "$home/.local/state/devbox" "$home/code"
if [ -d "$bundle/bin" ]; then
  give -m 755 "$bundle/bin/devbox-hub" "$bundle/bin/devbox" "$releases/$RELEASE/"
elif [ ! -x "$releases/$RELEASE/devbox-hub" ]; then
  arch=$(dpkg --print-architecture)
  url=https://github.com/$REPO/releases/download/$RELEASE/devbox-vm-linux-$arch.tar.gz
  tmp=$(mktemp -d)
  curl -fsSL "$url" -o "$tmp/vm.tar.gz"
  curl -fsSL "$url.sha256" -o "$tmp/vm.tar.gz.sha256"
  (cd "$tmp" && echo "$(cut -d' ' -f1 vm.tar.gz.sha256)  vm.tar.gz" | sha256sum --check --quiet)
  as_user tar -xzf "$tmp/vm.tar.gz" -C "$releases/$RELEASE" devbox-hub devbox
  rm -rf "$tmp"
fi
as_user ln -sfn "releases/$RELEASE" "$home/.local/lib/devbox/current"
for binary in devbox-hub devbox; do
  as_user ln -sf "$home/.local/lib/devbox/current/$binary" "$home/.local/bin/$binary"
done
# `agent` is the hub's session command under the name people type.
as_user tee "$home/.local/bin/agent" >/dev/null <<'AGENT'
#!/bin/sh
exec devbox-hub agent "$@"
AGENT
chmod 755 "$home/.local/bin/agent"

step "hub configuration"
give -m 644 "$bundle/hub.toml" "$home/.config/devbox/hub.toml"
secrets=$home/.config/devbox/hub.secrets.toml
# The hub adds its web push keys to this file on its first start; they are the devices' subscriptions.
kept=$(grep -E '^(# Web push keys|vapid_(public|private)_key = )' "$secrets" 2>/dev/null || true)
give -m 600 "$bundle/hub.secrets.toml" "$secrets"
[ -z "$kept" ] || printf '\n%s\n' "$kept" >>"$secrets"
rm -f "$bundle/hub.secrets.toml"

step "agent hooks and instructions"
as_user "$home/.local/bin/devbox-hub" hooks install
give -m 644 "$bundle/agents/AGENTS.md" "$home/.config/devbox/AGENTS.md"
as_user mkdir -p "$home/.claude" "$home/.codex" "$home/.config/opencode"
[ -e "$home/.claude/CLAUDE.md" ] || as_user sh -c "printf '@~/.config/devbox/AGENTS.md\n' > '$home/.claude/CLAUDE.md'"
for link in "$home/.codex/AGENTS.md" "$home/.config/opencode/AGENTS.md"; do
  [ -e "$link" ] || as_user ln -s "$home/.config/devbox/AGENTS.md" "$link"
done

step "clock"
# After a restore the VM's clock is behind; the hub sets it, and may do exactly that as root.
sudoers=${DEVBOX_ROOT:-}/etc/sudoers.d/devbox
rm -f "$sudoers"
cat >"$sudoers" <<SUDO
$DEVBOX_USER ALL=(root) NOPASSWD: /usr/bin/date -s @*, /usr/bin/systemctl restart systemd-timesyncd
SUDO
chmod 440 "$sudoers"

if [ -f "$bundle/github-token" ]; then
  step "GitHub"
  as_user gh auth login --with-token <"$bundle/github-token"
  as_user gh auth setup-git
  rm -f "$bundle/github-token"
fi

step "services"
loginctl enable-linger "$DEVBOX_USER"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  as_user systemctl --user show-environment >/dev/null 2>&1 && break
  sleep 1
done
as_user mkdir -p "$home/.config/systemd/user"
give -m 644 "$bundle"/units/* "$home/.config/systemd/user/"
as_user systemctl --user daemon-reload
as_user systemctl --user enable --now devbox-tmux.service devbox-hub.service devbox-context.timer >/dev/null 2>&1
as_user systemctl --user restart devbox-hub.service
# The developer context once now, so that agents have it before the timer first fires.
as_user systemctl --user start devbox-context.service >/dev/null 2>&1 || true
echo "done"
