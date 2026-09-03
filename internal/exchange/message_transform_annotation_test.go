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
		[]messagetransform.Policy{{ResponseJavaScript: `response.body = response.body;`}},
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
	headers := http.Header{
		"Content-Type":   {"application/json"},
		"Content-Length": {"999"},
		"Etag":           {"old-representation"},
	}

	cleanedHeaders, cleanedBody, _, err := applyRequestMessageTransform(
		context.Background(), turn, http.MethodPost, "/v1/messages", headers, body,
	)
	if err != nil {
		t.Fatalf("applyRequestMessageTransform() error = %v", err)
	}
	if bytes.Contains(cleanedBody, []byte("vibermate:annotation")) ||
		bytes.Contains(cleanedBody, []byte("2026-08-27")) {
		t.Fatalf("cleaned Body retained annotation: %s", cleanedBody)
	}
	if cleanedHeaders.Get("Content-Length") != "" || cleanedHeaders.Get("ETag") != "" {
		t.Fatalf("stale representation Headers survived: %#v", cleanedHeaders)
	}

	ordinary := []byte(`{ "content" : "ordinary" }`)
	unchangedHeaders, unchangedBody, _, err := applyRequestMessageTransform(
		context.Background(), turn, http.MethodPost, "/v1/messages", headers, ordinary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchangedBody, ordinary) ||
		unchangedHeaders.Get("Content-Length") != "999" ||
		unchangedHeaders.Get("ETag") != "old-representation" {
		t.Fatalf("ordinary request changed: Headers=%#v Body=%q", unchangedHeaders, unchangedBody)
	}
}
