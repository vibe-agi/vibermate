package runtimepersistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestReadingViewPreservesLatestUserGroupAcrossCheckpoints(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	messages := []transcriptMessage{
		{role: protocolcore.RoleUser, text: "OLD_INPUT"},
		{role: protocolcore.RoleAssistant, text: "OLD_REPLY"},
		{role: protocolcore.RoleUser, text: "IMAGE_ONE"},
		{role: protocolcore.RoleUser, text: "IMAGE_TWO"},
		{role: protocolcore.RoleUser, text: "QUESTION"},
		{role: protocolcore.RoleAssistant, text: "background activity"},
	}
	repository := store.ExchangeContentRepository()
	var firstAnchor string
	for i := range 3 {
		if i == 1 {
			messages = append(messages, transcriptMessage{role: protocolcore.RoleAssistant, text: "more background activity"})
		}
		if i == 2 {
			messages = append(messages, transcriptMessage{role: protocolcore.RoleUser, text: "QUESTION"})
		}
		id := []string{"reading-first", "reading-replay", "reading-repeat-question"}[i]
		record := transcriptContentRecordFixture(t, id, at, exchangecontent.ParentRef{CaptureRunID: "run-reading"}, messages, "ANSWER")
		if err := repository.Put(ctx, record); err != nil {
			t.Fatal(err)
		}
		view, err := repository.GetPagedProjection(ctx, id, at, exchangecontent.RequestView("reading"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(view.Page)
		var page struct {
			InputAnchor string `json:"inputAnchor"`
		}
		if json.Unmarshal(encoded, &page) != nil || len(page.InputAnchor) != 64 {
			t.Fatal("reading input has no stable identity")
		}
		if i == 0 {
			firstAnchor = page.InputAnchor
			if len(view.Request.Messages) != 3 || view.Request.Messages[0].Blocks[0].Text != "IMAGE_ONE" || view.Request.Messages[2].Blocks[0].Text != "QUESTION" {
				t.Fatal("latest input or attachments disappeared")
			}
		}
		if i == 1 && page.InputAnchor != firstAnchor {
			t.Fatal("background activity duplicated user input")
		}
		if i == 2 && (page.InputAnchor == firstAnchor || len(view.Request.Messages) != 1) {
			t.Fatal("a repeated question was incorrectly deduplicated")
		}
		if view.Presentation.Mode != exchangecontent.RequestPresentationCheckpoint || view.Presentation.InheritedMessageCount != 0 {
			t.Fatal("reading input was falsely declared an exact incremental transcript")
		}
	}
}

// Each mutation breaks either the frozen ancestry or the canonical role proof.
// The paged full view is an independent control, not a source of expectations.
func TestReadingViewRejectsCorruptFrozenEvidence(t *testing.T) {
	for _, mutation := range []struct {
		name, query string
	}{
		{"replaced_message", `UPDATE runtime_exchange_content_transcripts SET message_digest=(SELECT message_digest FROM runtime_exchange_content_transcripts WHERE digest=?) WHERE digest=?`},
		{"wrong_parent", `UPDATE runtime_exchange_content_transcripts SET parent_digest=? WHERE digest=?`},
		{"wrong_depth", `UPDATE runtime_exchange_content_transcripts SET depth=3 WHERE digest=?2`},
		{"ancestor_depth", `UPDATE runtime_exchange_content_transcripts SET depth=2 WHERE digest=(SELECT parent_digest FROM runtime_exchange_content_transcripts WHERE digest=?2)`},
		{"missing_node", `DELETE FROM runtime_exchange_content_transcripts WHERE digest=?2`},
		{"missing_message", `DELETE FROM runtime_exchange_content_messages WHERE digest=(SELECT message_digest FROM runtime_exchange_content_transcripts WHERE digest=?2)`},
		{"hidden_user_role", `UPDATE runtime_exchange_content_messages SET role='assistant' WHERE digest=(SELECT message_digest FROM runtime_exchange_content_transcripts WHERE digest=(SELECT parent_digest FROM runtime_exchange_content_transcripts WHERE digest=?2))`},
		{"skipped_assistant_role", `UPDATE runtime_exchange_content_messages SET role='tool' WHERE digest=(SELECT message_digest FROM runtime_exchange_content_transcripts WHERE digest=?2)`},
		{"cycle", `UPDATE runtime_exchange_content_transcripts SET parent_digest=digest WHERE digest=?2`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
			ctx := context.Background()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			repo := store.ExchangeContentRepository()
			for _, id := range []string{"source-a", "source-b"} {
				record := transcriptContentRecordFixture(t, id, at, exchangecontent.ParentRef{CaptureRunID: "run-" + id}, []transcriptMessage{
					{role: protocolcore.RoleUser, text: id + " image one"},
					{role: protocolcore.RoleUser, text: id + " image two"},
					{role: protocolcore.RoleUser, text: id + " question"},
					{role: protocolcore.RoleAssistant, text: id + " background"},
				}, "ANSWER")
				if err := repo.Put(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			a, err := loadStoredContentReference(ctx, store.database, "source-a", at)
			if err != nil {
				t.Fatal(err)
			}
			b, err := loadStoredContentReference(ctx, store.database, "source-b", at)
			if err != nil {
				t.Fatal(err)
			}
			// Allow missing-row corruption in this disposable SQLite fixture.
			if _, err := store.database.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.database.ExecContext(ctx, mutation.query, b.requestRoot, a.requestRoot); err != nil {
				t.Fatal(err)
			}
			if _, err := store.database.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
			for _, view := range []exchangecontent.RequestView{exchangecontent.RequestViewFull, exchangecontent.RequestViewReading} {
				bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				start := time.Now()
				_, err := repo.GetPagedProjection(bounded, "source-a", at, view)
				cancel()
				t.Logf("view=%s rejection=%v elapsed=%s", view, err, time.Since(start))
				if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
					t.Errorf("%s accepted corrupt frozen evidence or waited for deadline: %v", view, err)
				}
			}
		})
	}
}

func TestReadingViewIntegrityDefersUnprovableRole(t *testing.T) {
	for _, role := range []protocolcore.Role{protocolcore.RoleUser, protocolcore.RoleAssistant} {
		t.Run(string(role), func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
			ctx := context.Background()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			record := transcriptContentRecordFixture(t, "unproven-role", at, exchangecontent.ParentRef{}, []transcriptMessage{
				{role: protocolcore.RoleUser, text: "older question must not be selected"},
				{role: role, text: strings.Repeat("x", exchangecontent.PageContentBytes)},
			}, "ANSWER")
			repo := store.ExchangeContentRepository()
			if err := repo.Put(ctx, record); err != nil {
				t.Fatal(err)
			}
			// Proof cannot fit the preview; corrupting its payload makes an eager
			// verification read observable without a test-only production hook.
			digest, _, err := encodeStoredBlock(record.Request.Messages[1].Blocks[0])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=zeroblob(plain_bytes) WHERE digest=?`, digest); err != nil {
				t.Fatal(err)
			}
			view, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewReading)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Request.Messages) != 1 || view.Request.Messages[0].Role != "unknown" || view.Request.Messages[0].Blocks[0].Deferred == nil || view.Page.InputAnchor != "" {
				t.Fatalf("unproven role manufactured a user group: %+v page=%+v", view.Request.Messages, view.Page)
			}
			cursor := view.Request.Messages[0].Blocks[0].Deferred.Cursor
			if _, err := repo.GetContentPage(ctx, record.ExchangeID, at, cursor); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
				t.Fatalf("explicit deferred load did not verify canonical evidence: %v", err)
			}
		})
	}
}

func TestReadingViewIntegrityBoundsRoleProofCount(t *testing.T) {
	for _, assistants := range []int{23, 24} {
		t.Run(fmt.Sprint(assistants), func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
			ctx := context.Background()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			messages := []transcriptMessage{{role: protocolcore.RoleUser, text: "question"}}
			for i := range assistants {
				messages = append(messages, transcriptMessage{role: protocolcore.RoleAssistant, text: fmt.Sprintf("background-%d", i)})
			}
			record := transcriptContentRecordFixture(t, "proof-count", at, exchangecontent.ParentRef{}, messages, "")
			repo := store.ExchangeContentRepository()
			if err := repo.Put(ctx, record); err != nil {
				t.Fatal(err)
			}
			if assistants == 24 {
				digest, _, _ := encodeStoredBlock(record.Request.Messages[0].Blocks[0])
				if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=zeroblob(plain_bytes) WHERE digest=?`, digest); err != nil {
					t.Fatal(err)
				}
			}
			view, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewReading)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Request.Messages) != 1 {
				t.Fatalf("messages=%+v", view.Request.Messages)
			}
			message := view.Request.Messages[0]
			if assistants == 23 {
				if message.Role != "user" || message.Blocks[0].Text != "question" || view.Page.InputAnchor == "" {
					t.Fatal("last permitted proof lost the question")
				}
			} else if message.Role != "unknown" || message.Blocks[0].Deferred == nil || view.Page.InputAnchor != "" {
				t.Fatal("role proof exceeded 24 messages or classified the unproved question")
			}
		})
	}
}

