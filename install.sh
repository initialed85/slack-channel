#!/usr/bin/env bash
# slack-channel one-shot installer.
#
#   curl -fsSL https://raw.githubusercontent.com/initialed85/slack-channel/main/install.sh | bash
#
# Clones (or updates) the repo, builds the binary, creates a .env you can edit, and — unless you
# opt out — installs a background service that keeps the bridge running (launchd on macOS, a
# systemd --user unit on Linux).
#
# Knobs (all optional, pass as env vars):
#   SLACK_CHANNEL_REPO    default initialed85/slack-channel
#   SLACK_CHANNEL_REF     default main
#   SLACK_CHANNEL_DIR     default ~/.local/share/slack-channel   (where it is installed)
#   SLACK_CHANNEL_LABEL   default slack-channel                  (launchd label / systemd unit name)
#   NO_SERVICE=1          install the binary only, do not set up the background service
#                         (same as passing --no-service)
set -euo pipefail

REPO="${SLACK_CHANNEL_REPO:-initialed85/slack-channel}"
REF="${SLACK_CHANNEL_REF:-main}"
DEST="${SLACK_CHANNEL_DIR:-$HOME/.local/share/slack-channel}"
LABEL="${SLACK_CHANNEL_LABEL:-slack-channel}"
STATE_DIR="${SLACK_CHANNEL_STATE_DIR:-$HOME/.cache/slack-channel}"
NO_SERVICE="${NO_SERVICE:-0}"
[ "${1:-}" = "--no-service" ] && NO_SERVICE=1

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

OS="$(uname -s)"
command -v go  >/dev/null || die "Go is required (https://go.dev/dl/). Install it and re-run."
command -v git >/dev/null || die "git is required."

# --- fetch -------------------------------------------------------------------------------------
if [ -d "$DEST/.git" ]; then
  say "Updating existing checkout in $DEST"
  git -C "$DEST" fetch --quiet origin "$REF" && git -C "$DEST" checkout --quiet "$REF" && git -C "$DEST" pull --ff-only --quiet || warn "could not fast-forward; using what's there"
else
  say "Cloning $REPO ($REF) into $DEST"
  mkdir -p "$(dirname "$DEST")"
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    gh repo clone "$REPO" "$DEST" -- --branch "$REF" --quiet   # works while the repo is private
  else
    git clone --quiet --branch "$REF" "https://github.com/$REPO.git" "$DEST" \
      || die "clone failed. If the repo is private, install the GitHub CLI (gh) and 'gh auth login' first."
  fi
fi

# --- build -------------------------------------------------------------------------------------
say "Building the binary"
( cd "$DEST/go" && go build -o bin/slack-channel ./cmd/slack-channel )
BIN="$DEST/go/bin/slack-channel"
[ -x "$BIN" ] || die "build did not produce $BIN"

# --- config ------------------------------------------------------------------------------------
ENV_FILE="$DEST/.env"
if [ ! -f "$ENV_FILE" ]; then
  cp "$DEST/.env.example" "$ENV_FILE"
  say "Created $ENV_FILE from the example — edit it (at least SLACK_WORKSPACE if it isn't auto-detected)."
else
  say "Keeping your existing $ENV_FILE"
fi
mkdir -p "$STATE_DIR"

if [ "$OS" = "Linux" ]; then
  warn "On Linux the macOS Keychain cookie path does not apply — set SLACK_D_COOKIE in $ENV_FILE (your Slack 'd' / xoxd- cookie)."
fi

# --- service -----------------------------------------------------------------------------------
install_launchd() {
  local plist="$HOME/Library/LaunchAgents/$LABEL.plist"
  mkdir -p "$HOME/Library/LaunchAgents"
  sed -e "s#__LABEL__#$LABEL#g" -e "s#__BIN__#$BIN#g" -e "s#__STATE_DIR__#$STATE_DIR#g" -e "s#__ENV_FILE__#$ENV_FILE#g" \
    "$DEST/packaging/launchd/slack-channel.plist.template" > "$plist"
  launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$plist" 2>/dev/null || launchctl load "$plist"
  launchctl kickstart -k "gui/$(id -u)/$LABEL" 2>/dev/null || true
  say "launchd service '$LABEL' installed and started. Logs: $STATE_DIR/slack-channel.log"
  say "Stop/remove:  launchctl bootout gui/\$(id -u)/$LABEL && rm '$plist'"
}

install_systemd() {
  local dir="$HOME/.config/systemd/user" unit
  unit="$dir/$LABEL.service"
  mkdir -p "$dir"
  sed -e "s#__BIN__#$BIN#g" -e "s#__ENV_FILE__#$ENV_FILE#g" \
    "$DEST/packaging/systemd/slack-channel.service.template" > "$unit"
  systemctl --user daemon-reload
  systemctl --user enable --now "$LABEL.service"
  loginctl enable-linger "$(whoami)" 2>/dev/null || warn "could not enable linger; the service will stop when you log out (run: loginctl enable-linger $(whoami))"
  say "systemd --user service '$LABEL' installed and started. Logs: journalctl --user -u $LABEL -f"
  say "Stop/remove:  systemctl --user disable --now $LABEL && rm '$unit'"
}

if [ "$NO_SERVICE" = 1 ]; then
  say "Skipping background service (--no-service / NO_SERVICE=1). Run it yourself with:  $BIN daemon"
else
  case "$OS" in
    Darwin) install_launchd ;;
    Linux)  if command -v systemctl >/dev/null 2>&1; then install_systemd; else warn "no systemd found; run it yourself: $BIN daemon"; fi ;;
    *)      warn "unknown OS '$OS'; run it yourself: $BIN daemon" ;;
  esac
fi

cat <<DONE

$(say "Done.")
  1. Edit your config:        $ENV_FILE
  2. (After editing) restart: $([ "$OS" = Darwin ] && echo "launchctl kickstart -k gui/\$(id -u)/$LABEL" || echo "systemctl --user restart $LABEL")
  3. Start a Claude session that receives your @claude messages, named "$([ -n "${SLACK_DEFAULT_SESSION:-}" ] && echo "$SLACK_DEFAULT_SESSION" || echo slack)":
        claude    # then in Claude Code, ensure the session name matches SLACK_DEFAULT_SESSION
  4. In Slack, message yourself "@claude hello" and watch $STATE_DIR/slack-channel.log

See the README for how session naming and the daemon vs MCP modes work.
DONE
