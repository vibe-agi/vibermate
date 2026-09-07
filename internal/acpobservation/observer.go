package acpobservation

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"unicode/utf8"
)

// Observer consumes received bytes before they are offered to the other peer.
// A complete request is therefore known before its response can be read. This
// establishes observation, not successful delivery; interrupted transfers mark
// the final projection incomplete. Parsing is bounded and performs no I/O.
type Observer struct {
	mu        sync.Mutex
	content   bool
	streams   [2]frameStream
	snapshot  Snapshot
	pending   map[string]pendingRequest
	active    map[string]int
	textBytes int
}

type frameStream struct {
	bytes    []byte
	skipping bool
}
type pendingRequest struct {
	method, session, cwd string
	prompt               int
}
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func NewObserver(content bool) *Observer {
	return &Observer{content: content, snapshot: Snapshot{Revision: 1, Sessions: []Session{}, Prompts: []Prompt{}}, pending: map[string]pendingRequest{}, active: map[string]int{}}
}

func (observer *Observer) Feed(client bool, data []byte) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.snapshot.Final {
		return
	}
	index := 0
	if !client {
		index = 1
	}
	stream := &observer.streams[index]
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		part := data
		if end >= 0 {
			part = data[:end]
			data = data[end+1:]
		} else {
			data = nil
		}
		if !stream.skipping {
			if len(stream.bytes)+len(part) > MaxFrameBytes {
				clear(stream.bytes)
				stream.bytes = nil
				stream.skipping = true
				observer.snapshot.Incomplete = true
				observer.snapshot.Revision++
			} else {
				stream.bytes = append(stream.bytes, part...)
			}
		}
		if end >= 0 {
			if !stream.skipping && len(bytes.TrimSpace(stream.bytes)) > 0 {
				observer.frame(client, stream.bytes)
			}
			clear(stream.bytes)
			stream.bytes = stream.bytes[:0]
			stream.skipping = false
		}
	}
}

func rpcID(raw json.RawMessage) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if len(raw) > 512 || decoder.Decode(&value) != nil {
		return ""
	}
	switch id := value.(type) {
	case string:
		return "s:" + id
	case json.Number:
		return "n:" + id.String()
	default:
		return ""
	}
}

