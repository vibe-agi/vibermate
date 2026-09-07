package rawevidence_test

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestDecodeBodyForDisplayPreservesEvidence(t *testing.T) {
	for _, encoding := range []string{"zstd", "gzip"} {
		for _, body := range [][]byte{nil, []byte("{\"message\":\"\u4f60\u597d\uff0cCodex\",\"stream\":true}"), {0, 255, 1}} {
			t.Run(encoding+"/"+string(body), func(t *testing.T) {
				wire := compressedBodyViewFixture(t, encoding, body)
				original := bytes.Clone(wire)
				revealed := bodyViewFixture("  "+encoding+"  ", wire)
				view := rawevidence.DecodeBodyForDisplay(revealed)
				if view == nil || view.State != "decoded" || !bytes.Equal(view.Body, body) {
					t.Fatalf("view = %+v", view)
				}
				clear(view.Body)
				if !bytes.Equal(wire, original) || revealed.Metadata.ContentEncoding != "  "+encoding+"  " {
					t.Fatal("display decoding modified original evidence")
				}
			})
		}
	}
}

func TestDecodeBodyForDisplayRejectsIncompleteCorruptAndOversizedContent(t *testing.T) {
	for _, encoding := range []string{"zstd", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			wire := compressedBodyViewFixture(t, encoding, []byte(`{"private":"content"}`))
			for _, test := range []struct {
				name, state string
				body        []byte
				truncated   bool
			}{
				{"corrupt", "invalid_compression", []byte("private-invalid-body"), false},
				{"partial frame", "invalid_compression", wire[:len(wire)/2], false},
				{"recorded prefix", "incomplete", wire, true},
				{"expansion limit", "size_limit", compressedBodyViewFixture(t, encoding, bytes.Repeat([]byte("x"), (4<<20)+1)), false},
			} {
				t.Run(test.name, func(t *testing.T) {
					revealed := bodyViewFixture(encoding, test.body)
					if test.truncated {
						revealed.Metadata.PayloadState = rawevidence.PayloadTruncated
					}
					view := rawevidence.DecodeBodyForDisplay(revealed)
					if view == nil || view.State != test.state || len(view.Body) != 0 {
						t.Fatalf("state = %v, want %s with no speculative body", view, test.state)
					}
				})
			}
		})
	}
	for _, encoding := range []string{"", "identity", " IDENTITY "} {
		if rawevidence.DecodeBodyForDisplay(bodyViewFixture(encoding, []byte("plain"))) != nil {
			t.Fatal("identity evidence acquired a redundant decoded view")
		}
	}
	for _, encoding := range []string{"br", "gzip, zstd"} {
		view := rawevidence.DecodeBodyForDisplay(bodyViewFixture(encoding, nil))
		if view == nil || view.State != "unsupported_encoding" || len(view.Body) != 0 {
			t.Fatalf("unsupported encoding view = %+v", view)
		}
	}
}

func bodyViewFixture(encoding string, body []byte) rawevidence.RevealedEnvelope {
	return rawevidence.RevealedEnvelope{
		Metadata: rawevidence.EnvelopeMetadata{ContentEncoding: encoding, PayloadState: rawevidence.PayloadCaptured},
		Payload:  rawevidence.Payload{Body: body},
	}
}

func compressedBodyViewFixture(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var writer interface {
		Write([]byte) (int, error)
		Close() error
	}
	if encoding == "gzip" {
		writer = gzip.NewWriter(&buffer)
	} else {
		var err error
		writer, err = zstd.NewWriter(&buffer, zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
