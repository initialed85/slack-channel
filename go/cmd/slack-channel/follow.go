package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Thread follow: once Claude has replied in a thread, your later messages in that thread reach it without the
// trigger, for followTTL after Claude's last reply there. It only follows *your* messages (colleagues still go through
// the peer convention), and skips any of yours that @mention someone else, since those are addressed to a person.
// Anything else that arrives this way is flagged as untagged, and the session is told it may be chatter and to stay
// silent unless it's plainly for it, so a follow-up that wasn't meant for Claude costs one short turn and no reply.
//
// The record of engaged threads is a small append-only file in the state dir, because `say` runs as its own process
// (the CLI, or the MCP server) while the listener that reads it is the daemon.

type engagedLog struct {
	mu   sync.Mutex
	path string
}

const engagedKeep = 500

func newEngagedLog(stateDir string) *engagedLog {
	return &engagedLog{path: filepath.Join(stateDir, "engaged.log")}
}

// touch records that Claude just replied in channel/threadTS.
func (l *engagedLog) touch(channel, threadTS string) {
	if l == nil || threadTS == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s/%s\t%d\n", channel, threadTS, time.Now().Unix())
}

// lastReply returns when Claude last replied in channel/threadTS (zero if never), trimming the file as it goes.
func (l *engagedLog) lastReply(channel, threadTS string) time.Time {
	if l == nil || threadTS == "" {
		return time.Time{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return time.Time{}
	}
	key := channel + "/" + threadTS
	var last time.Time
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		lines = append(lines, line)
		k, v, ok := strings.Cut(line, "\t")
		if !ok || k != key {
			continue
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && time.Unix(n, 0).After(last) {
			last = time.Unix(n, 0)
		}
	}
	f.Close()
	if len(lines) > engagedKeep*2 {
		_ = os.WriteFile(l.path, []byte(strings.Join(lines[len(lines)-engagedKeep:], "\n")+"\n"), 0o600)
	}
	return last
}

// followUp reports whether an untriggered message of yours should still reach Claude, because it's in a thread
// Claude is part of. It's only ever called for your own messages.
func (b *bridge) followUp(channel, threadTS, text string) bool {
	if b.followTTL <= 0 || threadTS == "" || strings.HasPrefix(text, replyPrefix) {
		return false
	}
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		if m[1] != b.selfID {
			return false // addressed to a person
		}
	}
	last := b.engaged.lastReply(channel, threadTS)
	return !last.IsZero() && time.Since(last) < b.followTTL
}

// markUnread puts your read marker back to just before Claude's reply, so the reply shows up as unread for you
// (Slack marks it read, because as far as it knows you wrote it). In a thread that's the thread's own marker
// (subscriptions.thread.mark, which the web client uses; there's no public API for thread read state).
func (b *bridge) markUnread(channel, threadTS, replyTS string) error {
	before := tsBefore(replyTS)
	if threadTS == "" || threadTS == replyTS {
		return b.webAPI("conversations.mark", url.Values{"channel": {channel}, "ts": {before}})
	}
	return b.webAPI("subscriptions.thread.mark", url.Values{
		"channel": {channel}, "thread_ts": {threadTS}, "ts": {before}, "read": {"0"},
	})
}

// tsBefore is the Slack timestamp one microsecond before ts.
func tsBefore(ts string) string {
	sec, frac, ok := strings.Cut(ts, ".")
	s, err1 := strconv.ParseInt(sec, 10, 64)
	us, err2 := strconv.ParseInt(frac, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return ts
	}
	t := s*1_000_000 + us - 1
	return fmt.Sprintf("%d.%06d", t/1_000_000, t%1_000_000)
}

// webAPI calls a Slack web API method the slack-go client doesn't wrap, with the same token and cookie.
func (b *bridge) webAPI(method string, form url.Values) error {
	b.mu.RLock()
	token, d := b.token, b.d
	b.mu.RUnlock()
	if token == "" {
		return fmt.Errorf("%s: not logged in", method)
	}
	form.Set("token", token)
	req, err := http.NewRequest("POST", "https://"+b.workspace+".slack.com/api/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: &cookieTransport{d: d, base: http.DefaultTransport}}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if !out.OK {
		return fmt.Errorf("%s: %s", method, out.Error)
	}
	return nil
}
