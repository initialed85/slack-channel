package main

import (
	"testing"
	"time"
)

func TestTsBefore(t *testing.T) {
	for in, want := range map[string]string{
		"1795065150.121729": "1795065150.121728",
		"1795065150.000000": "1795065149.999999",
		"garbage":           "garbage",
	} {
		if got := tsBefore(in); got != want {
			t.Errorf("tsBefore(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFollowUp(t *testing.T) {
	b := &bridge{selfID: "UME", followTTL: time.Hour, engaged: newEngagedLog(t.TempDir())}
	if b.followUp("C1", "1.1", "hello") {
		t.Fatal("followed a thread Claude never replied in")
	}
	b.engaged.touch("C1", "1.1")
	cases := map[string]bool{
		"yep change to all zeros":                           true,
		"<@UME> note to self":                               true,
		"<@UIZZY> what do you reckon?":                      true, // yours, so delivered; the session decides if it's for it
		"fix it then get <@UBOT> to chide whoever broke it": true,
		replyPrefix + "Claude's own reply":                  false,
	}
	for text, want := range cases {
		if got := b.followUp("C1", "1.1", text); got != want {
			t.Errorf("followUp(%q) = %v, want %v", text, got, want)
		}
	}
	if b.followUp("C1", "", "top-level") {
		t.Error("followed a top-level message")
	}
	if b.followUp("C2", "1.1", "other channel") {
		t.Error("followed a thread in another channel")
	}
	b.followTTL = 0
	if b.followUp("C1", "1.1", "disabled") {
		t.Error("followed with SLACK_FOLLOW_THREADS=0")
	}
}
