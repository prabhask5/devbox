#!/usr/bin/env bash
# Rebuilds the devbox setup from this repo. Safe to re-run.
set -euo pipefail
D="$(cd "$(dirname "$0")" && pwd)"
echo "== packages =="
sudo apt-get update -qq
sudo apt-get install -y -qq git gh curl wget unzip jq ripgrep fd-find tmux htop build-essential pkg-config libssl-dev ca-certificates >/dev/null
echo "== Node + pnpm (mise) =="
command -v mise >/dev/null || curl -fsSL https://mise.run | sh
export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$PATH"
mise use -g node@lts pnpm@latest
echo "== Rust + WASM target =="
command -v rustup >/dev/null || curl -fsSL https://sh.rustup.rs | sh -s -- -y --no-modify-path
rustup target add wasm32-unknown-unknown
echo "== Coding agents (Claude Code, Codex, opencode) =="
command -v claude >/dev/null || curl -fsSL https://claude.ai/install.sh | bash
mise use -g npm:@openai/codex@latest npm:opencode-ai@latest
echo "== link config =="
mkdir -p ~/.local/share/code-server/User
ln -sf "$D/code-server/settings.json" ~/.local/share/code-server/User/settings.json
ln -sf "$D/code-server/keybindings.json" ~/.local/share/code-server/User/keybindings.json
grep -q 'dotfiles/bashrc' ~/.bashrc || echo '[ -f ~/dotfiles/bashrc ] && . ~/dotfiles/bashrc' >> ~/.bashrc
git config --global include.path "$D/gitconfig"
echo "== agent hooks + global instructions =="
mkdir -p ~/.claude ~/.codex ~/.config/opencode/plugins ~/.local/bin
[ -s ~/.claude/settings.json ] || echo '{}' > ~/.claude/settings.json
# merge our hooks into Claude's settings (Claude rewrites this file, so it can't be a symlink)
jq -s '.[0] * .[1]' ~/.claude/settings.json "$D/agents/claude-hooks.json" > ~/.claude/settings.json.tmp && mv ~/.claude/settings.json.tmp ~/.claude/settings.json
ln -sf "$D/agents/CLAUDE.md" ~/.claude/CLAUDE.md
ln -sf "$D/agents/AGENTS.md" ~/.codex/AGENTS.md
ln -sf "$D/agents/codex-hooks.json" ~/.codex/hooks.json
ln -sf "$D/agents/AGENTS.md" ~/.config/opencode/AGENTS.md
ln -sf "$D/agents/opencode-devbox.js" ~/.config/opencode/plugins/devbox.js
for b in agent devbox-sync devbox-context devbox-agent-event; do ln -sf "$D/devbox/bin/$b" ~/.local/bin/$b; done

echo "== agents hub + background services =="
(cd "$D/devbox/hub" && mise exec node@lts -- npm ci --omit=dev --silent)
sudo loginctl enable-linger "$USER"            # user services run without a login session
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
for _ in 1 2 3 4 5; do systemctl --user show-environment >/dev/null 2>&1 && break; sleep 1; done
mkdir -p ~/.config/systemd/user
for u in "$D"/devbox/systemd/*; do ln -sf "$u" ~/.config/systemd/user/; done
systemctl --user daemon-reload
systemctl --user enable --now devbox-tmux.service devbox-hub.service devbox-context.timer
systemctl --user restart devbox-hub.service
jq -e '.passwordHash' ~/.config/devbox/hub.json >/dev/null 2>&1 || echo "  -> set the phone app password once: agent passwd"

echo "== code-server extensions =="
while read -r ext; do
  { [ -z "$ext" ] || [[ $ext == \#* ]]; } && continue
  code-server --install-extension "$ext" >/dev/null 2>&1 && echo "  ok   $ext" || echo "  skip $ext (not on Open VSX)"
done < "$D/code-server/extensions.txt"
echo "Done. Reload code.prabhas.io."