func TestReadingViewIntegrityBoundsRoleProofBytes(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
			ctx := context.Background()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			record := contentRecordFixture(t, "proof-bytes", at)
			record.Response = nil
			question := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "question"}
			background := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "x"}
			questionJSON, _ := json.Marshal(question)
			backgroundJSON, _ := json.Marshal(background)
			// One block separator plus 64 bytes of message metadata and the role
			// are included by the existing preview size contract. Both proofs
			// together are exactly 128 KiB, or one byte beyond it.
			textSize := (128 << 10) - (len(questionJSON) + 1 + 64 + len("user")) - (len(backgroundJSON) - 1 + 1 + 64 + len("assistant")) + extra
			background.Text = strings.Repeat("x", textSize)
			record.Request.Messages = []exchangecontent.Message{{Role: "user", Blocks: []exchangecontent.Block{question}}, {Role: "assistant", Blocks: []exchangecontent.Block{background}}}
			repo := store.ExchangeContentRepository()
			if err := repo.Put(ctx, record); err != nil {
				t.Fatal(err)
			}
			if extra == 1 {
				digest, _, _ := encodeStoredBlock(question)
				if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=zeroblob(plain_bytes) WHERE digest=?`, digest); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			view, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewReading)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(view)
			t.Logf("proof bytes=%d preview bytes=%d elapsed=%s", (128<<10)+extra, len(encoded), time.Since(start))
			if len(view.Request.Messages) != 1 {
				t.Fatalf("messages=%+v", view.Request.Messages)
			}
			message := view.Request.Messages[0]
			if extra == 0 {
				if message.Role != "user" || message.Blocks[0].Text != "question" || view.Page.InputAnchor == "" {
					t.Fatal("exact byte budget lost a proved question")
				}
			} else if message.Role != "unknown" || message.Blocks[0].Deferred == nil || view.Page.InputAnchor != "" {
				t.Fatal("role proof exceeded 128 KiB or claimed the unproved question")
			}
		})
	}
}

func TestReadingViewIntegrityDefersUnprovedGroupBoundary(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	record := transcriptContentRecordFixture(t, "unproved-boundary", at, exchangecontent.ParentRef{}, []transcriptMessage{
		{role: protocolcore.RoleAssistant, text: strings.Repeat("x", 128<<10)},
		{role: protocolcore.RoleUser, text: "QUESTION"},
	}, "")
	repo := store.ExchangeContentRepository()
	if err := repo.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	view, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewReading)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Request.Messages) != 1 || view.Request.Messages[0].Role != "unknown" || view.Request.Messages[0].Blocks[0].Deferred == nil || view.Page.InputAnchor != "" || view.Page.RequestOffset != 0 {
		t.Fatalf("partial group claimed an unproved boundary: %+v page=%+v", view.Request.Messages, view.Page)
	}
	cursor, err := decodeContentCursor(view.Request.Messages[0].Blocks[0].Deferred.Cursor)
	if err != nil || cursor.Depth != 1 {
		t.Fatalf("deferred loading points at the wrong position: %+v %v", cursor, err)
	}
}

func TestReadingViewIntegrityRejectsForgedPayloadSizesBeforeHydration(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	record := transcriptContentRecordFixture(t, "forged-size", at, exchangecontent.ParentRef{}, []transcriptMessage{{role: protocolcore.RoleUser, text: "question"}}, "")
	repo := store.ExchangeContentRepository()
	if err := repo.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	digest, _, _ := encodeStoredBlock(record.Request.Messages[0].Blocks[0])
	if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET plain_bytes=100,codec='identity',payload=zeroblob(8388608) WHERE digest=?`, digest); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewReading)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("forged payload accepted: %v", err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("8 MiB stored payload claiming 100 bytes: reading allocated %d bytes", allocated)
	// A 1 MiB allowance covers driver/JSON overhead generously; fetching the
	// corrupt 8 MiB body would exceed it even before decoding. No live data or
	// production instrumentation is needed to observe this budget violation.
	if allocated > 1<<20 {
		t.Fatalf("preflight fetched the forged oversized payload: allocated %d bytes", allocated)
	}
}

