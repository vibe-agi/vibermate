package nativemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type countedResponseBody struct {
	io.ReadCloser
	bytes  atomic.Int64
	onRead func()
}

func (b *countedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes.Add(int64(n))
	if n > 0 && b.onRead != nil {
		b.onRead()
	}
	return n, err
}

func (e *executor) consumeResponse(ctx context.Context, c CapturedRequest, request rpc, s *session, binding nativetool.Binding, response *http.Response, w http.ResponseWriter) error {
	if response.StatusCode == 202 || response.StatusCode == 204 {
		if request.kind != nativetool.RequestInitialized && request.kind != nativetool.RequestCancel && request.kind != nativetool.RequestServerReply && request.kind != nativetool.RequestDeleteSession {
			return errProtocol
		}
		var extra [1]byte
		if count, err := response.Body.Read(extra[:]); count != 0 || err != io.EOF {
			return errProtocol
		}
		w.WriteHeader(response.StatusCode)
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return errProtocol
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return errProtocol
	}
	idle := time.AfterFunc(e.limits.StreamIdle, func() { _ = response.Body.Close() })
	defer idle.Stop()
	if body, ok := response.Body.(*countedResponseBody); ok {
		body.onRead = func() { idle.Reset(e.limits.StreamIdle) }
	}
	if media == "text/event-stream" {
		return e.consumeSSE(ctx, c, request, s, binding, response, w, idle)
	}
	if media != "application/json" || request.kind == nativetool.RequestGetStream {
		return errProtocol
	}
	maximum := e.limits.ResultBytes
	if request.kind == nativetool.RequestToolsList {
		maximum = e.limits.CatalogPageBytes
	}
	raw, free, err := e.readBounded(response.Body, maximum)
	if err != nil {
		return err
	}
	defer free()
	if !e.budget.acquire(2 * int64(len(raw))) {
		return errBudget
	}
	defer e.budget.release(2 * int64(len(raw)))
	result, err := parseResponse(raw, e.limits)
	if err != nil {
		return err
	}
	if result.method != "" || !bytes.Equal(result.id, request.id) {
		return errProtocol
	}
	if err = e.acceptResult(ctx, request, result, s, binding); err != nil {
		return err
	}
	if err = e.retainResult(ctx, c.id, request, result, s); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	if request.kind == nativetool.RequestInitialize {
		w.Header().Set("Mcp-Session-Id", response.Header.Get("Mcp-Session-Id"))
	}
	w.WriteHeader(response.StatusCode)
	_, err = w.Write(raw)
	if err == nil {
		if tracked, ok := w.(*nativeWriter); ok {
			tracked.terminal = true
		}
	}
	return err
}
func (e *executor) acceptResult(ctx context.Context, request, result rpc, s *session, binding nativetool.Binding) error {
	if request.kind == nativetool.RequestInitialize {
		var initialize map[string]json.RawMessage
		if json.Unmarshal(result.object["result"], &initialize) != nil {
			return errProtocol
		}
		version, ok := text(initialize["protocolVersion"])
		if !ok || version != "2025-06-18" {
			return errProtocol
		}
		if _, err := decodeObject(initialize["capabilities"], e.limits.ResultBytes, e.limits.Depth); err != nil {
			return errProtocol
		}
		if _, err := decodeObject(initialize["serverInfo"], e.limits.ResultBytes, e.limits.Depth); err != nil {
			return errProtocol
		}
	}
	if request.kind == nativetool.RequestToolsList {
		if _, failure := result.object["error"]; failure {
			return errProtocol
		}
		return e.observeCatalogPage(request, s, binding, result.raw)
	}
	return nil
}
func (e *executor) retainResult(ctx context.Context, id nativetool.ID, request, result rpc, s *session) error {
	if request.kind != nativetool.RequestToolsCall || s.plan.ContentRecording().Mode != environment.ContentRecordingFull {
		return nil
	}
	name, _ := text(request.params["name"])
	profile, ok := e.contentProfile(s, name)
	if !ok {
		return nil
	}
	if body, permitted := retainedContent(result.object["result"], profile.result, e.limits); permitted {
		return e.dependencies.Store.PutContent(ctx, id, "tool_result", body, e.dependencies.Clock().UTC())
	}
	return nil
}

type sseEvent struct {
	id   string
	data []byte
	wire []byte
}

