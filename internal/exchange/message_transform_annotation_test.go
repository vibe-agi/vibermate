package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

type responseCloseFunc func() error

func (close responseCloseFunc) Close() error { return close() }

type responseReadFunc func([]byte) (int, error)

func (read responseReadFunc) Read(buffer []byte) (int, error) { return read(buffer) }

func TestLogicalResponseCloseInterruptsReadBeforeClosingDecoder(t *testing.T) {
	readStarted, transportClosed := make(chan struct{}), make(chan struct{})
	readExited, closed := make(chan struct{}), make(chan error, 1)
	stream := &logicalTransformStream{
		Reader: responseReadFunc(func([]byte) (int, error) {
			close(readStarted)
			<-transportClosed
			defer close(readExited)
			return 0, io.ErrClosedPipe
		}),
		source: struct {
			io.Reader
			io.Closer
		}{Closer: responseCloseFunc(func() error { close(transportClosed); return nil })},
		decoder: responseCloseFunc(func() error {
			select {
			case <-readExited:
				return nil
			default:
				return errors.New("decoder closed while Read was still active")
			}
		}),
	}
	go func() { _, _ = stream.Read(make([]byte, 1)) }()
	<-readStarted
	go func() { closed <- stream.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not interrupt the blocked transport read")
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Read after Close = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalResponseClosesTransportBeforeDecoder(t *testing.T) {
	sourceClosed, decoderClosed := false, false
	stream := &logicalTransformStream{
		source: struct {
			io.Reader
			io.Closer
		}{
			Reader: bytes.NewReader(nil),
			Closer: responseCloseFunc(func() error { sourceClosed = true; return nil }),
		},
		decoder: responseCloseFunc(func() error {
			if !sourceClosed {
				t.Error("decoder could still be blocked reading the transport")
			}
			decoderClosed = true
			return nil
		}),
	}
	if err := stream.Close(); err != nil || !sourceClosed || !decoderClosed {
		t.Fatalf("incomplete close: %v", err)
	}
}

func TestRequestPreparationCleansAnnotationsEvenWithoutRequestJavaScript(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip", "zstd"} {
		for _, response := range []string{"", `response.body = response.body;`} {
			t.Run(encoding+"/"+response, func(t *testing.T) {
				checkRequestAnnotationPreparation(t, encoding, response)
			})
		}
	}
}

func checkRequestAnnotationPreparation(t *testing.T, encoding, responseScript string) {
	t.Helper()
	t.Parallel()

	signer, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(signer.Destroy)
	annotation, err := signer.Issue("turn-time", "2026-08-27T06:05:04Z")
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := messagetransform.CompilePipeline(
		[]messagetransform.Policy{{ResponseJavaScript: responseScript}},
		messagetransform.DefaultLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := pipeline.NewTurnWithOptions(messagetransform.TurnOptions{Annotations: signer})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"content": "answer " + annotation})
	if encoding != "identity" {
		body = compressedRequestFixture(t, encoding, body)
	}
	headers := http.Header{
		"Content-Encoding": {encoding},
		"Content-Type":     {"application/json"},
		"Content-Length":   {"999"},
		"Etag":             {"old-representation"},
	}

	cleanedHeaders, cleanedBody, _, err := applyRequestMessageTransform(
		context.Background(), turn, http.MethodPost, "/v1/messages", headers, body,
	)
	if err != nil {
		t.Fatalf("applyRequestMessageTransform() error = %v", err)
	}
	if !json.Valid(cleanedBody) || bytes.Contains(cleanedBody, []byte("vibermate:annotation")) ||
		bytes.Contains(cleanedBody, []byte("2026-08-27")) {
		t.Fatalf("cleaned Body retained annotation: %s", cleanedBody)
	}
	if cleanedHeaders.Get("Content-Length") != "" || cleanedHeaders.Get("ETag") != "" || cleanedHeaders.Get("Content-Encoding") != "" {
		t.Fatalf("stale representation Headers survived: %#v", cleanedHeaders)
	}

	ordinary := []byte(`{ "content" : "ordinary" }`)
	if encoding != "identity" {
		ordinary = compressedRequestFixture(t, encoding, ordinary)
	}
	unchangedHeaders, unchangedBody, _, err := applyRequestMessageTransform(
		context.Background(), turn, http.MethodPost, "/v1/messages", headers, ordinary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchangedBody, ordinary) ||
		unchangedHeaders.Get("Content-Length") != "999" ||
		unchangedHeaders.Get("Content-Encoding") != encoding ||
		unchangedHeaders.Get("ETag") != "old-representation" {
		t.Fatalf("ordinary request changed: Headers=%#v Body=%q", unchangedHeaders, unchangedBody)
	}
}