func TestReadingViewPreservesBoundedUserGroupSuffix(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	messages := make([]transcriptMessage, 30)
	for i := range messages {
		messages[i] = transcriptMessage{role: protocolcore.RoleUser, text: fmt.Sprintf("group-%d", i)}
	}
	record := transcriptContentRecordFixture(t, "group-suffix", at, exchangecontent.ParentRef{}, messages, "")
	repo := store.ExchangeContentRepository()
	if err := repo.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	ref, err := loadStoredContentReference(ctx, store.database, record.ExchangeID, at)
	if err != nil {
		t.Fatal(err)
	}
	view, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewReading)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Request.Messages) != 24 || view.Request.Messages[0].Blocks[0].Text != "group-6" || view.Request.Messages[23].Blocks[0].Text != "group-29" || view.Page.RequestOffset != 6 || view.Page.InputAnchor != ref.requestRoot {
		t.Fatalf("bounded group lost its suffix or anchor: %+v page=%+v", view.Request.Messages, view.Page)
	}
}

func TestReadingViewPreservesImageGroupInLongHistory(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	record := contentRecordFixture(t, "long-reading", at)
	record.Response = nil
	record.Request.Messages = make([]exchangecontent.Message, 1000)
	for i := range record.Request.Messages {
		record.Request.Messages[i] = exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: fmt.Sprintf("old-%d", i)}}}
	}
	record.Request.Messages = append(record.Request.Messages,
		exchangecontent.Message{Role: "assistant", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "separator"}}},
		exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{{Kind: "provider_extension", Availability: exchangecontent.AvailabilityOmitted, ProviderSource: "openai", ProviderKind: "input_image", Fingerprint: "sha256:" + strings.Repeat("a", 64), OriginalSize: 123}}},
		exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{{Kind: "provider_extension", Availability: exchangecontent.AvailabilityOmitted, ProviderSource: "openai", ProviderKind: "input_image", Fingerprint: "sha256:" + strings.Repeat("b", 64), OriginalSize: 456}}},
		exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "QUESTION"}}},
		exchangecontent.Message{Role: "assistant", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "background"}}},
	)
	repo := store.ExchangeContentRepository()
	if err := repo.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	// Old payloads are outside the group and its verified separator.
	digest, _, _ := encodeStoredBlock(record.Request.Messages[999].Blocks[0])
	if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET payload=x'00' WHERE digest=?`, digest); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	view, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewReading)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	t.Logf("history=1005 verified positions=5 selected messages=%d preview bytes=%d elapsed=%s", len(view.Request.Messages), len(encoded), time.Since(start))
	if len(view.Request.Messages) != 3 || view.Request.Messages[0].Blocks[0].Fingerprint != "sha256:"+strings.Repeat("a", 64) || view.Request.Messages[1].Blocks[0].Fingerprint != "sha256:"+strings.Repeat("b", 64) || view.Request.Messages[2].Blocks[0].Text != "QUESTION" || view.Page.RequestOffset != 1001 || view.Page.InputAnchor == "" || view.Page.RequestNextCursor == "" {
		t.Fatalf("image/question group or position lost: %+v page=%+v", view.Request.Messages, view.Page)
	}
	if len(encoded) > exchangecontent.MaxPageBytes {
		t.Fatalf("oversized reading preview: %d", len(encoded))
	}
	// Appending background work replays the same observed input position.
	anchor := view.Page.InputAnchor
	replay := record.Clone()
	replay.ExchangeID = "long-reading-replay"
	replay.Request.Messages = append(replay.Request.Messages, exchangecontent.Message{Role: "assistant", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "more background"}}})
	if err := repo.Put(ctx, replay); err != nil {
		t.Fatal(err)
	}
	view, err = repo.GetPagedProjection(ctx, replay.ExchangeID, at, exchangecontent.RequestViewReading)
	if err != nil || view.Page.InputAnchor != anchor {
		t.Fatalf("replay changed anchor: %+v %v", view.Page, err)
	}
	// The old damaged payload remains visible to an independent full read.
	if _, err := repo.Get(ctx, record.ExchangeID, at); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("full control ignored damaged history: %v", err)
	}
}

func BenchmarkReadingContentProof(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("messages=%d", size), func(b *testing.B) {
			store := openTestStore(b, filepath.Join(b.TempDir(), "runtime.db"))
			b.Cleanup(func() { shutdownTestStore(b, store) })
			ctx := context.Background()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			record := contentRecordFixture(b, "benchmark-reading", at)
			record.Response = nil
			record.Request.Messages = make([]exchangecontent.Message, size)
			for i := range size {
				record.Request.Messages[i] = exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: fmt.Sprintf("%d %s", i, strings.Repeat("context ", 128))}}}
			}
			repo := store.ExchangeContentRepository()
			if err := repo.Put(ctx, record); err != nil {
				b.Fatal(err)
			}
			for _, view := range []exchangecontent.RequestView{exchangecontent.RequestViewFull, exchangecontent.RequestViewReading} {
				b.Run(string(view), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if _, err := repo.GetPagedProjection(ctx, record.ExchangeID, at, view); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
