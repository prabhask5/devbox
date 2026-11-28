#!/bin/sh
# devbox-ssh-setup: makes `ssh <alias>` work on this computer through an SSH-over-WebSocket
# host (a devbox gateway, a Cloudflare Tunnel SSH hostname): installs the devbox-ssh client,
# makes sure there is a key, optionally pairs it, and writes one block of SSH config.
#
#   sh devbox-ssh-setup.sh add --alias NAME --url https://HOST --user USER --host-key 'ssh-ed25519 AAAA…'
#                              [--pair] [--client-base URL] [--header 'Name: value']...
#                              [--authorize-hint TEXT] [--yes]
#   sh devbox-ssh-setup.sh remove --alias NAME
#   sh devbox-ssh-setup.sh status [--alias NAME]
#
# What it writes, and nothing else:
#   ~/.local/bin/devbox-ssh             the client
#   ~/.ssh/id_ed25519(.pub)             only when there is no such key yet
#   ~/.ssh/config                       two lines at its top that include ~/.ssh/config.d/*
#   ~/.ssh/config.d/NAME                the Host block
#   ~/.ssh/known_hosts.d/NAME           the server's host key
# `remove` takes all of it back except the client and the key.
set -eu

# What a server fills in when it serves this script (`curl …/setup | sh`). Arguments replace
# them; the alias stays the default for `remove` and `status`.
DEFAULT_ALIAS=''
DEFAULT_URL=''
DEFAULT_USER=''
DEFAULT_HOST_KEY=''
DEFAULT_PAIR=''
DEFAULT_CLIENT_BASE=''
DEFAULT_REVOKE_HINT=''

MARK='# devbox-ssh-setup:'
MANAGED='# Managed by devbox-ssh-setup.'

say() { printf '%s\n' "$*"; }
warn() { printf 'devbox-ssh-setup: %s\n' "$*" >&2; }
die() {
  warn "$*"
  exit 1
}

usage() {
  cat >&2 <<'USAGE'
usage: sh devbox-ssh-setup.sh add --alias NAME --url https://HOST --user USER --host-key 'ssh-ed25519 AAAA…'
                                  [--pair] [--client-base URL] [--header 'Name: value']...
                                  [--authorize-hint TEXT] [--yes]
       sh devbox-ssh-setup.sh remove --alias NAME
       sh devbox-ssh-setup.sh status [--alias NAME]
USAGE
  exit 2
}

need() {
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || die "this needs $tool, which is not installed"
  done
}

# sha256 <file>: its SHA-256, with whichever tool this system has.
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    die "this needs sha256sum or shasum, and neither is installed"
  fi
}

# quote <text>: the text as one word of a shell command.
quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

action=add
case ${1:-} in
  add | remove | status)
    action=$1
    shift
    ;;
  -h | --help | help) usage ;;
esac

alias_name=$DEFAULT_ALIAS
url=$DEFAULT_URL
user=$DEFAULT_USER
host_key=$DEFAULT_HOST_KEY
pair=$DEFAULT_PAIR
client_base=$DEFAULT_CLIENT_BASE
authorize_hint=''
yes=''
alias_given=''
proxy_headers=''
NL='
'

while [ $# -gt 0 ]; do
  case $1 in
    --alias | --url | --user | --host-key | --client-base | --header | --authorize-hint)
      [ $# -ge 2 ] || usage
      case $1 in
        --alias)
          alias_name=$2
          alias_given=1
          ;;
        --url) url=$2 ;;
        --user) user=$2 ;;
        --host-key) host_key=$2 ;;
        --client-base) client_base=$2 ;;
        --authorize-hint) authorize_hint=$2 ;;
        --header)
          case $2 in
            *"$NL"*) die "--header takes one line" ;;
            ?*:*) ;;
            *) die "--header takes 'Name: value'" ;;
          esac
          proxy_headers="$proxy_headers --header $(quote "$2")"
          ;;
      esac
      shift 2
      ;;
    --pair)
      pair=1
      shift
      ;;
    --yes)
      yes=1
      shift
      ;;
    *) usage ;;
  esac
done

