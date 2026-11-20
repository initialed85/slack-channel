package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
)

// The RTM socket drops every so often (laptop sleep, network blips, Slack recycling it) and never replays what was
// said while it was down, so a tag sent in that gap is silently lost. The sweep closes that hole: every
// sweepInterval, and right after every reconnect, it asks Slack's search for recent messages that could be a
// trigger and feeds any it hasn't handled through the same onMessage path the socket uses. Search lags a little
// (seconds to a minute), which is fine for a safety net.

const (
	sweepInterval = time.Minute
	// sweepWindow is how far back a sweep looks. Generous on purpose: a reconnect after a long sleep should still
	// pick up what was missed, and anything already handled is skipped via the seen log.
	sweepWindow = 2 * time.Hour
	// lateAfter is how old a request can be before the delivered message says it's late, so the session doesn't
	// treat an hour-old "now" as current.
	lateAfter   = 2 * time.Minute
	seenLogKeep = 2000
)

// seenLog persists which channel/ts the bridge has already acted on, so a restart doesn't re-deliver what the
// sweep finds in its window.
type seenLog struct {
	mu   sync.Mutex
	path string
}

func (l *seenLog) load(b *bridge) {
	f, err := os.Open(l.path)
	if err != nil {
		return
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		if k := strings.TrimSpace(s.Text()); k != "" {
			lines = append(lines, k)
			b.seen.Store(k, true)
		}
	}
	// keep the file from growing forever
	if len(lines) > seenLogKeep {
		_ = os.WriteFile(l.path, []byte(strings.Join(lines[len(lines)-seenLogKeep:], "\n")+"\n"), 0o600)
	}
}

func (l *seenLog) add(key string) {
	if l == nil || l.path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, key)
}

func newSeenLog(stateDir string) *seenLog {
	return &seenLog{path: filepath.Join(stateDir, "seen.log")}
}

// markSeen records channel/ts as handled; false means it already was.
func (b *bridge) markSeen(channel, ts string) bool {
	key := channel + "/" + ts
	if _, dup := b.seen.LoadOrStore(key, true); dup {
		return false
	}
	b.seenLog.add(key)
	return true
}

// sweepQueries are the searches that between them cover every kind of trigger: your own "@claude ..." and a
// whitelisted colleague (or their Claude) addressing "<@you>'s claude".
func (b *bridge) sweepQueries() []string {
	q := []string{"claude from:<@" + b.selfID + ">"}
	if len(b.peers) > 0 {
		q = append(q, "claude <@"+b.selfID+">")
	}
	return q
}

// sweepCandidates returns recent messages from search that could be triggers, oldest first, as MessageEvents.
func (b *bridge) sweepCandidates(window time.Duration) ([]*slack.MessageEvent, error) {
	api := b.client()
	cutoff := time.Now().Add(-window)
	var out []*slack.MessageEvent
	have := map[string]bool{}
	for _, q := range b.sweepQueries() {
		res, err := api.SearchMessages(q, slack.SearchParameters{Sort: "timestamp", SortDirection: "desc", Count: 50, Page: 1})
		if err != nil {
			return nil, fmt.Errorf("search %q: %w", q, err)
		}
		for _, m := range res.Matches {
			if slackTime(m.Timestamp).Before(cutoff) || have[m.Channel.ID+"/"+m.Timestamp] {
				continue
			}
			have[m.Channel.ID+"/"+m.Timestamp] = true
			e := &slack.MessageEvent{Msg: slack.Msg{
				Channel:         m.Channel.ID,
				User:            m.User,
				Text:            m.Text,
				Timestamp:       m.Timestamp,
				ThreadTimestamp: threadTSFromPermalink(m.Permalink),
			}}
			out = append([]*slack.MessageEvent{e}, out...)
		}
	}
	return out, nil
}

// threadTSFromPermalink pulls thread_ts out of a search match's permalink (search results don't carry it
// otherwise); "" for a top-level message.
func threadTSFromPermalink(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Query().Get("thread_ts")
}

// sweep feeds any trigger the socket missed through onMessage, which skips what's already been handled.
func (b *bridge) sweep() {
	events, err := b.sweepCandidates(sweepWindow)
	if err != nil {
		logf("sweep: %v", err)
		return
	}
	for _, e := range events {
		if _, done := b.seen.Load(e.Channel + "/" + e.Timestamp); done {
			b.sweepEdits(e) // already delivered; but edited since?
			continue
		}
		b.onMessage(e, "sweep")
	}
}

func (b *bridge) sweepLoop() {
	// First run with no seen log yet (a fresh install, or the upgrade that added it): there's no record of what
	// was already handled, so record what's there now rather than replaying up to sweepWindow of old requests.
	if _, err := os.Stat(b.seenLog.path); os.IsNotExist(err) {
		if events, err := b.sweepCandidates(sweepWindow); err == nil {
			for _, e := range events {
				b.markSeen(e.Channel, e.Timestamp)
			}
			logf("sweep: first run, recorded %d recent messages as already handled", len(events))
		}
	}
	for {
		b.sweep()
		select {
		case <-b.sweepNow:
		case <-time.After(sweepInterval):
		}
	}
}

// lateNote is prepended to a delivered request that's been waiting, so the session knows "now" in it isn't now.
func lateNote(ts string) string {
	sent := slackTime(ts)
	age := time.Since(sent)
	if age < lateAfter {
		return ""
	}
	return fmt.Sprintf("NOTE: this was sent at %s, %s ago, and is only reaching you now (the listener missed it live). Check the thread for anything that has moved on since before acting on it.\n\n",
		sent.Local().Format("Mon 15:04"), age.Round(time.Minute))
}
