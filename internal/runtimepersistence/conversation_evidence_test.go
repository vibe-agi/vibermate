package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

func TestConversationEvidenceReadsOnlyRetainedIdentityMetadata(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { shutdownTestStore(t, store) })
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	repository := store.ExchangeContentRepository()
	completed := contentRecordFixture(t, "identity-only", now)
	pending := completed.Clone()
	pending.Response = nil
	if err := repository.Put(ctx, pending); err != nil {
		t.Fatal(err)
	}
	check := func(want exchangecontent.Record) {
		t.Helper()
		got, err := repository.GetConversationEvidence(ctx, want.ExchangeID, now)
		if err != nil || !reflect.DeepEqual(got, want.ConversationEvidence()) {
			t.Fatalf("conversation evidence = %+v, %v; want %+v", got, err, want.ConversationEvidence())
		}
	}
	check(pending)
	if err := repository.Put(ctx, completed); err != nil {
		t.Fatal(err)
	}
	check(completed)
	// Corrupt only synthetic transcript blocks. Metadata reads must not inflate
	// or validate these blocks, but opening content must still reject them.
	if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET payload = ?`, []byte("broken")); err != nil {
		t.Fatal(err)
	}
	check(completed)
	if _, err := repository.Get(ctx, completed.ExchangeID, now); err == nil {
		t.Fatal("full evidence silently accepted a broken transcript")
	}
	if _, err := repository.GetConversationEvidence(ctx, completed.ExchangeID, completed.ExpiresAt); !errors.Is(err, exchangecontent.ErrNotFound) {
		t.Fatalf("expired metadata: %v", err)
	}
	if _, err := store.database.Exec(`UPDATE runtime_exchange_contents SET manifest_json = ? WHERE exchange_id = ?`, []byte("{}"), completed.ExchangeID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetConversationEvidence(ctx, completed.ExchangeID, now); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("invalid manifest: %v", err)
	}
	if _, err := store.database.Exec(`DELETE FROM runtime_exchange_contents WHERE exchange_id = ?`, completed.ExchangeID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetConversationEvidence(ctx, completed.ExchangeID, now); !errors.Is(err, exchangecontent.ErrNotFound) {
		t.Fatalf("deleted metadata: %v", err)
	}
}

func BenchmarkConversationEvidenceRead(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("messages=%d", size), func(b *testing.B) {
			store := openTestStore(b, filepath.Join(b.TempDir(), "runtime.db"))
			b.Cleanup(func() { shutdownTestStore(b, store) })
			now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
			record := contentRecordFixture(b, "benchmark-identity", now)
			message := record.Request.Messages[0]
			record.Request.Messages = make([]exchangecontent.Message, size)
			for i := range size {
				record.Request.Messages[i] = exchangecontent.Message{
					Role: message.Role, Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: fmt.Sprintf("%d %s", i, strings.Repeat("context ", 128))}},
				}
			}
			repository := store.ExchangeContentRepository()
			ctx := context.Background()
			if err := repository.Put(ctx, record); err != nil {
				b.Fatal(err)
			}
			b.Run("full-transcript", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := repository.Get(ctx, record.ExchangeID, now); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("identity-manifest", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := repository.GetConversationEvidence(ctx, record.ExchangeID, now); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