[ -n "${HOME:-}" ] || die "HOME is not set"
ssh_dir=$HOME/.ssh
config=$ssh_dir/config
config_d=$ssh_dir/config.d
known_d=$ssh_dir/known_hosts.d
key=$ssh_dir/id_ed25519
client=$HOME/.local/bin/devbox-ssh

valid_alias() {
  case $1 in
    '' | .* | *[!A-Za-z0-9._-]*) return 1 ;;
  esac
}

# ask <prompt>: a line typed on the terminal, which is there even when this script is piped in.
ask() {
  [ -r /dev/tty ] || return 1
  printf '%s' "$1" >/dev/tty
  IFS= read -r answer </dev/tty || return 1
  printf '%s' "$answer"
}

# ask_secret <prompt>: the same, without showing what is typed.
ask_secret() {
  [ -r /dev/tty ] || return 1
  printf '%s' "$1" >/dev/tty
  saved=$(stty -g </dev/tty 2>/dev/null || true)
  stty -echo </dev/tty 2>/dev/null || true
  IFS= read -r answer </dev/tty || answer=''
  [ -z "$saved" ] || stty "$saved" </dev/tty 2>/dev/null || true
  printf '\n' >/dev/tty
  printf '%s' "$answer"
}

# ---------------------------------------------------------------------------------------------
# ~/.ssh/config: the Include at its top, and a block of the same alias that is in the way.

