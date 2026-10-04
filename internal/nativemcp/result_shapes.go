package nativemcp

import (
	"bytes"
	"encoding/json"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Return borrowed element slices, not a second copy of every content payload.
func contentElements(raw []byte) ([]json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' || !json.Valid(raw) {
		return nil, false
	}
	inside := bytes.TrimSpace(raw[1 : len(raw)-1])
	if len(inside) == 0 {
		return []json.RawMessage{}, true
	}
	var elements []json.RawMessage
	start, depth := 0, 0
	quoted, escaped := false, false
	for index, ch := range inside {
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
		switch ch {
		case '"':
			quoted = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				elements = append(elements, bytes.TrimSpace(inside[start:index]))
				start = index + 1
			}
		}
	}
	elements = append(elements, bytes.TrimSpace(inside[start:]))
	return elements, true
}

func implementation(object map[string]json.RawMessage) bool {
	for _, key := range []string{"name", "version"} {
		if _, ok := text(object[key]); !ok {
			return false
		}
	}
	for _, key := range []string{"title", "description", "websiteUrl"} {
		if raw, present := object[key]; present {
			if _, ok := text(raw); !ok {
				return false
			}
		}
	}
	return true
}
func rpcError(raw []byte, l Limits) bool {
	object, err := decodeObject(raw, l.ResultBytes, l.Depth)
	if err != nil || !allowed(object, "code", "message", "data") {
		return false
	}
	var code int32
	if json.Unmarshal(object["code"], &code) != nil || string(object["code"]) == "null" {
		return false
	}
	_, ok := text(object["message"])
	return ok
}
func optionalObject(object map[string]json.RawMessage, key string, l Limits) bool {
	if raw, present := object[key]; present {
		_, err := decodeObject(raw, l.ResultBytes, l.Depth)
		return err == nil
	}
	return true
}
func contentBlock(raw []byte, l Limits) bool {
	object, err := decodeObject(raw, l.ResultBytes, l.Depth)
	if err != nil || !optionalObject(object, "_meta", l) || !optionalObject(object, "annotations", l) {
		return false
	}
	kind, ok := text(object["type"])
	if !ok {
		return false
	}
	required := []string{}
	switch kind {
	case "text":
		required = []string{"text"}
	case "image", "audio":
		required = []string{"data", "mimeType"}
	case "resource_link":
		required = []string{"uri", "name"}
	case "resource":
		resource, err := decodeObject(object["resource"], l.ResultBytes, l.Depth)
		if err != nil {
			return false
		}
		if _, ok := text(resource["uri"]); !ok {
			return false
		}
		_, hasText := resource["text"]
		_, hasBlob := resource["blob"]
		if hasText == hasBlob {
			return false
		}
		key := "text"
		if hasBlob {
			key = "blob"
		}
		if _, ok := text(resource[key]); !ok {
			return false
		}
		return optionalObject(resource, "_meta", l)
	default:
		return false
	}
	for _, key := range required {
		if _, ok := text(object[key]); !ok {
			return false
		}
	}
	return true
}
func validateMethodResult(kind nativetool.RequestKind, result rpc, l Limits) error {
	if failure, present := result.object["error"]; present {
		if !rpcError(failure, l) {
			return errProtocol
		}
		return nil
	}
	object, err := decodeObject(result.object["result"], l.ResultBytes, l.Depth)
	if err != nil {
		return err
	}
	switch kind {
	case nativetool.RequestInitialize:
		info, err := decodeObject(object["serverInfo"], l.ResultBytes, l.Depth)
		if err != nil || !implementation(info) {
			return errProtocol
		}
	case nativetool.RequestToolsCall:
		_, content := object["content"]
		_, structured := object["structuredContent"]
		_, isError := object["isError"]
		_, meta := object["_meta"]
		if !content && !structured && !isError && !meta {
			return errProtocol
		}
		if content {
			blocks, valid := contentElements(object["content"])
			if !valid {
				return errProtocol
			}
			for _, block := range blocks {
				if !contentBlock(block, l) {
					return errProtocol
				}
			}
		}
		if isError {
			var flag bool
			if json.Unmarshal(object["isError"], &flag) != nil || string(object["isError"]) == "null" {
				return errProtocol
			}
		}
		if !optionalObject(object, "structuredContent", l) || !optionalObject(object, "_meta", l) {
			return errProtocol
		}
	case nativetool.RequestPing:
		if len(object) != 0 {
			return errProtocol
		}
	}
	return nil
}
