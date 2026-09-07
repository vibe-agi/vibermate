package rawevidence

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// DecodedBodyView is a transient reading aid, never a replacement for the
// retained bytes, their digest, headers, byte counts, or frame offsets.
type DecodedBodyView struct {
	State string `json:"state"`
	Body  []byte `json:"bodyBase64"`
}

const maximumDecodedBodyViewBytes = 4 << 20

// DecodeBodyForDisplay is called only after an authorized, audited reveal.
// It leaves evidence untouched and bounds both output and decoder memory.
// Incomplete compressed evidence is kept available as original bytes without
// presenting a potentially corrupt prefix as a complete decoded message.
func DecodeBodyForDisplay(revealed RevealedEnvelope) *DecodedBodyView {
	encoding := strings.ToLower(strings.TrimSpace(revealed.Metadata.ContentEncoding))
	if encoding == "" || encoding == "identity" {
		return nil
	}
	view := &DecodedBodyView{State: "invalid_compression", Body: []byte{}}
	if revealed.Metadata.PayloadState != PayloadCaptured {
		view.State = "incomplete"
		return view
	}
	var reader io.Reader
	switch encoding {
	case "gzip":
		decoder, err := gzip.NewReader(bytes.NewReader(revealed.Payload.Body))
		if err != nil {
			return view
		}
		defer decoder.Close()
		reader = decoder
	case "zstd":
		decoder, err := zstd.NewReader(bytes.NewReader(revealed.Payload.Body),
			zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(16<<20))
		if err != nil {
			return view
		}
		defer decoder.Close()
		reader = decoder
	default:
		view.State = "unsupported_encoding"
		return view
	}
	body, err := io.ReadAll(io.LimitReader(reader, maximumDecodedBodyViewBytes+1))
	if len(body) > maximumDecodedBodyViewBytes {
		clear(body)
		view.State = "size_limit"
		return view
	}
	if err != nil {
		clear(body)
		return view
	}
	view.State, view.Body = "decoded", body
	return view
}