# includes_config_d: whether an Include before the first Host or Match covers ~/.ssh/config.d/*.
includes_config_d() {
  [ -f "$config" ] || return 1
  awk -v home="$HOME" '
    BEGIN { found = 0 }
    {
      line = $0
      sub(/^[ \t]+/, "", line)
      low = tolower(line)
      if (low ~ /^(host|match)([ \t=]|$)/) exit
      if (low ~ /^include[ \t=]/) {
        n = split(line, words, /[ \t=]+/)
        for (i = 2; i <= n; i++) {
          w = words[i]
          gsub(/"/, "", w)
          if (w == "~/.ssh/config.d/*" || w == "config.d/*" || w == home "/.ssh/config.d/*") found = 1
        }
      }
    }
    END { exit found ? 0 : 1 }
  ' "$config"
}

backed_up=''
# backup: a copy of ~/.ssh/config as it is, before the first change of this run.
backup() {
  [ -z "$backed_up" ] || return 0
  backed_up=1
  [ -f "$config" ] || return 0
  cp -p "$config" "$config.bak.devbox-ssh-setup.$(date +%s)"
}

# replace_config <file>: put new content in ~/.ssh/config, keeping its mode.
replace_config() {
  if [ -f "$config" ]; then
    cat "$1" >"$config"
  else
    (umask 077 && cat "$1" >"$config")
  fi
  rm -f "$1"
}

ensure_include() {
  if includes_config_d; then
    return 0
  fi
  backup
  tmp=$config.devbox-ssh-setup.$$
  {
    printf '%s include\nInclude ~/.ssh/config.d/*\n' "$MARK"
    [ ! -f "$config" ] || cat "$config"
  } >"$tmp"
  replace_config "$tmp"
  say "Added an Include of ~/.ssh/config.d/* at the top of ~/.ssh/config."
}

# conflicting_block <alias>: 0 when a Host block names only the alias, 2 when the alias is one
# of several patterns of a Host line, 1 when it is nowhere.
conflicting_block() {
  [ -f "$config" ] || return 1
  awk -v name="$1" -v mark="$MARK" '
    BEGIN { code = 1; skip = 0 }
    index($0, mark " disabled begin ") == 1 { skip = 1 }
    index($0, mark " disabled end ") == 1 { skip = 0; next }
    skip { next }
    {
      line = $0
      sub(/^[ \t]+/, "", line)
      if (tolower(line) !~ /^host([ \t=]|$)/) next
      n = split(line, words, /[ \t=]+/)
      has = 0
      for (i = 2; i <= n; i++) if (words[i] == name) has = 1
      if (has && n == 2) { code = 0; exit }
      if (has && code == 1) code = 2
    }
    END { exit code }
  ' "$config"
}

# disable_block <alias>: comment out the Host block that names only the alias, between marks.
disable_block() {
  backup
  tmp=$config.devbox-ssh-setup.$$
  awk -v name="$1" -v mark="$MARK" '
    function is_start(line,    low) {
      sub(/^[ \t]+/, "", line)
      low = tolower(line)
      return low ~ /^(host|match)([ \t=]|$)/
    }
    function only_alias(line,    n, words) {
      sub(/^[ \t]+/, "", line)
      if (tolower(line) !~ /^host([ \t=]|$)/) return 0
      n = split(line, words, /[ \t=]+/)
      return n == 2 && words[2] == name
    }
    {
      if (inside && is_start($0)) {
        print mark " disabled end " name
        inside = 0
      }
      if (!inside && !done && only_alias($0)) {
        print mark " disabled begin " name
        inside = 1
        done = 1
      }
      if (inside) print "#- " $0
      else print
    }
    END { if (inside) print mark " disabled end " name }
  ' "$config" >"$tmp"
  replace_config "$tmp"
  say "A Host $1 block in ~/.ssh/config was in the way: it is commented out, and \`remove\` brings it back."
}

# restore_block <alias>: undo disable_block.
restore_block() {
  [ -f "$config" ] || return 0
  grep -q "^$MARK disabled begin $1\$" "$config" || return 0
  tmp=$config.devbox-ssh-setup.$$
  awk -v name="$1" -v mark="$MARK" '
    $0 == mark " disabled begin " name { inside = 1; next }
    $0 == mark " disabled end " name { inside = 0; next }
    inside && index($0, "#- ") == 1 { print substr($0, 4); next }
    inside && $0 == "#-" { print ""; next }
    { print }
  ' "$config" >"$tmp"
  replace_config "$tmp"
  say "Restored the Host $1 block that was in ~/.ssh/config before."
}

# others_managed <alias>: whether config.d holds an alias of this script other than the one named.
others_managed() {
  [ -d "$config_d" ] || return 1
  for file in "$config_d"/*; do
    [ -f "$file" ] || continue
    [ "$(basename "$file")" != "$1" ] || continue
    if grep -q "^$MANAGED" "$file" 2>/dev/null; then
      return 0
    fi
  done
  return 1
}

remove_include() {
  [ -f "$config" ] || return 0
  grep -q "^$MARK include\$" "$config" || return 0
  tmp=$config.devbox-ssh-setup.$$
  awk -v mark="$MARK" '
    $0 == mark " include" { drop = 1; next }
    drop == 1 { drop = 0; if ($0 == "Include ~/.ssh/config.d/*") next }
    { print }
  ' "$config" >"$tmp"
  replace_config "$tmp"
  # A config that this script made, with nothing else in it, is not left behind empty.
  [ -s "$config" ] || rm -f "$config"
}

# ---------------------------------------------------------------------------------------------

platform() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case $arch in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) die "there is no devbox-ssh for this kind of processor ($arch)" ;;
  esac
  case $os in
    darwin | linux) ;;
    *) die "there is no devbox-ssh for $os: this works on macOS and Linux" ;;
  esac
  printf '%s-%s' "$os" "$arch"
}

install_client() {
  [ -n "$client_base" ] || die "where the client is downloaded from is not known: pass --client-base URL"
  build=devbox-ssh-$(platform)
  work=$(mktemp -d)
  # shellcheck disable=SC2064 # the directory of this run, known now
  trap "rm -rf '$work'" EXIT
  curl -fsSL "$client_base/SHA256SUMS" -o "$work/SHA256SUMS" || die "could not download $client_base/SHA256SUMS"
  sum_of() { awk -v name="$1" '{ file = $2; sub(/^\*/, "", file) } file == name { print $1; exit }' "$work/SHA256SUMS"; }
  want_binary=$(sum_of "$build")
  want_archive=$(sum_of "$build.gz")
  [ -n "$want_archive" ] || die "$client_base/SHA256SUMS does not list $build.gz"
  if [ -x "$client" ] && [ -n "$want_binary" ] && [ "$(sha256 "$client")" = "$want_binary" ]; then
    say "devbox-ssh is installed and current."
    return 0
  fi
  curl -fsSL "$client_base/$build.gz" -o "$work/$build.gz" || die "could not download $client_base/$build.gz"
  [ "$(sha256 "$work/$build.gz")" = "$want_archive" ] ||
    die "$build.gz does not match its checksum: nothing was installed. Try again; if it happens again, the download is being tampered with or the server is broken"
  gzip -dc "$work/$build.gz" >"$work/$build"
  if [ -n "$want_binary" ] && [ "$(sha256 "$work/$build")" != "$want_binary" ]; then
    die "$build does not match its checksum: nothing was installed"
  fi
  mkdir -p "$(dirname "$client")"
  chmod 755 "$work/$build"
  mv -f "$work/$build" "$client"
  say "Installed devbox-ssh in ~/.local/bin."
}

