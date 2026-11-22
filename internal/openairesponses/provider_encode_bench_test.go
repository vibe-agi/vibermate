package openairesponses_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// BenchmarkEncodeClaudeCorpus measures the provider encode of every Claude
// body of the S2 corpus (design 14.1): responses/<body> is this side's
// EncodeRequestFor on the translated decode, chat/<body> the existing A->C
// encode on the strict decode of the same body. The gate is at most 1.2x the
// A->C encode with allocations proportional to items plus tools. A->C
// refuses three bodies (claude-tools.008 and claude-interactive-tools.006 at
// decode, claude-tools.008-text-results at encode), so they have no chat
// baseline.
func BenchmarkEncodeClaudeCorpus(b *testing.B) {
	side := newProviderSide(b, nil)
	chat, err := anthropicchat.NewProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		b.Fatal(err)
	}
	providerContext := protocolpath.ProviderContext{ReasoningBinding: testBinding}
	for _, file := range claudeCorpusFiles(b) {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		body := readFile(b, file)
		translated := decodeClaude(b, body)
		b.Run("responses/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := side.EncodeRequestFor(providerContext, translated); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("chat/"+name, func(b *testing.B) {
			strict, _, err := chat.Client().DecodeRequest(body)
			if err != nil {
				b.Skipf("A->C refuses this body at decode: %v", err)
			}
			if strict, err = strict.WithEffectiveModel(mappedModel); err != nil {
				b.Fatal(err)
			}
			if _, _, err := chat.EncodeProviderRequest(strict, body, nil); err != nil {
				b.Skipf("A->C refuses this body at encode: %v", err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := chat.EncodeProviderRequest(strict, body, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkReadmitClaudeCorpus measures re-admission of each encoded body,
// a full compatible decode (design 14.1: reported, not gated).
func BenchmarkReadmitClaudeCorpus(b *testing.B) {
	side := newProviderSide(b, nil)
	for _, file := range claudeCorpusFiles(b) {
		encoded, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{}, decodeClaude(b, readFile(b, file)))
		if err != nil {
			b.Fatal(err)
		}
		body := encoded.Body()
		b.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if err := side.ValidateTransformedRequest(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
