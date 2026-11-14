package main

import (
	"io"
	"log"
	"regexp"
	"testing"

	"github.com/slack-go/slack"
)

func testBridge() *bridge {
	logger = log.New(io.Discard, "", 0)
	return &bridge{
		selfID:  "UME",
		trigger: regexp.MustCompile(`(?i)(?:^|[\s(])@claude(?::([A-Za-z0-9._-]+))?\b`),
		peers:   map[string]bool{"UDAN": true},
		peerRe:  regexp.MustCompile(`(?i)<@UME(?:\|[^>]*)?>\s*['\x{2019}]s\s+@?\x{200b}?claude\b`),
	}
}

func TestPeerAddressing(t *testing.T) {
	b := testBridge()
	for text, want := range map[string]bool{
		replyPrefix + "<@UME>'s claude, what's for lunch?":   true,
		replyPrefix + "hey <@UME|ed>’s Claude, you there":    true, // macOS smart quote
		replyPrefix + "<@UME>'s @​claude ping":               true, // what say() turns "@claude" into
		replyPrefix + "<@UME> claude ping":                   false,
		replyPrefix + "<@UDAN>'s claude ping":                false, // someone else's Claude
		replyPrefix + "<@UME>'s claudeish":                   false,
		"ping <@UME>'s claude (no prefix, typed by a human)": true, // regex alone; prefix checked separately
	} {
		if got := b.peerRe.MatchString(text); got != want {
			t.Errorf("peerRe(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestPeerGate(t *testing.T) {
	b := testBridge()
	cases := []struct {
		user, text string
		want       string
	}{
		// The human wake string ("<@UME>'s claude") is a subset of the robot one
		// (replyPrefix + the same), so the robot prefix must be what distinguishes them.
		{"UDAN", replyPrefix + "<@UME>'s claude hi", "claude"}, // their Claude
		{"UDAN", "<@UME>'s claude hi", "human"},                // the colleague in person
		{"UDAN", "hey <@UME>’s Claude, around?", "human"},      // smart quote, no prefix
		{"UEVE", replyPrefix + "<@UME>'s claude hi", ""},       // not an allowed peer
		{"UEVE", "<@UME>'s claude hi", ""},                     // not an allowed peer (human)
		{"UDAN", replyPrefix + "<@UDAN>'s claude hi", ""},      // addressed to someone else
		{"UDAN", "just chatting, no address", ""},              // not addressed to you
		{"UME", replyPrefix + "<@UME>'s claude hi", ""},        // our own Claude's post
	}
	for i, c := range cases {
		ts := string(rune('a' + i))
		e := &slack.MessageEvent{Msg: slack.Msg{User: c.user, Text: c.text, Channel: "C", Timestamp: ts}}
		if got := b.peerKind(e); got != c.want {
			t.Errorf("case %d (%s: %q): kind=%q want %q", i, c.user, c.text, got, c.want)
		}
	}
}

func TestOwnClaudeRepliesNeverTrigger(t *testing.T) {
	b := testBridge()
	// a reply that mentions @claude gets defused by say(), so neither bridge's self-trigger fires on it
	defused := b.trigger.ReplaceAllStringFunc("ask @claude later", func(m string) string { return m[:len(m)-len("@claude")] + "@​claude" })
	if b.trigger.MatchString(defused) {
		t.Errorf("defused text still triggers: %q", defused)
	}
}
