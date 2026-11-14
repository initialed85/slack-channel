package main

// A deliberately tiny MCP server over stdio: just enough of JSON-RPC for Claude Code to load us as a
// channel (experimental `claude/channel` capability) and call a couple of tools. Hand-rolled because
// the channel notification is a Claude Code extension and this is ~100 lines.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	handler     func(args map[string]any) (string, error)
}

type mcpServer struct {
	name         string
	instructions string
	tools        []tool

	mu  sync.Mutex
	out *json.Encoder
}

func newMCPServer(name, instructions string, tools []tool) *mcpServer {
	return &mcpServer{name: name, instructions: instructions, tools: tools, out: json.NewEncoder(os.Stdout)}
}

func (s *mcpServer) write(m rpcMsg) {
	m.JSONRPC = "2.0"
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.out.Encode(m); err != nil {
		logf("mcp write: %v", err)
	}
}

// notifyChannel pushes an event into the Claude Code session. meta keys must be [A-Za-z0-9_]+.
func (s *mcpServer) notifyChannel(content string, meta map[string]string) {
	params, _ := json.Marshal(map[string]any{"content": content, "meta": meta})
	s.write(rpcMsg{Method: "notifications/claude/channel", Params: params})
}

// serve blocks reading requests from stdin until Claude Code closes it.
func (s *mcpServer) serve() error {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var req rpcMsg
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			logf("mcp: bad json: %v", err)
			continue
		}
		if req.ID == nil {
			continue // a notification (initialized, cancelled, ...); nothing to say back
		}
		go s.handle(req)
	}
	return sc.Err()
}

func (s *mcpServer) handle(req rpcMsg) {
	reply := func(result any) { s.write(rpcMsg{ID: req.ID, Result: result}) }
	fail := func(code int, msg string) { s.write(rpcMsg{ID: req.ID, Error: &rpcError{Code: code, Message: msg}}) }

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		reply(map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities": map[string]any{
				"experimental": map[string]any{"claude/channel": map[string]any{}},
				"tools":        map[string]any{},
			},
			"serverInfo":   map[string]any{"name": s.name, "version": "0.1.0"},
			"instructions": s.instructions,
		})
	case "ping":
		reply(map[string]any{})
	case "tools/list":
		reply(map[string]any{"tools": s.tools})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			fail(-32602, err.Error())
			return
		}
		for _, t := range s.tools {
			if t.Name != p.Name {
				continue
			}
			text, err := t.handler(p.Arguments)
			if err != nil {
				reply(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}})
				return
			}
			reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
			return
		}
		fail(-32602, fmt.Sprintf("unknown tool %q", p.Name))
	default:
		fail(-32601, "method not found: "+req.Method)
	}
}

func strArg(args map[string]any, k string) string {
	v, _ := args[k].(string)
	return v
}
