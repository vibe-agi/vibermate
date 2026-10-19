package exchangecontent

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"strings"
	"testing"
	"time"
)

func TestOwnedContentSurvivesProducerMutation(t *testing.T) {
	request, response := evidenceFixture(t)
	request.ProtocolEvidence = []protocolcore.ProtocolEvidenceValue{{Name: "openai_responses.session_id", Value: "original-session"}}
	request.Messages[0].Blocks = []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "Authorization: Bearer secret-original"}}
	request.Messages[0].Agent = &protocolcore.AgentMessageContext{Author: "original", Recipient: "recipient"}
	response.StopReason = protocolcore.StopReasonEndTurn
	response.Blocks = []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "answer-original"}}
	budget, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
	reservation, _ := budget.TryReserve(recording.Content, 0)
	defer reservation.Release()
	owned, err := OwnWithin(context.Background(), OwnInput{ExchangeID: "owned", Frozen: frozenFixture(), Policy: environment.DefaultContentRecordingPolicy(), RecordedAt: time.Now(), Request: request, Response: &response}, sourceFixtureLimits(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	request.ProtocolEvidence[0].Value = "changed"
	request.Messages[0].Blocks[0].Text = "changed"
	request.Messages[0].Agent.Author = "changed"
	response.Blocks[0].Text = "changed"
	if owned.Source().Metadata().Request.ProtocolEvidence[0].Value != "original-session" {
		t.Fatal("borrowed protocol evidence")
	}
	var text string
	err = owned.Source().Walk(context.Background(), func(part Part, _ int, m MessageSource) error {
		if part == RequestPart && m.Header().Agent.Author != "original" {
			t.Fatal("borrowed producer agent")
		}
		return m.WalkBlocks(context.Background(), func(b Block) error { text += b.Text; return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "secret-original") || strings.Contains(text, "changed") || !strings.Contains(text, "answer-original") || !strings.Contains(text, "[redacted]") {
		t.Fatalf("snapshot lost sanitization or ownership: %q", text)
	}
	if budget.Snapshot().ReservedBytes == 0 {
		t.Fatal("owned allocation was not charged")
	}
}

func TestOwnedContentRedactsCompleteAuthorizationValues(t *testing.T) {
	for _, text := range []string{"Authorization: Bearer abcdefgh12345678", "authorization = bearer\tabcdefgh12345678", "Proxy-Authorization: Basic abcdefgh12345678", "AUTHORIZATION:\tBaSiC abcdefgh12345678"} {
		request, _ := evidenceFixture(t)
		request.Messages[0].Blocks = []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: text}}
		budget, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
		reservation, _ := budget.TryReserve(recording.Content, 0)
		owned, err := OwnWithin(context.Background(), OwnInput{ExchangeID: "redaction", Frozen: frozenFixture(), Policy: environment.DefaultContentRecordingPolicy(), RecordedAt: time.Now(), Request: request}, sourceFixtureLimits(), reservation)
		if err != nil {
			t.Fatal(err)
		}
		err = owned.Source().Walk(context.Background(), func(_ Part, _ int, m MessageSource) error {
			return m.WalkBlocks(context.Background(), func(b Block) error {
				if strings.Contains(b.Text, "abcdefgh12345678") {
					t.Errorf("credential survived: %q", b.Text)
				}
				return nil
			})
		})
		reservation.Release()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMetadataOnlyOwnsShapeWithoutFullContentCapacity(t *testing.T) {
	request, _ := evidenceFixture(t)
	request.Messages[0].Blocks = []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: strings.Repeat("private", 1<<20)}}
	limits, _ := DefaultSourceLimits()
	budget, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 1 << 20})
	reservation, _ := budget.TryReserve(recording.Content, 0)
	defer reservation.Release()
	owned, err := OwnWithin(context.Background(), OwnInput{ExchangeID: "metadata", Frozen: frozenFixture(), Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 1}, RecordedAt: time.Now(), Request: request}, limits, reservation)
	if err != nil {
		t.Fatal(err)
	}
	err = owned.Source().Walk(context.Background(), func(_ Part, _ int, m MessageSource) error {
		return m.WalkBlocks(context.Background(), func(b Block) error {
			if b.Text != "" || len(b.Arguments) != 0 {
				t.Fatal("metadata retained content")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if budget.Snapshot().ReservedBytes >= 1<<20 {
		t.Fatal("metadata charged full body")
	}
}

func TestOwnedContentRedactsWholeRecognizedHeaderLine(t *testing.T) {
	for _, header := range []string{"Authorization: Negotiate secret-original", "Proxy-Authorization: Digest username=secret-user, nonce=secret-nonce, response=secret-response", "Cookie: session=secret-session; token=secret-token", "Set-Cookie: session=secret-session; token=secret-token"} {
		request, _ := evidenceFixture(t)
		request.Messages[0].Blocks = []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: header + "\npublic following line"}}
		b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
		reservation, _ := b.TryReserve(recording.Content, 0)
		owned, err := OwnWithin(context.Background(), OwnInput{ExchangeID: "header-line", Frozen: frozenFixture(), Policy: environment.DefaultContentRecordingPolicy(), RecordedAt: time.Now(), Request: request}, sourceFixtureLimits(), reservation)
		if err != nil {
			t.Fatal(err)
		}
		err = owned.Source().Walk(context.Background(), func(_ Part, _ int, m MessageSource) error {
			return m.WalkBlocks(context.Background(), func(block Block) error {
				if strings.Contains(block.Text, "secret-") || !strings.HasSuffix(block.Text, "\npublic following line") || !strings.Contains(block.Text, "[redacted]") {
					t.Errorf("header boundary leaked or removed next line: %q", block.Text)
				}
				return nil
			})
		})
		reservation.Release()
		if err != nil {
			t.Fatal(err)
		}
	}
}
