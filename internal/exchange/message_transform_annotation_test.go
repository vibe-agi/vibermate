package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

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
