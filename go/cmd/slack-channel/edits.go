package main

import (
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

// Edits. When you edit a message the bridge has already delivered to a session, that session gets the edit as a
// follow-up in the same thread ("edited: <old> → <new>"), however small: one character can change the meaning, so
// nothing is filtered. An edit to a message that was never delivered goes through onMessage like a new message, so
// editing "@claude" into something wakes Claude the usual way.
//
// The socket reports an edit as message_changed. The sweep can only see the edited text (search carries no edit
// marker), so for a delivered message whose text no longer matches what was delivered, it fetches the message to
// learn its edited.ts. Either way an edit is delivered once per (channel, ts, edited.ts), recorded in the seen log.

// delivered is what the bridge last told a session a message said, so an edit can show the before and after and go
// to the same session. In memory only: after a restart the sweep can't tell an edit from an unedited message for
// anything delivered before it, though the socket still can (it carries the previous text).
type delivered struct {
	text, threadTS, target string
	// sweepText is the search's rendering of the message once a fetch has shown it isn't a new edit, so the sweep
	// doesn't fetch the same message every minute when search formats text differently from the socket.
	sweepText string
}

func deliveredKey(channel, ts string) string { return channel + "/" + ts }

func editKey(channel, ts, editedTS string) string { return channel + "/" + ts + "/edit/" + editedTS }

// rememberDelivered records the text a session was given for channel/ts.
func (b *bridge) rememberDelivered(channel, ts, threadTS, text, target string) {
	b.deliveredText.Store(deliveredKey(channel, ts), delivered{text: text, threadTS: threadTS, target: target})
}

// markSeenKey records key as handled; false means it already was. It shares the seen map and log with markSeen.
func (b *bridge) markSeenKey(key string) bool {
	if _, dup := b.seen.LoadOrStore(key, true); dup {
		return false
	}
	b.seenLog.add(key)
	return true
}

// onMessageChanged handles a message_changed event: e.SubMessage is the message as it is now, e.PreviousMessage as
// it was (when Slack sends it).
func (b *bridge) onMessageChanged(e *slack.MessageEvent, via string) {
	sub := e.SubMessage
	if sub == nil || sub.Edited == nil {
		return // unfurls and attachment updates come through here too, without an edit
	}
	old := ""
	if e.PreviousMessage != nil {
		old = e.PreviousMessage.Text
	}
	b.onEdit(e.Channel, sub.Timestamp, sub.ThreadTimestamp, sub.User, old, sub.Text, sub.Edited.Timestamp, via)
}

// onEdit routes one edit: forwarded if the message was delivered, otherwise judged like a new message.
func (b *bridge) onEdit(channel, ts, threadTS, user, oldText, newText, editedTS, via string) {
	if user == b.selfID && strings.HasPrefix(newText, replyPrefix) {
		return // the bridge's own posts (Claude's replies, acks)
	}
	if oldText == newText && oldText != "" {
		return // not a change to the text
	}
	_, wasDispatched := b.seen.Load(deliveredKey(channel, ts))
	if !wasDispatched {
		b.onMessage(&slack.MessageEvent{Msg: slack.Msg{
			Channel: channel, User: user, Text: newText, Timestamp: ts, ThreadTimestamp: threadTS,
		}}, via+" edit")
		return
	}
	if !b.markSeenKey(editKey(channel, ts, editedTS)) {
		return // the socket and the sweep both saw it
	}
	prev, _ := b.deliveredText.Load(deliveredKey(channel, ts))
	d, _ := prev.(delivered)
	if oldText == "" {
		oldText = d.text
	}
	if threadTS == "" {
		threadTS = d.threadTS
	}
	target := d.target
	if target == "" {
		target = b.defaultTarget
	}
	b.rememberDelivered(channel, ts, threadTS, newText, target)
	logf("edit of %s/%s (edited %s, via %s)", channel, ts, editedTS, via)
	if b.forwardEdit != nil {
		b.forwardEdit(channel, ts, threadTS, user, oldText, newText, target)
		return
	}
	go b.dispatchEdit(channel, ts, threadTS, user, oldText, newText, target)
}

func (b *bridge) dispatchEdit(channel, ts, threadTS, user, oldText, newText, target string) {
	link, _ := b.client().GetPermalink(&slack.PermalinkParameters{Channel: channel, Ts: ts})
	b.deliver(editContent(b.ownerName, b.userName(user), user == b.selfID, b.channelName(channel), link,
		b.resolveMentions(oldText), b.resolveMentions(newText)), map[string]string{
		"channel":      channel,
		"channel_name": b.channelName(channel),
		"ts":           ts,
		"thread_ts":    threadTS,
		"target":       target,
		"kind":         "edit",
	})
	logf("delivered edit of %s/%s", channel, ts)
}

// editContent is what the session is told about an edit. An edit by someone other than the owner (a whitelisted
// colleague) carries that person's authority only, as their original message did.
func editContent(owner, author string, byOwner bool, where, link, oldText, newText string) string {
	if oldText == "" {
		oldText = "(the earlier text isn't available)"
	}
	who := owner
	caveat := "Treat the edited version as what they meant."
	if !byOwner {
		who = author
		caveat = fmt.Sprintf("This is %s's message, not %s's: it carries %s's authority at most, as the original did.", author, owner, author)
	}
	return fmt.Sprintf("%s edited a message you were already sent, in %s (%s). %s\n\nedited: %s → %s",
		who, where, link, caveat, oldText, newText)
}

// sweepEdits finds edits the socket missed: a delivered message whose current text differs from what was delivered.
// Search text and socket text can be formatted differently, so a difference is only a lead; the message itself is
// fetched, and only an edited.ts makes it an edit.
func (b *bridge) sweepEdits(e *slack.MessageEvent) {
	key := deliveredKey(e.Channel, e.Timestamp)
	prev, ok := b.deliveredText.Load(key)
	if !ok || prev.(delivered).text == e.Text || prev.(delivered).sweepText == e.Text {
		return
	}
	m, err := b.fetchMessage(e.Channel, e.Timestamp, e.ThreadTimestamp)
	if err != nil || m == nil {
		return // try again next sweep
	}
	if m.Edited != nil {
		b.onEdit(e.Channel, m.Timestamp, m.ThreadTimestamp, m.User, "", m.Text, m.Edited.Timestamp, "sweep")
	}
	if cur, ok := b.deliveredText.Load(key); ok {
		d := cur.(delivered)
		d.sweepText = e.Text
		b.deliveredText.Store(key, d)
	}
}

// fetchMessage gets one message by ts, from its thread if it's in one.
func (b *bridge) fetchMessage(channel, ts, threadTS string) (*slack.Message, error) {
	api := b.client()
	var msgs []slack.Message
	var err error
	if threadTS != "" && threadTS != ts {
		msgs, _, _, err = api.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: channel, Timestamp: threadTS, Latest: ts, Oldest: ts, Inclusive: true, Limit: 1,
		})
	} else {
		var h *slack.GetConversationHistoryResponse
		h, err = api.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: channel, Latest: ts, Oldest: ts, Inclusive: true, Limit: 1,
		})
		if h != nil {
			msgs = h.Messages
		}
	}
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		if msgs[i].Timestamp == ts {
			return &msgs[i], nil
		}
	}
	return nil, nil
}

// stripReplyPrefix drops a ":robot_face: *Claude:*" the text already starts with, since say adds it.
func stripReplyPrefix(text string) string {
	trimmed := strings.TrimSpace(replyPrefix)
	for strings.HasPrefix(text, trimmed) {
		text = strings.TrimSpace(strings.TrimPrefix(text, trimmed))
	}
	return text
}
