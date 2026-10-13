package nativemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type rpc struct {
	raw                []byte
	object             map[string]json.RawMessage
	params             map[string]json.RawMessage
	method             string
	id                 []byte
	kind               nativetool.RequestKind
	failure            *bool
	renewalUnavailable *bool
	collection         uint64
	collectionToken    *uint64
	cancel             context.CancelFunc
	retention          *retentionState
	stream             *streamRecord
	resumeOrdinal      int
}

// Allocation preflight counts every member/container before allocating key
// sets or maps. Wire-backed RawMessages avoid nested payload copies.
func jsonAllocation(raw []byte) int64 {
	// Include allocator rounding and fixed decoder/map scratch in addition to
	// payload strings, members, and element slices.
	cost := int64(len(raw)) + 4096
	quoted, escaped := false, false
	var containers [65]byte
	depth := 0
	for _, ch := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		if ch == '"' {
			quoted = true
			continue
		}
		switch ch {
		case ':':
			cost += 256
		case '{', '[':
			cost += 128
			if depth < 65 {
				containers[depth] = ch
			}
			depth++
			if ch == '[' {
				cost += 128
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth > 0 && depth <= 65 && containers[depth-1] == '[' {
				cost += 128
			}
		}
	}
	return cost
}

// Token scan rejects duplicate keys at every depth before DTO decoding.
func decodeObject(raw []byte, maximum int64, depth int) (map[string]json.RawMessage, error) {
	if int64(len(raw)) > maximum {
		return nil, errBudget
	}
	if !utf8.Valid(raw) {
		return nil, errProtocol
	}
	if !json.Valid(raw) {
		return nil, errProtocol
	}
	if jsonAllocation(raw) > DefaultLimits().BufferBytes {
		return nil, errBudget
	}
	object := map[string]json.RawMessage{}
	position := 0
	space := func() {
		for position < len(raw) && bytes.ContainsRune([]byte(" \n\t\r"), rune(raw[position])) {
			position++
		}
	}
	var scanString func() (int, error)
	scanString = func() (int, error) {
		start := position
		position++
		for position < len(raw) {
			ch := raw[position]
			position++
			if ch == '"' {
				return start, nil
			}
			if ch == '\\' {
				escape := raw[position]
				position++
				if escape == 'u' {
					code, _ := strconv.ParseUint(string(raw[position:position+4]), 16, 16)
					position += 4
					if code >= 0xd800 && code <= 0xdbff {
						if position+6 > len(raw) || string(raw[position:position+2]) != "\\u" {
							return 0, errProtocol
						}
						low, err := strconv.ParseUint(string(raw[position+2:position+6]), 16, 16)
						if err != nil || low < 0xdc00 || low > 0xdfff {
							return 0, errProtocol
						}
						position += 6
					} else if code >= 0xdc00 && code <= 0xdfff {
						return 0, errProtocol
					}
				}
			}
		}
		return 0, errProtocol
	}
	var scan func(int) error
	scan = func(level int) error {
		space()
		switch raw[position] {
		case '{', '[':
			if level > depth {
				return errBudget
			}
			container := raw[position]
			position++
			space()
			close := byte('}')
			if container == '[' {
				close = ']'
			}
			seen := map[string]bool{}
			for raw[position] != close {
				if container == '{' {
					start, err := scanString()
					if err != nil {
						return err
					}
					var key string
					_ = json.Unmarshal(raw[start:position], &key)
					if seen[key] {
						return errProtocol
					}
					seen[key] = true
					space()
					position++
					space()
					valueStart := position
					if err := scan(level + 1); err != nil {
						return err
					}
					if level == 1 {
						object[key] = raw[valueStart:position]
					}
				} else {
					if err := scan(level + 1); err != nil {
						return err
					}
				}
				space()
				if raw[position] == ',' {
					position++
					space()
				} else {
					break
				}
			}
			position++
		case '"':
			_, err := scanString()
			return err
		default:
			for position < len(raw) && !bytes.ContainsRune([]byte(" \n\t\r,]}"), rune(raw[position])) {
				position++
			}
		}
		return nil
	}
	space()
	if position >= len(raw) || raw[position] != '{' {
		return nil, errProtocol
	}
	if err := scan(1); err != nil {
		return nil, err
	}
	space()
	if position != len(raw) {
		return nil, errProtocol
	}
	return object, nil
}
func objectField(raw []byte, l Limits) (map[string]json.RawMessage, error) {
	return decodeObject(raw, l.RequestBytes, l.Depth)
}
func allowed(o map[string]json.RawMessage, names ...string) bool {
	for key := range o {
		found := false
		for _, name := range names {
			if key == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func text(raw []byte) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil
}
func identity(raw []byte, max int) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if d.Decode(&value) != nil {
		return nil, errProtocol
	}
	switch v := value.(type) {
	case string:
		if len(v) == 0 || len(v) > max || !utf8.ValidString(v) {
			return nil, errProtocol
		}
	case json.Number:
		if _, err := strconv.ParseInt(string(v), 10, 64); err != nil {
			return nil, errProtocol
		}
	default:
		return nil, errProtocol
	}
	return json.Marshal(value)
}
func parseRequest(raw []byte, l Limits) (rpc, error) {
	o, err := decodeObject(raw, l.RequestBytes, l.Depth)
	if err != nil {
		return rpc{}, err
	}
	v, ok := text(o["jsonrpc"])
	if !ok || v != "2.0" {
		return rpc{}, errProtocol
	}
	r := rpc{raw: raw, object: o}
	method, hasMethod := o["method"]
	if !hasMethod {
		r.kind = nativetool.RequestServerReply
		if !allowed(o, "jsonrpc", "id", "result", "error") {
			return rpc{}, errProtocol
		}
		_, result := o["result"]
		_, failure := o["error"]
		if result == failure {
			return rpc{}, errProtocol
		}
		r.id, err = identity(o["id"], l.OpaqueIDBytes)
		if err != nil {
			return rpc{}, err
		}
		value := o["result"]
		if failure {
			value = o["error"]
		}
		if _, err = decodeObject(value, l.RequestBytes, l.Depth); err != nil {
			return rpc{}, err
		}
		return r, nil
	}
	if !allowed(o, "jsonrpc", "id", "method", "params", "_meta") {
		return rpc{}, errProtocol
	}
	r.method, ok = text(method)
	if !ok {
		return rpc{}, errProtocol
	}
	if meta, present := o["_meta"]; present {
		if _, err = objectField(meta, l); err != nil {
			return rpc{}, err
		}
	}
	if params, present := o["params"]; present {
		r.params, err = objectField(params, l)
		if err != nil {
			return rpc{}, err
		}
	} else {
		r.params = map[string]json.RawMessage{}
	}
	switch r.method {
	case "initialize":
		r.kind = nativetool.RequestInitialize
		if !allowed(r.params, "protocolVersion", "capabilities", "clientInfo", "_meta") {
			return rpc{}, errProtocol
		}
		version, ok := text(r.params["protocolVersion"])
		if !ok || version != "2025-06-18" {
			return rpc{}, errProtocol
		}
		if info, present := r.params["clientInfo"]; present {
			object, err := objectField(info, l)
			if err != nil || !implementation(object) {
				return rpc{}, errProtocol
			}
		}
	case "notifications/initialized":
		r.kind = nativetool.RequestInitialized
		if !allowed(r.params, "_meta") {
			return rpc{}, errProtocol
		}
	case "tools/list":
		r.kind = nativetool.RequestToolsList
		if !allowed(r.params, "cursor", "_meta") {
			return rpc{}, errProtocol
		}
		if cursor, present := r.params["cursor"]; present {
			v, ok := text(cursor)
			if !ok || v == "" || len(v) > l.OpaqueIDBytes {
				return rpc{}, errProtocol
			}
		}
	case "tools/call":
		r.kind = nativetool.RequestToolsCall
		if !allowed(r.params, "name", "arguments", "_meta") {
			return rpc{}, errProtocol
		}
		name, ok := text(r.params["name"])
		if !ok || name == "" || len(name) > 256 {
			return rpc{}, errProtocol
		}
		if args, present := r.params["arguments"]; present {
			if _, err = objectField(args, l); err != nil {
				return rpc{}, err
			}
		}
	case "ping":
		r.kind = nativetool.RequestPing
		if !allowed(r.params, "_meta") {
			return rpc{}, errProtocol
		}
	case "notifications/cancelled":
		r.kind = nativetool.RequestCancel
		if !allowed(r.params, "requestId", "reason", "_meta") {
			return rpc{}, errProtocol
		}
		if _, err = identity(r.params["requestId"], l.OpaqueIDBytes); err != nil {
			return rpc{}, err
		}
		if reason, present := r.params["reason"]; present {
			if _, ok := text(reason); !ok {
				return rpc{}, errProtocol
			}
		}
	default:
		return rpc{}, errProtocol
	}
	if r.kind == nativetool.RequestInitialized || r.kind == nativetool.RequestCancel {
		if _, present := o["id"]; present {
			return rpc{}, errProtocol
		}
	} else {
		r.id, err = identity(o["id"], l.OpaqueIDBytes)
		if err != nil {
			return rpc{}, err
		}
	}
	return r, nil
}
func parseResponse(raw []byte, l Limits) (rpc, error) {
	o, err := decodeObject(raw, l.ResultBytes, l.Depth)
	if err != nil {
		return rpc{}, err
	}
	version, ok := text(o["jsonrpc"])
	if !ok || version != "2.0" {
		return rpc{}, errProtocol
	}
	r := rpc{raw: raw, object: o}
	if method, present := o["method"]; present {
		r.method, ok = text(method)
		if !ok || !allowed(o, "jsonrpc", "id", "method", "params") {
			return rpc{}, errProtocol
		}
		if r.params, err = decodeObject(o["params"], l.EventBytes, l.Depth); err != nil && r.method != "ping" {
			return rpc{}, err
		}
		if id, present := o["id"]; present {
			r.id, err = identity(id, l.OpaqueIDBytes)
		}
		return r, err
	}
	if !allowed(o, "jsonrpc", "id", "result", "error") {
		return rpc{}, errProtocol
	}
	_, result := o["result"]
	_, failure := o["error"]
	if result == failure {
		return rpc{}, errProtocol
	}
	r.id, err = identity(o["id"], l.OpaqueIDBytes)
	if err != nil {
		return rpc{}, err
	}
	value := o["result"]
	if failure {
		value = o["error"]
	}
	if _, err = decodeObject(value, l.ResultBytes, l.Depth); err != nil {
		return rpc{}, err
	}
	return r, nil
}