ensure_key() {
  if [ -f "$key" ]; then
    [ -f "$key.pub" ] || ssh-keygen -y -f "$key" >"$key.pub"
    return 0
  fi
  mkdir -p "$ssh_dir"
  chmod 700 "$ssh_dir"
  say "This computer has no SSH key yet (~/.ssh/id_ed25519): making one."
  if [ -n "$yes" ]; then
    ssh-keygen -q -t ed25519 -N '' -f "$key" </dev/null
  else
    ssh-keygen -t ed25519 -f "$key" </dev/tty
  fi
}

computer_name() {
  name=''
  if command -v scutil >/dev/null 2>&1; then
    name=$(scutil --get ComputerName 2>/dev/null || true)
  fi
  [ -n "$name" ] || name=$(hostname 2>/dev/null || true)
  [ -n "$name" ] || name=computer
  printf '%s' "$name" | tr -c 'A-Za-z0-9._ -' '-' | cut -c1-64
}

pair_key() {
  password=${DEVBOX_APP_PASSWORD:-}
  if [ -z "$password" ]; then
    password=$(ask_secret "Password of the agents app on $host: ") || die "pairing asks for the agents app password, and there is no terminal to ask on: set DEVBOX_APP_PASSWORD"
  fi
  [ -n "$password" ] || die "pairing needs the agents app password"
  answer=$work/pair
  code=$(curl -sS -o "$answer" -w '%{http_code}' -X POST \
    --data-urlencode "password=$password" --data-urlencode "name=$(computer_name)" --data-urlencode "key=$(cat "$key.pub")" \
    "$url/__devbox/ssh/pair") || die "could not reach $url to pair"
  if [ "$code" != 200 ] || ! grep -q '^ok=1$' "$answer"; then
    reason=$(sed -n 's/^error=//p' "$answer" | head -n 1)
    die "pairing was refused: ${reason:-HTTP $code}"
  fi
  say "Paired this computer's key with $host."
}

write_alias() {
  mkdir -p "$ssh_dir" "$config_d" "$known_d"
  chmod 700 "$ssh_dir" "$config_d" "$known_d"
  status=0
  conflicting_block "$alias_name" || status=$?
  case $status in
    0) disable_block "$alias_name" ;;
    2) warn "a Host line in ~/.ssh/config lists $alias_name among other names; it is left as it is, and its options may still apply to $alias_name" ;;
  esac
  ensure_include
  tmp=$config_d/.$alias_name.$$
  (
    umask 077
    cat >"$tmp" <<BLOCK
$MANAGED Undo: sh devbox-ssh-setup.sh remove --alias $alias_name
Host $alias_name
  HostName $host
  User $user
  ProxyCommand $client proxy $url$proxy_headers
  HostKeyAlias $host
  UserKnownHostsFile ~/.ssh/known_hosts.d/$alias_name
  StrictHostKeyChecking yes
  IdentityFile ~/.ssh/id_ed25519
  IdentitiesOnly yes
  ServerAliveInterval 30
  ServerAliveCountMax 4
  IgnoreUnknown UseKeychain
  UseKeychain yes
  AddKeysToAgent yes
BLOCK
    printf '%s %s\n' "$host" "$host_key" >"$known_d/.$alias_name.$$"
  )
  if [ -f "$config_d/$alias_name" ] && cmp -s "$tmp" "$config_d/$alias_name" && cmp -s "$known_d/.$alias_name.$$" "$known_d/$alias_name" 2>/dev/null; then
    rm -f "$tmp" "$known_d/.$alias_name.$$"
    say "The SSH config for $alias_name is already as it should be."
  else
    mv -f "$tmp" "$config_d/$alias_name"
    mv -f "$known_d/.$alias_name.$$" "$known_d/$alias_name"
    say "Wrote ~/.ssh/config.d/$alias_name and ~/.ssh/known_hosts.d/$alias_name."
  fi
}

