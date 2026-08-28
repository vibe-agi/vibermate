package clientannotation

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/hostsecret"
)

func TestSignerRemovesOnlyAuthenticStructuredAnnotationsFromJSONStrings(t *testing.T) {
	t.Parallel()

	signer, err := NewSigner(bytes.Repeat([]byte{0x42}, signingKeyBytes))
	if err != nil {
		t.Fatalf("NewSigner() error = %v", err)
	}
	t.Cleanup(signer.Destroy)
	annotation, err := signer.Issue("turn-time", "2026-08-27 14:05 Asia/Singapore")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	tampered := strings.Replace(annotation, "14:05", "14:06", 1)
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{"content": "before " + annotation + " after"},
		},
		"untrusted": tampered,
	})
	if err != nil {
		t.Fatal(err)
	}

	cleaned, changed, err := signer.StripJSON(body)
	if err != nil {
		t.Fatalf("StripJSON() error = %v", err)
	}
	if !changed {
		t.Fatal("StripJSON() changed = false, want true")
	}
	var result map[string]any
	if err := json.Unmarshal(cleaned, &result); err != nil {
		t.Fatalf("decode cleaned JSON: %v", err)
	}
	input := result["input"].([]any)[0].(map[string]any)["content"]
	if input != "before  after" {
		t.Fatalf("cleaned content = %q", input)
	}
	if result["untrusted"] != tampered {
		t.Fatal("StripJSON() removed an annotation with an invalid signature")
	}
}

func TestSignerLeavesBodiesWithoutAuthenticAnnotationsByteExact(t *testing.T) {
	t.Parallel()

	signer, err := NewSigner(bytes.Repeat([]byte{0x24}, signingKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(signer.Destroy)
	for _, body := range [][]byte{
		[]byte(`{ "content" : "ordinary user text" }`),
		[]byte(`{"content":"<!--vibermate:annotation:v1:turn-time:invalid-->text<!--/vibermate:annotation-->"}`),
		[]byte(`not json`),
	} {
		cleaned, changed, stripErr := signer.StripJSON(body)
		if stripErr != nil {
			t.Fatalf("StripJSON(%q) error = %v", body, stripErr)
		}
		if changed || !bytes.Equal(cleaned, body) {
			t.Fatalf("StripJSON(%q) = (%q, %t), want byte-exact unchanged", body, cleaned, changed)
		}
	}
}

func TestOpenSignerPersistsOneHostSecretAcrossRuntimeRestarts(t *testing.T) {
	t.Parallel()

	factory, err := hostsecret.NewServerFileFactory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(context.Background(), store, bytes.NewReader(bytes.Repeat([]byte{0x77}, signingKeyBytes)))
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	annotation, err := first.Issue("turn-time", "persistent")
	if err != nil {
		t.Fatal(err)
	}
	first.Destroy()

	reopenedStore, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), reopenedStore, strings.NewReader(""))
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}
	t.Cleanup(second.Destroy)
	body, _ := json.Marshal(map[string]string{"content": annotation})
	cleaned, changed, err := second.StripJSON(body)
	if err != nil || !changed || strings.Contains(string(cleaned), "persistent") {
		t.Fatalf("reopened StripJSON() = (%q, %t, %v)", cleaned, changed, err)
	}
}