func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (observer *Observer) frame(client bool, frame []byte) {
	var message envelope
	if !utf8.Valid(frame) || json.Unmarshal(frame, &message) != nil {
		observer.snapshot.Incomplete = true
		observer.snapshot.Revision++
		return
	}
	if client && message.Method != "" {
		switch message.Method {
		case "initialize", "authenticate", "session/new", "session/load", "session/resume", "session/fork", "session/prompt":
		default:
			return
		}
		id := rpcID(message.ID)
		if id == "" || len(observer.pending) >= MaxPrompts {
			observer.snapshot.Incomplete = true
			observer.snapshot.Revision++
			return
		}
		if _, exists := observer.pending[id]; exists {
			observer.snapshot.Incomplete = true
			observer.snapshot.Revision++
			return
		}
		if message.Method == "authenticate" {
			// Remember only the RPC identity; never inspect login parameters.
			observer.pending[id] = pendingRequest{method: "authenticate", prompt: -1}
			return
		}
		var params struct {
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
			Prompt    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"prompt"`
		}
		if json.Unmarshal(message.Params, &params) != nil || len(params.SessionID) > 512 || len(params.CWD) > 4096 {
			observer.snapshot.Incomplete = true
			observer.snapshot.Revision++
			return
		}
		request := pendingRequest{method: message.Method, session: params.SessionID, cwd: params.CWD, prompt: -1}
		if message.Method == "session/prompt" {
			if !observer.hasSession(params.SessionID) || len(observer.snapshot.Prompts) >= MaxPrompts {
				observer.snapshot.Incomplete = true
				observer.snapshot.Revision++
				return
			}
			request.prompt = len(observer.snapshot.Prompts)
			prompt := Prompt{Sequence: uint64(request.prompt + 1), SessionID: params.SessionID, State: "pending"}
			if observer.content {
				for _, block := range params.Prompt {
					if block.Type == "text" {
						prompt.UserText += observer.retainText(block.Text)
					}
				}
			}
			observer.snapshot.Prompts = append(observer.snapshot.Prompts, prompt)
			if _, exists := observer.active[params.SessionID]; exists {
				observer.active[params.SessionID] = -1
				observer.snapshot.Incomplete = true
			} else {
				observer.active[params.SessionID] = request.prompt
			}
		}
		observer.pending[id] = request
		observer.snapshot.Revision++
		return
	}
	if !client && message.Method == "" {
		id := rpcID(message.ID)
		request, found := observer.pending[id]
		if !found {
			return
		}
		delete(observer.pending, id)
		observer.snapshot.Revision++
		failed := len(message.Error) != 0 && !bytes.Equal(message.Error, []byte("null"))
		if failed {
			var failure struct {
				Code int `json:"code"`
			}
			if json.Unmarshal(message.Error, &failure) == nil && failure.Code == -32000 {
				observer.snapshot.NeedsAuthentication = true
			}
		}
		var result struct {
			SessionID       string `json:"sessionId"`
			ProtocolVersion uint32 `json:"protocolVersion"`
			AgentInfo       Agent  `json:"agentInfo"`
			StopReason      string `json:"stopReason"`
		}
		if !failed && (len(message.Result) == 0 || json.Unmarshal(message.Result, &result) != nil) {
			observer.snapshot.Incomplete = true
			failed = true
		}
		switch request.method {
		case "authenticate":
			if !failed {
				observer.snapshot.NeedsAuthentication = false
			}
		case "initialize":
			if !failed {
				observer.snapshot.ProtocolVersion = result.ProtocolVersion
				observer.snapshot.Agent = Agent{Name: bounded(result.AgentInfo.Name, 256), Version: bounded(result.AgentInfo.Version, 128)}
			}
		case "session/new", "session/load", "session/resume", "session/fork":
			if failed {
				return
			}
			id := result.SessionID
			if id == "" && (request.method == "session/load" || request.method == "session/resume") {
				id = request.session
			}
			if id == "" || len(id) > 512 {
				observer.snapshot.Incomplete = true
				return
			}
			if observer.hasSession(id) {
				return
			}
			if len(observer.snapshot.Sessions) >= MaxSessions {
				observer.snapshot.Incomplete = true
				return
			}
			observer.snapshot.Sessions = append(observer.snapshot.Sessions, Session{ID: id, CWD: request.cwd, Operation: strings.TrimPrefix(request.method, "session/")})
		case "session/prompt":
			if request.prompt < 0 {
				return
			}
			prompt := &observer.snapshot.Prompts[request.prompt]
			delete(observer.active, request.session)
			if failed {
				prompt.State = "failed"
				return
			}
			prompt.State = "completed"
			observer.snapshot.NeedsAuthentication = false
			switch result.StopReason {
			case "end_turn", "max_tokens", "max_turn_requests", "refusal":
				prompt.StopReason = result.StopReason
			case "cancelled":
				prompt.State = "cancelled"
				prompt.StopReason = "cancelled"
			default:
				prompt.StopReason = "other"
			}
		}
		return
	}
	if !client && message.Method == "_auth/status_update" {
		var params struct {
			AuthStatus struct {
				Kind string `json:"kind"`
			} `json:"authStatus"`
		}
		if json.Unmarshal(message.Params, &params) == nil && params.AuthStatus.Kind == "none" {
			observer.snapshot.NeedsAuthentication = true
			observer.snapshot.Revision++
		}
		return
	}
	if client || message.Method != "session/update" {
		return
	}
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		observer.snapshot.Incomplete = true
		observer.snapshot.Revision++
		return
	}
	index, ok := observer.active[params.SessionID]
	if !ok || index < 0 {
		return
	} // Includes session/load replay: never fresh usage.
	prompt := &observer.snapshot.Prompts[index]
	switch params.Update.SessionUpdate {
	case "agent_message_chunk":
		if observer.content && params.Update.Content.Type == "text" {
			prompt.AgentText += observer.retainText(params.Update.Content.Text)
			observer.snapshot.Revision++
		}
	case "tool_call":
		prompt.ToolCalls++
		observer.snapshot.Revision++
	}
}

func (observer *Observer) hasSession(id string) bool {
	for _, session := range observer.snapshot.Sessions {
		if session.ID == id {
			return true
		}
	}
	return false
}

func (observer *Observer) retainText(text string) string {
	kept := bounded(text, MaxTextBytes-observer.textBytes)
	if len(kept) != len(text) {
		observer.snapshot.Incomplete = true
	}
	observer.textBytes += len(kept)
	return kept
}

func (observer *Observer) Snapshot() Snapshot {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	result := observer.snapshot
	result.Sessions = append([]Session{}, result.Sessions...)
	result.Prompts = append([]Prompt{}, result.Prompts...)
	if result.ExitCode != nil {
		value := *result.ExitCode
		result.ExitCode = &value
	}
	// Raw string budgets alone do not bound JSON: escaping can expand a byte
	// sixfold. Keep the wire/storage budget deterministic, without ever editing
	// the relayed stream or corrupting an opaque session ID. Prefer identities
	// and outcomes over text, then workspace claims, then the newest prompts.
	encoded, _ := json.Marshal(result)
	size := len(encoded)
	if size >= MaxSnapshotBytes {
		result.Incomplete = true
		clearText := func(value *string) {
			if size >= MaxSnapshotBytes && *value != "" {
				encoded, _ := json.Marshal(*value)
				size -= len(encoded) - 2
				*value = ""
			}
		}
		for index := len(result.Prompts) - 1; index >= 0; index-- {
			clearText(&result.Prompts[index].AgentText)
			clearText(&result.Prompts[index].UserText)
		}
		for index := len(result.Sessions) - 1; index >= 0; index-- {
			clearText(&result.Sessions[index].CWD)
		}
		for size >= MaxSnapshotBytes && len(result.Prompts) > 0 {
			last := len(result.Prompts) - 1
			encoded, _ := json.Marshal(result.Prompts[last])
			size -= len(encoded)
			result.Prompts = result.Prompts[:last]
		}
	}
	return result
}

func (observer *Observer) Finish(exitCode int, incomplete bool) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.snapshot.Final {
		return
	}
	if len(observer.pending) > 0 {
		incomplete = true
	}
	for index := range observer.streams {
		stream := &observer.streams[index]
		if len(stream.bytes) != 0 || stream.skipping {
			incomplete = true
		}
		clear(stream.bytes)
		stream.bytes = nil
	}
	for index := range observer.snapshot.Prompts {
		if observer.snapshot.Prompts[index].State == "pending" {
			observer.snapshot.Prompts[index].State = "interrupted"
			incomplete = true
		}
	}
	observer.pending = nil
	observer.active = nil
	observer.snapshot.ExitCode = &exitCode
	observer.snapshot.Final = true
	observer.snapshot.Incomplete = observer.snapshot.Incomplete || incomplete
	observer.snapshot.Revision++
}
