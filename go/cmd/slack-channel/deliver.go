package main

// Delivery into a running interactive Claude Code session over its local messaging socket — the same
// mechanism SendMessage uses between local sessions. Undocumented, reverse-engineered from Claude Code
// 2.1.322, so expect it to need a poke when Claude Code changes:
//
//   ~/.claude/sessions/<pid>.json        {"name": ..., "messagingSocketPath": ..., "status": ..., "updatedAt": ...}
//   ~/.claude/sessions/<pid>.<hash>.key  {"peerToken": ...}   (0600, so only you can talk to your sessions)
//
// Wire format, one connection per message, newline-delimited JSON:
//
//   {"type":"auth","token":"<peerToken>"}
//   {"type":"user","message":{"role":"user","content":"<cross-session-message ...>\n...\n</cross-session-message>"},"priority":"next","msg_id":"..."}

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type sessionRecord struct {
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	Socket     string `json:"messagingSocketPath"`
	Kind       string `json:"kind"`
	Cwd        string `json:"cwd"`
	UpdatedAt  int64  `json:"updatedAt"`
	FormerName []struct {
		Name string `json:"name"`
	} `json:"formerNames"`
}

func sessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "sessions")
}

// liveSessions lists interactive sessions whose process is still alive, most recently active first.
func liveSessions() []sessionRecord {
	paths, _ := filepath.Glob(filepath.Join(sessionsDir(), "*.json"))
	var out []sessionRecord
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r sessionRecord
		if json.Unmarshal(raw, &r) != nil || r.Socket == "" || r.PID == 0 || r.Kind != "interactive" {
			continue
		}
		if syscall.Kill(r.PID, 0) != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func findSession(name string) (sessionRecord, bool) {
	for _, r := range liveSessions() {
		if strings.EqualFold(r.Name, name) {
			return r, true
		}
	}
	return sessionRecord{}, false
}

func sessionNames() []string {
	var names []string
	for _, r := range liveSessions() {
		names = append(names, r.Name)
	}
	return names
}

func sendToSession(r sessionRecord, fromName, body string) error {
	keys, _ := filepath.Glob(filepath.Join(sessionsDir(), fmt.Sprintf("%d.*.key", r.PID)))
	if len(keys) == 0 {
		return fmt.Errorf("no key file for session %s (pid %d)", r.Name, r.PID)
	}
	raw, err := os.ReadFile(keys[0])
	if err != nil {
		return err
	}
	var key struct {
		PeerToken string `json:"peerToken"`
	}
	if err := json.Unmarshal(raw, &key); err != nil || key.PeerToken == "" {
		return fmt.Errorf("unusable key file %s", keys[0])
	}

	content := fmt.Sprintf("<cross-session-message from-name=%q>\n%s\n</cross-session-message>", fromName, body)
	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": key.PeerToken})
	msg, _ := json.Marshal(map[string]any{
		"type":     "user",
		"message":  map[string]any{"role": "user", "content": content},
		"priority": "next",
		"msg_id":   rand.Text(),
	})

	conn, err := net.DialTimeout("unix", r.Socket, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append(append(append(auth, '\n'), msg...), '\n')); err != nil {
		return err
	}
	return nil
}
