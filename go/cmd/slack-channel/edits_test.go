package main

import (
	"io"
	"log"
	"regexp"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

type forwarded struct{ ts, oldText, newText, target, threadTS string }

func editTestBridge(t *testing.T) (*bridge, *[]forwarded) {
	logger = log.New(io.Discard, "", 0)
	var got []forwarded
	b := &bridge{
		selfID:        "UME",
		defaultTarget: "slack",
		trigger:       regexp.MustCompile(`@claude(?::(\S+))?`),
		seenLog:       newSeenLog(t.TempDir()),
		engaged:       newEngagedLog(t.TempDir()),
	}
	b.forwardEdit = func(channel, ts, threadTS, user, oldText, newText, target string) {
		got = append(got, forwarded{ts, oldText, newText, target, threadTS})
	}
	return b, &got
}

// delivered marks channel/ts as handed to a session, the way onMessage + dispatch do.
func deliveredTo(b *bridge, ts, text, target string) {
	b.markSeen("C1", ts)
	b.rememberDelivered("C1", ts, "", text, target)
}

func TestAnEditIsForwardedOncePerEditedTS(t *testing.T) {
	b, got := editTestBridge(t)
	deliveredTo(b, "1.1", "@claude:other fix teh bug", "other")

	b.onEdit("C1", "1.1", "", "UME", "@claude:other fix teh bug", "@claude:other fix the bug", "2.2", "rtm")
	b.onEdit("C1", "1.1", "", "UME", "", "@claude:other fix the bug", "2.2", "sweep") // the sweep sees the same edit
	if len(*got) != 1 {
		t.Fatalf("one edit seen by socket and sweep was forwarded %d times", len(*got))
	}
	if f := (*got)[0]; f.oldText != "@claude:other fix teh bug" || f.newText != "@claude:other fix the bug" || f.target != "other" {
		t.Errorf("forwarded %+v; want the before/after, to the session that had it", f)
	}

	// a second edit has a new edited.ts and goes through, with the first edit as its "before"
	b.onEdit("C1", "1.1", "", "UME", "", "@claude:other fix the bugs", "3.3", "sweep")
	if len(*got) != 2 || (*got)[1].oldText != "@claude:other fix the bug" {
		t.Fatalf("second edit: %+v", *got)
	}
}

func TestAOneCharacterEditIsStillForwarded(t *testing.T) {
	b, got := editTestBridge(t)
	deliveredTo(b, "1.1", "@claude ship it", "slack")
	b.onEdit("C1", "1.1", "", "UME", "@claude ship it", "@claude ship it?", "2.2", "rtm")
	if len(*got) != 1 {
		t.Fatal("a one-character edit was dropped")
	}
}

func TestEditsThatArentForwarded(t *testing.T) {
	b, got := editTestBridge(t)
	deliveredTo(b, "1.1", "@claude hi", "slack")

	// the bridge's own robot-prefixed post
	b.onEdit("C1", "1.1", "", "UME", replyPrefix+"done", replyPrefix+"done!", "2.2", "rtm")
	// same text: an unfurl, not an edit
	b.onEdit("C1", "1.1", "", "UME", "@claude hi", "@claude hi", "3.3", "rtm")
	// a message never delivered, still without a trigger after the edit
	b.onEdit("C1", "9.9", "", "UME", "lunch?", "lunch at 1?", "4.4", "rtm")
	if len(*got) != 0 {
		t.Errorf("forwarded %+v", *got)
	}
	if _, seen := b.seen.Load("C1/9.9"); seen {
		t.Error("an untagged, never-delivered message was marked handled by its edit")
	}
}

func TestMessageChangedWithoutAnEditIsIgnored(t *testing.T) {
	b, got := editTestBridge(t)
	deliveredTo(b, "1.1", "@claude look at https://x", "slack")
	b.onMessage(&slack.MessageEvent{
		Msg:        slack.Msg{Channel: "C1", SubType: "message_changed"},
		SubMessage: &slack.Msg{Timestamp: "1.1", User: "UME", Text: "@claude look at https://x"}, // unfurl: no Edited
	}, "rtm")
	if len(*got) != 0 {
		t.Errorf("an unfurl was forwarded as an edit: %+v", *got)
	}
}

func TestEditContent(t *testing.T) {
	c := editContent("Alex", "Alex", true, "#general", "https://link", "teh", "the")
	if !strings.Contains(c, "edited: teh → the") || !strings.Contains(c, "https://link") {
		t.Errorf("owner edit: %q", c)
	}
	c = editContent("Alex", "Sam", false, "#general", "https://link", "", "new")
	if !strings.Contains(c, "Sam's authority at most") || !strings.Contains(c, "isn't available") {
		t.Errorf("colleague edit with no earlier text: %q", c)
	}
}

func TestStripReplyPrefix(t *testing.T) {
	cases := map[string]string{
		":robot_face: *Claude:* done":                        "done",
		":robot_face: *Claude:*done":                         "done",
		":robot_face: *Claude:* :robot_face: *Claude:* done": "done",
		"done":                         "done",
		"see :robot_face: *Claude:* x": "see :robot_face: *Claude:* x",
	}
	for in, want := range cases {
		if got := stripReplyPrefix(in); got != want {
			t.Errorf("stripReplyPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
