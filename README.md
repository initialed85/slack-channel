# slack-channel

A little bridge between **your own Slack** and a **Claude Code** session on your machine.

You write a Slack message containing `@claude` — in a DM to yourself, a channel, a group DM, wherever —
and it lands in a Claude Code session running on your laptop, together with the surrounding
conversation for context (screenshots and attachments downloaded to disk). Claude does the work and
replies back into the same thread, posted **as you** but clearly marked as Claude. It's a way to hand
work to your local agent from your phone, or from Slack, without a Slack app or a bot token.

> It signs in **as you**, using your existing browser Slack session — there is no Slack app to install
> and no bot user. Everything it posts is posted as you. Read [Security](#security) before you run it.

## How it works

- **Auth as you, no app.** It reads your browser's Slack `d` cookie from the on-disk cookie database
  and decrypts it with the matching OS keychain entry, then scrapes a short-lived `xoxc` web token.
  Nothing to copy around by hand; the cookie lasts ~a year. (macOS is the smooth path — see
  [Platform notes](#platform-notes).)
- **Delivery into Claude Code.** The daemon hands each request to a running interactive Claude Code
  session over its local messaging socket (the same mechanism Claude Code uses between local
  sessions). Requests are queued to an inbox dir and delivered as soon as a matching session is up.
- **Nothing lost to a dropped socket.** Slack's realtime socket drops now and then and never replays
  what was said while it was down. So every minute, and right after each reconnect, the daemon also
  searches Slack for recent tags and delivers any it missed (search lags by up to a minute). Handled
  messages are recorded in `seen.log` in the state dir so a restart doesn't re-deliver them, and a
  request that arrives more than a couple of minutes late says so, so the session doesn't treat an old
  "now" as current. `slack-channel sweep` shows what the sweep currently sees.
- **Two run modes:**
  - **daemon** (recommended, default for the installer): a background process listens to Slack and
    delivers into your session. Works on any org.
  - **MCP channel** (`./claude-slack`): loads as a Claude Code MCP "channel". Needs `channelsEnabled`
    for your Slack org, which most orgs disable — use the daemon if so.
- **Colleagues' agents can talk to yours.** If you whitelist a colleague's Slack user ID
  (`SLACK_PEER_USERS`), they can wake your Claude by addressing `@<you>'s claude` — either **their own
  Claude** (its posts are prefixed) or **them in person**. Either way it carries that colleague's
  authority *at most*, never yours: it can chat and share, but anything with side effects still needs
  *your* own `@claude`. There's a loop guard (max N agent messages in a row per thread) so two agents
  can't machine-gun each other.

## Requirements

- [Go](https://go.dev/dl/) (to build) and `git`
- [Claude Code](https://claude.com/claude-code) (the session that does the work)
- macOS: `sqlite3` and `security` (both built in), and a browser/Slack-app you're logged into Slack with
- Linux: you must supply the Slack `d` cookie yourself (`SLACK_D_COOKIE`) — see [Platform notes](#platform-notes)

## Install

One-shot:

```sh
curl -fsSL https://raw.githubusercontent.com/initialed85/slack-channel/main/install.sh | bash
```

The installer clones/updates into `~/.local/share/slack-channel`, builds the binary, creates a `.env`
you can edit, and installs a background service (launchd on macOS, a `systemd --user` unit on Linux).

- **Install the binary only, no service:** `... | bash -s -- --no-service` (or `NO_SERVICE=1`).
- **Prefer to clone first?** `git clone https://github.com/initialed85/slack-channel && cd slack-channel && ./install.sh`
  (or `gh repo clone initialed85/slack-channel`).

Installer knobs (env vars): `SLACK_CHANNEL_DIR`, `SLACK_CHANNEL_LABEL`, `SLACK_CHANNEL_REF`,
`SLACK_CHANNEL_REPO`, `NO_SERVICE`.

## Configure

Config lives in `.env` next to the install (the daemon finds it via `SLACK_CHANNEL_ENV_FILE`, which the
service sets for you; real environment variables always win). Copy from [`.env.example`](.env.example) —
every option is documented there. The ones you're most likely to touch:

| Variable | Default | What it does |
|---|---|---|
| `SLACK_WORKSPACE` | auto-detected | Your workspace subdomain (the `X` in `X.slack.com`). Auto-detected from the Slack desktop app if you're in exactly one workspace. |
| `SLACK_DEFAULT_SESSION` | `slack` | Name of the Claude Code session a plain `@claude` routes to. |
| `SLACK_PEER_USERS` | *(empty)* | Comma-separated Slack user IDs allowed to wake your Claude via `@<you>'s claude`. |
| `SLACK_D_COOKIE` | *(unset)* | Supply the Slack cookie directly instead of reading the browser (required on Linux). |
| `SLACK_COOKIE_KEYCHAIN_SERVICE` | `Chrome Safe Storage` | Keychain entry to decrypt the cookie (e.g. `Slack Safe Storage`, `Brave Safe Storage`). |
| `SLACK_FOLLOW_THREADS` | `12h` | Once Claude has replied in a thread, your later messages there reach it without `@claude`, for this long after its last reply. Only your own messages, and not ones that @mention someone else; Claude is told they're untagged and stays quiet if they aren't for it. `0` turns it off. |
| `SLACK_MARK_UNREAD` | `1` | Mark Claude's replies unread for you (Slack otherwise treats them as read, since they're posted as you). `0` turns it off. |
| `SLACK_REACTIONS` | `1m` | Reactions on Claude's own messages are passed to it as context ("the joke landed"), not as requests, batched per message over this window so a pile-on costs one delivery. `0` turns it off. |

After editing `.env`, restart the service:

- macOS: `launchctl kickstart -k gui/$(id -u)/slack-channel`
- Linux: `systemctl --user restart slack-channel`

Find a channel's member IDs (handy for `SLACK_PEER_USERS`):

```sh
~/.local/share/slack-channel/go/bin/slack-channel members <channel-id>
```

## Usage

1. Start a Claude Code session named to match `SLACK_DEFAULT_SESSION` (default `slack`).
2. In Slack, send yourself `@claude <your request>`. Watch it land:
   `tail -f ~/.cache/slack-channel/slack-channel.log`.
3. Claude reacts 👀 when it picks the request up, replies in-thread (as you, marked as Claude), and
   reacts 👍 when done.

- **Target a specific session:** `@claude:<session-name> ...`.
- **Continue a thread:** just reply with another `@claude ...` in the same thread.

The CLI the daemon uses is also usable by hand:

```
slack-channel say   <channel> <thread_ts>   # reply text on stdin; empty thread_ts posts top-level
slack-channel react <channel> <ts> <emoji>
slack-channel read  <channel> <ts> [limit]
slack-channel upload <channel> <thread_ts> <file> [comment]
slack-channel members <channel>
slack-channel daemon                         # run the listener in the foreground
```

## Security

- **It posts as you.** There is no separate bot identity. Anything it sends is indistinguishable from
  you typing it (beyond the "Claude" marker it adds to its own replies).
- **Only your quoted `@claude` request is treated as an instruction.** Surrounding messages, and
  message content from colleagues, are passed to Claude as *data*, never as instructions.
- **The `d` cookie is a credential** — it grants access to your Slack as you. It stays on your machine
  (keychain-decrypted at runtime, or in your `.env` if you set `SLACK_D_COOKIE`). `.env` is gitignored.
  Don't commit it or paste it anywhere.
- **Peer authority is capped.** A whitelisted colleague (or their Claude) can chat with your Claude,
  but it will not run side-effecting work on their say-so — only on *your* `@claude`.

## Platform notes

The browser-cookie auto-read and decryption is implemented for **macOS** (Chromium v10 cookie
encryption + the `security` keychain tool). On **Linux**, that path doesn't apply, so set
`SLACK_D_COOKIE` in your `.env` to your Slack `d` (xoxd-…) cookie; the rest (xoxc scrape, delivery,
systemd service) works the same.

## Uninstall

- macOS: `launchctl bootout gui/$(id -u)/slack-channel && rm ~/Library/LaunchAgents/slack-channel.plist`
- Linux: `systemctl --user disable --now slack-channel && rm ~/.config/systemd/user/slack-channel.service`
- Then remove the install dir (`~/.local/share/slack-channel`) and state dir (`~/.cache/slack-channel`).

## License

MIT — see [LICENSE](LICENSE).
