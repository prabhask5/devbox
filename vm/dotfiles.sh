#!/usr/bin/env bash
# Applies the owner's dotfiles to the devbox VM, after `install.sh`. `devbox up` clones the
# repository, reads its `devbox.toml`, writes what it says into `dotfiles.env` next to this
# script, and runs it as root. Paths in `dotfiles.env` are relative to the dotfiles' root.
set -euo pipefail
bundle=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
. "$bundle/env"            # DEVBOX_USER
# shellcheck source=/dev/null
. "$bundle/dotfiles.env"   # PACKAGES INSTALL BASHRC GITCONFIG SETTINGS KEYBINDINGS EXTENSIONS INSTRUCTIONS

home=$(getent passwd "$DEVBOX_USER" | cut -d: -f6)
uid=$(id -u "$DEVBOX_USER")
dotfiles=$home/dotfiles
as_user() {
  sudo -u "$DEVBOX_USER" -H env "XDG_RUNTIME_DIR=/run/user/$uid" "PATH=$home/.local/bin:/usr/local/bin:/usr/bin:/bin" "$@"
}
# Links a file of the dotfiles to where a program reads it, so an edit in either place is the same file.
link() { # link <path in dotfiles> <target>
  [ -n "$1" ] && [ -e "$dotfiles/$1" ] || return 0
  as_user mkdir -p "$(dirname "$2")"
  as_user ln -sfn "$dotfiles/$1" "$2"
}

if [ -n "${PACKAGES:-}" ]; then
  echo "== packages: $PACKAGES"
  export DEBIAN_FRONTEND=noninteractive
  # shellcheck disable=SC2086 # a list of package names
  apt-get install -y -qq $PACKAGES >/dev/null
fi

echo "== shell, git, editor, agents"
if [ -n "${BASHRC:-}" ] && ! grep -qF "dotfiles/$BASHRC" "$home/.bashrc" 2>/dev/null; then
  as_user sh -c "printf '%s\n' '[ -f ~/dotfiles/$BASHRC ] && . ~/dotfiles/$BASHRC' >> '$home/.bashrc'"
fi
if [ -n "${GITCONFIG:-}" ]; then
  as_user git config --global --get-all include.path | grep -qxF "$dotfiles/$GITCONFIG" ||
    as_user git config --global --add include.path "$dotfiles/$GITCONFIG"
fi
link "${SETTINGS:-}" "$home/.local/share/code-server/User/settings.json"
link "${KEYBINDINGS:-}" "$home/.local/share/code-server/User/keybindings.json"
if [ -n "${EXTENSIONS:-}" ] && [ -f "$dotfiles/$EXTENSIONS" ]; then
  while read -r extension; do
    case $extension in '' | \#*) continue ;; esac
    as_user code-server --install-extension "$extension" >/dev/null 2>&1 || echo "   $extension is not on Open VSX; skipped"
  done <"$dotfiles/$EXTENSIONS"
fi
if [ -n "${INSTRUCTIONS:-}" ] && [ -f "$dotfiles/$INSTRUCTIONS" ]; then
  # The owner's instructions take the place of devbox's defaults for every agent.
  link "$INSTRUCTIONS" "$home/.codex/AGENTS.md"
  link "$INSTRUCTIONS" "$home/.config/opencode/AGENTS.md"
  as_user sh -c "printf '%s\n' '@~/dotfiles/$INSTRUCTIONS' '@~/dotfiles/${CONTEXT_DIR:-context}/summary.md' > '$home/.claude/CLAUDE.md'"
fi

if [ -n "${INSTALL:-}" ] && [ -f "$dotfiles/$INSTALL" ]; then
  echo "== $INSTALL"
  (cd "$dotfiles" && as_user bash "$dotfiles/$INSTALL")
fi
echo "done"