func parseSSEEvent(wire []byte, l Limits) (sseEvent, error) {
	if !utf8.Valid(wire) {
		return sseEvent{}, errProtocol
	}
	if int64(len(wire)) > l.EventBytes {
		return sseEvent{}, errBudget
	}
	event := sseEvent{wire: wire}
	var data [][]byte
	eventName := ""
	seenID, seenEvent := false, false
	for _, line := range bytes.Split(wire, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			continue
		}
		if line[0] == ':' {
			continue
		}
		parts := bytes.SplitN(line, []byte(":"), 2)
		if len(parts) != 2 {
			return sseEvent{}, errProtocol
		}
		value := parts[1]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(parts[0]) {
		case "event":
			if seenEvent {
				return sseEvent{}, errProtocol
			}
			seenEvent = true
			eventName = string(value)
		case "id":
			if seenID || len(value) == 0 || len(value) > l.OpaqueIDBytes || bytes.ContainsAny(value, "\x00\r\n") {
				return sseEvent{}, errProtocol
			}
			seenID = true
			event.id = string(value)
		case "data":
			data = append(data, value)
		default:
			return sseEvent{}, errProtocol
		}
	}
	if eventName != "" && eventName != "message" || len(data) == 0 && (eventName != "" || event.id != "") {
		return sseEvent{}, errProtocol
	}
	if len(data) == 0 {
		return event, nil
	}
	if len(data) == 1 {
		event.data = data[0]
	} else {
		event.data = bytes.Join(data, []byte("\n"))
	}
	return event, nil
}
func (e *executor) consumeSSE(ctx context.Context, c CapturedRequest, request rpc, s *session, binding nativetool.Binding, response *http.Response, w http.ResponseWriter, idle *time.Timer) error {
	maximum := e.limits.EventBytes
	if !e.budget.acquire(maximum) {
		return errBudget
	}
	defer e.budget.release(maximum)
	wire := make([]byte, maximum)
	length := 0
	var one [1]byte
	terminal := false
	headersSent := false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := response.Body.Read(one[:])
		if n != 0 {
			idle.Reset(e.limits.StreamIdle)
			if length == len(wire) {
				return errBudget
			}
			wire[length] = one[0]
			length++
			if one[0] == '\n' && (length >= 2 && wire[length-2] == '\n' || length >= 4 && string(wire[length-4:length]) == "\r\n\r\n") {
				event, err := parseSSEEvent(wire[:length], e.limits)
				if err != nil {
					return err
				}
				if request.kind == nativetool.RequestGetStream {
					s.mu.Lock()
					previous, known := s.events[event.id]
					same := known && equalEvent(previous, event.wire)
					s.mu.Unlock()
					if event.id == "" || !same {
						return errSession
					}
				} else if len(event.data) > 0 {
					if !e.budget.acquire(2 * int64(len(event.data))) {
						return errBudget
					}
					message, parseErr := parseResponse(event.data, e.limits)
					if parseErr == nil {
						parseErr = e.acceptSSEMessage(ctx, c.id, request, message, s, binding, &terminal)
					}
					e.budget.release(2 * int64(len(event.data)))
					if parseErr != nil {
						return parseErr
					}
					if event.id != "" {
						s.mu.Lock()
						if previous, known := s.events[event.id]; known {
							if !equalEvent(previous, event.wire) {
								s.mu.Unlock()
								return errSession
							}
						} else {
							if !e.budget.acquire(int64(length)) {
								s.mu.Unlock()
								return errBudget
							}
							s.events[event.id] = bytes.Clone(event.wire)
							s.retained += int64(length)
						}
						s.mu.Unlock()
					}
				}
				if !headersSent {
					w.Header().Set("Content-Type", "text/event-stream")
					if request.kind == nativetool.RequestInitialize {
						w.Header().Set("Mcp-Session-Id", response.Header.Get("Mcp-Session-Id"))
					}
					w.WriteHeader(http.StatusOK)
					headersSent = true
				}
				if _, err = w.Write(event.wire); err != nil {
					return err
				}
				if terminal {
					if tracked, ok := w.(*nativeWriter); ok {
						tracked.terminal = true
					}
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				length = 0
			}
		}
		if err == io.EOF {
			if length != 0 || !terminal && request.kind != nativetool.RequestGetStream {
				return errProtocol
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}
func (e *executor) acceptSSEMessage(ctx context.Context, operation nativetool.ID, request, message rpc, s *session, binding nativetool.Binding, terminal *bool) error {
	if message.method == "" {
		if *terminal || !bytes.Equal(message.id, request.id) {
			return errProtocol
		}
		if err := e.acceptResult(ctx, request, message, s, binding); err != nil {
			return err
		}
		if err := e.retainResult(ctx, operation, request, message, s); err != nil {
			return err
		}
		*terminal = true
		return nil
	}
	if *terminal {
		return errProtocol
	}
	if len(message.id) > 0 {
		return e.observeCallback(s, operation, binding, message)
	}
	switch message.method {
	case "notifications/progress":
		if !allowed(message.params, "progressToken", "progress", "total", "message", "_meta") {
			return errProtocol
		}
		var meta map[string]json.RawMessage
		_ = json.Unmarshal(request.params["_meta"], &meta)
		token, err := identity(message.params["progressToken"], e.limits.OpaqueIDBytes)
		if err != nil {
			return errProtocol
		}
		expected, err := identity(meta["progressToken"], e.limits.OpaqueIDBytes)
		if err != nil || !bytes.Equal(token, expected) {
			return errProtocol
		}
		var progress json.Number
		if json.Unmarshal(message.params["progress"], &progress) != nil {
			return errProtocol
		}
		return nil
	case "notifications/tools/list_changed":
		if len(message.params) > 0 && !allowed(message.params, "_meta") {
			return errProtocol
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		revision, err := e.dependencies.Store.InvalidateCatalog(ctx, s.id, s.revision, e.dependencies.Clock().UTC())
		if err != nil {
			return err
		}
		s.revision = revision
		s.generation = 0
		s.catalog = nil
		s.retained -= s.publicBytes
		e.budget.release(s.publicBytes)
		s.publicBytes = 0
		s.nextCursor = ""
		return nil
	case "notifications/elicitation/complete":
		if !allowed(message.params, "elicitationId", "_meta") {
			return errProtocol
		}
		id, ok := text(message.params["elicitationId"])
		if !ok || strings.TrimSpace(id) == "" {
			return errProtocol
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, observed := s.elicitations[id]; observed {
			delete(s.elicitations, id)
			charge := int64(128 + len(id))
			s.retained -= charge
			e.budget.release(charge)
			return nil
		}
		return errSession
	default:
		return errProtocol
	}
}
