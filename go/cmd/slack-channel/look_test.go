package main

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestIsLook(t *testing.T) {
	b := &bridge{selfID: "UME", lookReaction: "thinking_face", lookChannel: "DME"}
	ev := func(user, reaction string) *slack.ReactionAddedEvent {
		e := &slack.ReactionAddedEvent{User: user, Reaction: reaction}
		e.Item.Type = "message"
		return e
	}
	if !b.isLook(ev("UME", "thinking_face")) {
		t.Fatal("owner's look reaction should trigger")
	}
	if b.isLook(ev("UOTHER", "thinking_face")) {
		t.Fatal("someone else's reaction must not trigger")
	}
	if b.isLook(ev("UME", "+1")) {
		t.Fatal("other emoji must not trigger")
	}
	b.lookChannel = ""
	if b.isLook(ev("UME", "thinking_face")) {
		t.Fatal("no report channel means the feature is off")
	}
}

func TestReplyHowToReactsOnOriginal(t *testing.T) {
	out := replyHowTo("bin", "Ed", map[string]string{
		"channel": "DME", "ts": "111.1", "thread_ts": "111.1",
		"react_channel": "CORIG", "react_ts": "222.2",
	})
	if !strings.Contains(out, "bin say DME 111.1") {
		t.Fatalf("reply should go to the private thread:\n%s", out)
	}
	if !strings.Contains(out, "bin react CORIG 222.2 +1") {
		t.Fatalf("finishing react should go on the original message:\n%s", out)
	}
}

func TestSortDedupe(t *testing.T) {
	in := []slack.Message{
		{Msg: slack.Msg{Timestamp: "300.0", Text: "c"}},
		{Msg: slack.Msg{Timestamp: "100.0", Text: "parent"}},
		{Msg: slack.Msg{Timestamp: "200.0", Text: "b"}},
		{Msg: slack.Msg{Timestamp: "100.0", Text: "parent"}},
	}
	out := sortDedupe(in)
	if len(out) != 3 || out[0].Text != "parent" || out[2].Text != "c" {
		t.Fatalf("got %+v", out)
	}
}
