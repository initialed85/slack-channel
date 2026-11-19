package main

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestReactionContent(t *testing.T) {
	got := reactionContent("DM", "https://x/p1", "a joke", []string{":joy: Izzy", ":joy: Dan", ":+1: Ed", ":joy: Marco"})
	for _, want := range []string{":joy: ×3, :+1: ×1", "Dan", "a joke", "not a request"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestOnReactionIgnoresOthersMessages(t *testing.T) {
	b := &bridge{selfID: "UME", reactionDelay: 1}
	e := &slack.ReactionAddedEvent{ItemUser: "UOTHER", Reaction: "joy"}
	e.Item.Type = "message"
	b.onReaction(e)
	if len(b.reactions.pending) != 0 {
		t.Fatal("batched a reaction on someone else's message")
	}
}
