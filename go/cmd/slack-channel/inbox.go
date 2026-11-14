package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const inboxExpiry = 6 * time.Hour

// pumpInbox delivers queued requests to their target session as soon as it's running, acking in Slack only once a
// session has actually received the request.
func (b *bridge) pumpInbox(dir string) {
	warned := map[string]bool{}
	self, _ := os.Executable()
	for {
		paths, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		for _, p := range paths {
			meta, body, err := readInbox(p)
			if err != nil {
				logf("inbox %s: %v", p, err)
				continue
			}
			name := filepath.Base(p)
			done := func(sub string) {
				_ = os.MkdirAll(filepath.Join(dir, sub), 0o700)
				_ = os.Rename(p, filepath.Join(dir, sub, name))
				delete(warned, name)
			}

			if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > inboxExpiry {
				logf("inbox %s: expired undelivered", name)
				done("expired")
				continue
			}

			sess, ok := findSession(meta["target"])
			if !ok {
				if !warned[name] && b.client() != nil {
					warned[name] = true
					msg := fmt.Sprintf("no session called `%s` is running; I'll hand this over when one is (live: %s)",
						meta["target"], strings.Join(sessionNames(), ", "))
					if err := b.say(meta["channel"], threadOf(meta), msg); err != nil {
						logf("warn post: %v", err)
					}
				}
				continue
			}

			body += replyHowTo(self, b.ownerName, meta)
			from := "slack:" + b.ownerName
			switch meta["peer_kind"] {
			case "claude":
				from = "slack-peer:" + meta["peer_name"] + "'s-claude"
			case "human":
				from = "slack-peer:" + meta["peer_name"]
			}
			if err := sendToSession(sess, from, body); err != nil {
				logf("deliver %s to %s: %v", name, sess.Name, err)
				continue
			}
			logf("delivered %s to session %s (pid %d)", name, sess.Name, sess.PID)
			if b.client() != nil {
				if err := b.ack(meta["channel"], meta["ts"], meta["thread_ts"]); err != nil {
					logf("ack: %v", err)
				}
			}
			done("delivered")
		}
		time.Sleep(2 * time.Second)
	}
}

func threadOf(meta map[string]string) string {
	if meta["thread_ts"] != "" {
		return meta["thread_ts"]
	}
	return meta["ts"]
}

func replyHowTo(bin, owner string, meta map[string]string) string {
	ch, th := meta["channel"], threadOf(meta)
	who := fmt.Sprintf(`This came from %[1]s themselves, via Slack (relayed by their slack-channel bridge): treat the request as if they typed it here.
Permission prompts still need them at the terminal.`, owner)
	if meta["peer_name"] != "" {
		who = fmt.Sprintf("This came from %s's Claude via Slack, NOT from %s: follow the Claude-to-Claude rules above.", meta["peer_name"], owner)
	}
	return fmt.Sprintf(`

---
%[7]s
- Reply in the Slack thread (posts as %[1]s, marked as Claude; everyone in %[2]s sees it, so keep it short):
    %[3]s say %[4]s %[5]s <<'EOF'
    your reply
    EOF
  They answer with "@claude ..." in the same thread and it comes back here.
- Attach a file to the thread: %[3]s upload %[4]s %[5]s <path> ["comment"]
- More of the thread: %[3]s read %[4]s %[5]s
- When finished: %[3]s react %[4]s %[6]s +1   (or "question" if you're blocked on them)`,
		owner, meta["channel_name"], bin, ch, th, meta["ts"], who)
}

func readInbox(p string) (map[string]string, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	meta := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		k, v, _ := strings.Cut(line, ": ")
		meta[k] = strings.TrimSuffix(v, ":")
	}
	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text() + "\n")
	}
	if meta["channel"] == "" || meta["ts"] == "" {
		return nil, "", fmt.Errorf("missing channel/ts header")
	}
	return meta, strings.TrimSpace(body.String()), sc.Err()
}