try_it() {
  errors=$work/ssh-errors
  if ssh -o BatchMode=yes "$alias_name" true 2>"$errors"; then
    cat "$errors" >&2
    say "Done. Connect with: ssh $alias_name"
    return 0
  fi
  cat "$errors" >&2
  if grep -qi 'permission denied' "$errors"; then
    say "This computer's key is not authorized on $host yet."
    if [ -n "$authorize_hint" ]; then
      say "$authorize_hint"
      say ""
      cat "$key.pub"
    fi
    exit 1
  fi
  die "ssh $alias_name did not work (see above). What was written stays; run this again when it is fixed"
}

do_add() {
  need curl ssh ssh-keygen uname gzip awk
  valid_alias "$alias_name" || die "--alias must be a name of letters, digits, dots, dashes and underscores"
  case $url in
    https://?*) ;;
    http://127.0.0.1:* | http://localhost:*) ;; # a server on this computer, as in the tests of this script
    *) die "--url must be the https:// address of the SSH host" ;;
  esac
  [ -n "$user" ] || die "--user is needed: the user to log in as"
  case $host_key in
    ssh-ed25519\ * | ecdsa-sha2-*\ * | ssh-rsa\ * | rsa-sha2-*\ *) ;;
    *) die "--host-key must be the server's public host key, like 'ssh-ed25519 AAAA…'" ;;
  esac
  case $alias_name$user in *[[:space:]]*) die "the alias and the user are single words" ;; esac
  host=${url#*://}
  host=${host%%/*}
  host=${host%%:*}
  install_client
  ensure_key
  [ -z "$pair" ] || pair_key
  write_alias
  try_it
}

do_remove() {
  valid_alias "$alias_name" || die "--alias NAME says which alias to remove"
  removed=''
  for file in "$config_d/$alias_name" "$known_d/$alias_name"; do
    if [ -f "$file" ]; then
      rm -f "$file"
      removed=1
    fi
  done
  restore_block "$alias_name"
  others_managed "$alias_name" || remove_include
  rmdir "$config_d" "$known_d" 2>/dev/null || true
  if [ -n "$removed" ]; then
    say "Removed the SSH config for $alias_name."
  else
    say "There was no SSH config for $alias_name."
  fi
  say "The client (~/.local/bin/devbox-ssh) and your key (~/.ssh/id_ed25519) are left where they are."
  if [ -n "$DEFAULT_REVOKE_HINT" ]; then
    say "To take this computer's key off the server: $DEFAULT_REVOKE_HINT"
  else
    say "To take this computer's key off the server, on the server: devbox ssh keys remove <name>"
  fi
}

do_status() {
  found=''
  if [ -d "$config_d" ]; then
    for file in "$config_d"/*; do
      [ -f "$file" ] || continue
      grep -q "^$MANAGED" "$file" 2>/dev/null || continue
      name=$(basename "$file")
      [ -z "$alias_given" ] || [ "$alias_name" = "$name" ] || continue
      found=1
      target=$(sed -n 's/^  HostName //p' "$file" | head -n 1)
      fingerprint='no key'
      if [ -f "$key.pub" ]; then
        fingerprint=$(ssh-keygen -lf "$key.pub" 2>/dev/null | cut -d' ' -f2)
      fi
      if ssh -o BatchMode=yes -o ConnectTimeout=10 "$name" true >/dev/null 2>&1; then
        works='works'
      else
        works='does not connect'
      fi
      say "$name  $target  $fingerprint  $works"
    done
  fi
  [ -n "$found" ] || say "No SSH alias on this computer was set up by devbox-ssh-setup."
}

case $action in
  add) do_add ;;
  remove) do_remove ;;
  status) do_status ;;
esac
