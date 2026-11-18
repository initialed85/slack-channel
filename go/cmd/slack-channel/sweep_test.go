package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestThreadTSFromPermalink(t *testing.T) {
	cases := map[string]string{
		"https://x.slack.com/archives/C1/p1794960598125439?thread_ts=1794926888.285339&cid=C1": "1794926888.285339",
		"https://x.slack.com/archives/C1/p1794960598125439":                                    "",
		"::not a url": "",
	}
	for link, want := range cases {
		if got := threadTSFromPermalink(link); got != want {
			t.Errorf("threadTSFromPermalink(%q) = %q, want %q", link, got, want)
		}
	}
}

func TestLateNote(t *testing.T) {
	ts := func(ago time.Duration) string { return fmt.Sprintf("%d.000100", time.Now().Add(-ago).Unix()) }
	if n := lateNote(ts(10 * time.Second)); n != "" {
		t.Errorf("fresh message got a late note: %q", n)
	}
	if n := lateNote(ts(10 * time.Minute)); !strings.Contains(n, "10m0s ago") {
		t.Errorf("10-minute-old message: %q", n)
	}
}

func TestSeenLogSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	b := &bridge{seenLog: newSeenLog(dir)}
	if !b.markSeen("C1", "1.1") || b.markSeen("C1", "1.1") {
		t.Fatal("markSeen should be true once, then false")
	}
	restarted := &bridge{seenLog: newSeenLog(dir)}
	restarted.seenLog.load(restarted)
	if restarted.markSeen("C1", "1.1") {
		t.Error("a restarted bridge re-handled a message from before the restart")
	}
}
