package nativemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	cost := jsonAllocation(raw)
	if !e.budget.acquire(cost) {
		return errBudget
	}
	defer e.budget.release(cost)
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
	if err = e.observeActivity(ctx, request, s, true); err != nil {
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
	if err := validateMethodResult(request.kind, result, e.limits); err != nil {
		return err
	}
	if _, failure := result.object["error"]; failure {
		if request.failure != nil {
			*request.failure = true
		}
		return nil
	}
	if request.kind == nativetool.RequestInitialize {
		s.mu.Lock()
		hasSession := s.wire != "" && s.hasher != nil
		s.mu.Unlock()
		if !hasSession {
			return errSession
		}
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
	if request.retention == nil {
		return nil
	}
	profile, ok := request.retention.profile, request.retention.known
	if !ok {
		return nil
	}
	if body, permitted := retainedToolResult(result.object["result"], profile.result, e.limits); permitted {
		if err := e.dependencies.Store.PutContent(ctx, id, "tool_result", body, e.dependencies.Clock().UTC()); err != nil {
			request.retention.unavailable = true
		}
		return nil
	}
	return nil
}

type sseEvent struct {
	id   string
	data []byte
	wire []byte
	free func()
}

func parseSSEEvent(wire []byte, l Limits, budgets ...*memoryBudget) (sseEvent, error) {
	if !utf8.Valid(wire) {
		return sseEvent{}, errProtocol
	}
	if int64(len(wire)) > l.EventBytes {
		return sseEvent{}, errBudget
	}
	event := sseEvent{wire: wire, free: func() {}}
	dataCount, dataBytes := 0, 0
	eventName := ""
	seenID, seenEvent := false, false
	for offset := 0; offset < len(wire); {
		end := bytes.IndexByte(wire[offset:], '\n')
		if end < 0 {
			end = len(wire) - offset
		}
		line := wire[offset : offset+end]
		offset += end + 1
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			continue
		}
		if line[0] == ':' {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			return sseEvent{}, errProtocol
		}
		value := line[colon+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(line[:colon]) {
		case "event":
			if seenEvent {
				return sseEvent{}, errProtocol
			}
			seenEvent = true
			if !bytes.Equal(value, []byte("message")) {
				return sseEvent{}, errProtocol
			}
			eventName = "message"
		case "id":
			if seenID || len(value) == 0 || len(value) > l.OpaqueIDBytes || bytes.ContainsAny(value, "\x00\r\n") {
				return sseEvent{}, errProtocol
			}
			seenID = true
			event.id = string(value)
		case "data":
			dataCount++
			dataBytes += len(value)
			if dataCount == 1 {
				event.data = value
			}
		default:
			return sseEvent{}, errProtocol
		}
	}
	if dataCount == 0 && (eventName != "" || event.id != "") {
		return sseEvent{}, errProtocol
	}
	if dataCount == 0 {
		return event, nil
	}
	if dataCount > 1 {
		size := int64(dataBytes + dataCount - 1)
		if size > l.EventBytes {
			return sseEvent{}, errBudget
		}
		if len(budgets) > 0 {
			budget := budgets[0]
			if !budget.acquire(size) {
				return sseEvent{}, errBudget
			}
			event.free = func() { budget.release(size) }
		}
		joined := make([]byte, 0, int(size))
		firstData := true
		for offset := 0; offset < len(wire); {
			end := bytes.IndexByte(wire[offset:], '\n')
			if end < 0 {
				end = len(wire) - offset
			}
			line := bytes.TrimSuffix(wire[offset:offset+end], []byte("\r"))
			offset += end + 1
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			value := line[5:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			if !firstData {
				joined = append(joined, '\n')
			}
			firstData = false
			joined = append(joined, value...)
		}
		event.data = joined
	}
	return event, nil
}
func (e *executor) consumeSSE(ctx context.Context, c CapturedRequest, request rpc, s *session, binding nativetool.Binding, response *http.Response, w http.ResponseWriter, idle *time.Timer) error {
	stream := request.stream
	if stream == nil {
		return errSession
	}
	physicalRequest := request
	request = stream.request
	binding = stream.binding
	lastReplay := physicalRequest.resumeOrdinal
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
	var eventFree func()
	defer func() {
		if eventFree != nil {
			eventFree()
		}
	}()
	for {
		if ctx.Err() != nil {
			if length == 0 {
				return errStreamDetached
			}
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
				event, err := parseSSEEvent(wire[:length], e.limits, &e.budget)
				if err != nil {
					return err
				}
				eventFree = event.free
				skip, acceptErr := e.acceptStreamEvent(ctx, s, stream, physicalRequest.resumeOrdinal, &lastReplay, event, &terminal)
				if acceptErr != nil {
					return acceptErr
				}
				if skip {
					event.free()
					eventFree = nil
					length = 0
					continue
				}
				if !headersSent {
					w.Header().Set("Content-Type", "text/event-stream")
					if request.kind == nativetool.RequestInitialize {
						w.Header().Set("Mcp-Session-Id", response.Header.Get("Mcp-Session-Id"))
					}
					w.WriteHeader(http.StatusOK)
					headersSent = true
				}
				written, writeErr := w.Write(event.wire)
				if writeErr != nil {
					return errors.Join(errStreamDelivery, writeErr)
				}
				if written != len(event.wire) {
					return errors.Join(errStreamDelivery, io.ErrShortWrite)
				}
				if err = flushStream(w); err != nil {
					return errors.Join(errStreamDelivery, err)
				}
				if terminal {
					s.mu.Lock()
					stream.delivered = true
					s.mu.Unlock()
				}
				if terminal {
					if tracked, ok := w.(*nativeWriter); ok {
						tracked.terminal = true
					}
				}
				length = 0
				if err = e.observeActivity(ctx, request, s, terminal); err != nil {
					return err
				}
				event.free()
				eventFree = nil
			}
		}
		if err == io.EOF {
			if length != 0 {
				return errProtocol
			}
			if !terminal && request.kind != nativetool.RequestGetStream {
				return errStreamDetached
			}
			return nil
		}
		if err != nil {
			if length == 0 && !terminal {
				return errStreamDetached
			}
			return err
		}
	}
}

func (e *executor) acceptStreamEvent(ctx context.Context, s *session, stream *streamRecord, cursor int, last *int, event sseEvent, terminal *bool) (bool, error) {
	// Serialize acceptance across this session's streams. The event-ID claim
	// is checked before semantic effects, and no network write holds this lock.
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	e.reclaimDeliveredStreams(s)
	if event.id != "" {
		if err := e.observeStream(ctx, s, stream); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	previous, known := s.eventIndex[event.id]
	if stream.retired || known && (previous.stream != stream || !equalEvent(stream.events[previous.ordinal].wire, event.wire)) {
		s.mu.Unlock()
		return false, errSession
	}
	if known {
		if cursor < 0 || previous.ordinal <= cursor {
			s.mu.Unlock()
			return true, nil
		}
		if previous.ordinal <= *last {
			s.mu.Unlock()
			return false, errSession
		}
		*last = previous.ordinal
	}
	charge := int64(0)
	if !known && event.id != "" {
		charge = int64(len(event.wire) + len(event.id) + 256)
		if len(s.eventIndex) >= e.limits.Pending || !e.budget.acquire(charge) {
			s.mu.Unlock()
			return false, errBudget
		}
	}
	s.mu.Unlock()
	defer func() {
		if charge > 0 {
			e.budget.release(charge)
		}
	}()
	request := stream.request
	if !known && event.id == "" && len(event.data) > 0 {
		s.mu.Lock()
		stream.unidentifiable = true
		s.mu.Unlock()
	}
	if !known && len(event.data) > 0 {
		cost := jsonAllocation(event.data)
		if !e.budget.acquire(cost) {
			return false, errBudget
		}
		defer e.budget.release(cost)
		message, err := parseResponse(event.data, e.limits)
		if err != nil {
			return false, err
		}
		if message.method != "" && len(message.id) > 0 {
			if err := e.observeStream(ctx, s, stream); err != nil {
				return false, err
			}
		}
		if err := e.acceptSSEMessage(ctx, stream.operation, request, message, s, stream.binding, terminal); err != nil {
			return false, err
		}
		if *terminal {
			outcome, reason := "completed", "completed"
			if request.failure != nil && *request.failure {
				outcome, reason = "failed", "rpc_error"
			}
			s.mu.Lock()
			stream.knownTerminal = &nativetool.LogicalFinishInput{Outcome: outcome, Reason: reason, ObservedAt: e.dependencies.Clock().UTC()}
			s.mu.Unlock()
		}
	}
	if !known && event.id != "" {
		s.mu.Lock()
		if stream.retired {
			s.mu.Unlock()
			return false, errSession
		}
		s.eventIndex[event.id] = streamCursor{stream: stream, ordinal: len(stream.events)}
		stream.events = append(stream.events, streamEvent{id: event.id, wire: bytes.Clone(event.wire)})
		stream.charged += charge
		s.retained += charge
		charge = 0
		s.mu.Unlock()
	}
	s.mu.Lock()
	observed := stream.observed
	s.mu.Unlock()
	if *terminal && !known && observed {
		outcome, reason := "completed", "completed"
		if request.failure != nil && *request.failure {
			outcome, reason = "failed", "rpc_error"
		}
		if err := e.dependencies.Store.CompleteStreamRPC(ctx, stream.authority, nativetool.LogicalFinishInput{Outcome: outcome, Reason: reason, ObservedAt: e.dependencies.Clock().UTC()}); err != nil {
			return false, err
		}
		s.mu.Lock()
		stream.terminal = true
		stream.terminalCursor = event.id != ""
		delete(s.active, string(request.id))
		e.retireStreamToolLocked(s, stream.operation)
		s.mu.Unlock()
		e.retireCompletedParents(ctx, s, stream.operation)
	}
	return false, nil
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
		s.mu.Lock()
		stream := s.streams[operation]
		s.mu.Unlock()
		if stream == nil {
			return errSession
		}
		return e.observeCallback(ctx, s, stream, message)
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
		e.discardCatalog(s)
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
