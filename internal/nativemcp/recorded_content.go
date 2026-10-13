package nativemcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/jpeg"
	_ "image/png"
)

// RecordedToolContent is a neutral projection of validated retained bytes.
type RecordedToolContent struct{ Blocks []RecordedBlock }
type RecordedBlock struct {
	Kind, Text, MIMEType string
	ImageBytes           []byte
}

func DecodeRecordedToolContent(part string, body []byte) (RecordedToolContent, error) {
	l := DefaultLimits()
	object, err := decodeObject(body, l.ResultBytes, l.Depth)
	if err != nil {
		return RecordedToolContent{}, errProtocol
	}
	if part == "tool_arguments" {
		return RecordedToolContent{Blocks: []RecordedBlock{{Kind: "json", Text: string(bytes.Clone(body))}}}, nil
	}
	if part != "tool_result" || !allowed(object, "content", "structuredContent", "isError") || !recordedPublicResult(object, l) {
		return RecordedToolContent{}, errProtocol
	}
	if err = validateMethodResult(nativetool.RequestToolsCall, rpc{object: map[string]json.RawMessage{"result": body}}, l); err != nil {
		return RecordedToolContent{}, err
	}
	result := RecordedToolContent{Blocks: []RecordedBlock{}}
	elements, _ := contentElements(object["content"])
	for _, raw := range elements {
		block, _ := decodeObject(raw, l.ResultBytes, l.Depth)
		kind, _ := text(block["type"])
		switch kind {
		case "text":
			value, _ := text(block["text"])
			if value != "" {
				result.Blocks = append(result.Blocks, RecordedBlock{Kind: "text", Text: value})
			}
		case "image":
			mime, _ := text(block["mimeType"])
			encoded, _ := text(block["data"])
			if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" || len(encoded) > base64.StdEncoding.EncodedLen(4<<20) {
				return RecordedToolContent{}, errProtocol
			}
			data, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || len(data) == 0 || len(data) > 4<<20 || base64.StdEncoding.EncodeToString(data) != encoded {
				clear(data)
				return RecordedToolContent{}, errProtocol
			}
			config, format, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || "image/"+format != mime || config.Width <= 0 || config.Height <= 0 || config.Width > (16<<20)/config.Height {
				clear(data)
				return RecordedToolContent{}, errProtocol
			}
			// DecodeConfig can return only the WebP canvas. Full decode checks the
			// actual VP8/VP8L dimensions before allocating pixels and validates bytes.
			decoded, actual, err := image.Decode(bytes.NewReader(data))
			if err != nil || actual != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
				clear(data)
				return RecordedToolContent{}, errProtocol
			}
			result.Blocks = append(result.Blocks, RecordedBlock{Kind: "image", MIMEType: mime, ImageBytes: data})
		default:
			// Resource URI, audio, metadata and file payloads are never fetched or
			// interpreted as a preview. Do not disclose URI query/proof fields.
			result.Blocks = append(result.Blocks, RecordedBlock{Kind: "text", Text: "Resource unavailable"})
		}
	}
	if raw, ok := object["structuredContent"]; ok {
		result.Blocks = append(result.Blocks, RecordedBlock{Kind: "json", Text: string(bytes.Clone(raw))})
	}
	return result, nil
}

func recordedPublicResult(object map[string]json.RawMessage, l Limits) bool {
	if _, private := object["_meta"]; private {
		return false
	}
	elements, _ := contentElements(object["content"])
	for _, raw := range elements {
		block, err := decodeObject(raw, l.ResultBytes, l.Depth)
		if err != nil {
			return false
		}
		if _, private := block["_meta"]; private {
			return false
		}
		if kind, _ := text(block["type"]); kind == "resource" {
			resource, err := decodeObject(block["resource"], l.ResultBytes, l.Depth)
			if err != nil {
				return false
			}
			if _, private := resource["_meta"]; private {
				return false
			}
		}
	}
	return true
}
