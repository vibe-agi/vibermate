package runtimepersistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestContentPagesBoundLargeCheckpointAndPreserveEveryMessage(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	messages := make([]transcriptMessage, 1118)
	for i := range messages {
		messages[i] = transcriptMessage{role: protocolcore.RoleUser, text: fmt.Sprintf("message-%04d ", i) + strings.Repeat("x", 2048)}
	}
	record := transcriptContentRecordFixture(t, "large-history", at, exchangecontent.ParentRef{CaptureRunID: "test-run"}, messages, "answer")
	repository := store.ExchangeContentRepository()
	if err := repository.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	first, err := repository.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewIncremental)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(first)
	if len(encoded) > exchangecontent.MaxPageBytes || first.Page == nil || len(first.Request.Messages) != 0 || first.Page.RequestNextCursor == "" || first.Response.Blocks[0].Text != "answer" {
		t.Fatalf("checkpoint eagerly loaded history: bytes=%d page=%+v", len(encoded), first.Page)
	}
	cursor := first.Page.RequestNextCursor
	end := len(messages)
	for cursor != "" {
		page, err := repository.GetContentPage(ctx, record.ExchangeID, at, cursor)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) > exchangecontent.MaxPageBytes || len(page.Messages) > exchangecontent.PageMessageLimit || page.Offset+len(page.Messages) != end {
			t.Fatalf("invalid page: bytes=%d offset=%d items=%d end=%d", len(encoded), page.Offset, len(page.Messages), end)
		}
		for i, message := range page.Messages {
			if message.Blocks[0].Text != messages[page.Offset+i].text {
				t.Fatalf("history reordered at %d", page.Offset+i)
			}
		}
		end, cursor = page.Offset, page.NextCursor
	}
	if end != 0 {
		t.Fatalf("lost history before %d", end)
	}
	// Ordinary reads must not hydrate or verify unrelated history payloads.
	_, _, transcript, err := encodeStoredContent(record)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, _, _ := encodeStoredBlock(record.Request.Messages[0].Blocks[0])
	if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET payload=x'00' WHERE digest=?`, oldDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewFull); err != nil {
		t.Fatalf("tail page hydrated old payload: %v", err)
	}
	bad := pageCursor(storedContentReference{manifest: storedExchangeContentManifest{ExchangeID: record.ExchangeID}, requestRoot: transcript.requestRoot}, "request", "message", 1)
	if _, err := repository.GetContentPage(ctx, record.ExchangeID, at, encodeContentCursor(bad)); err == nil {
		t.Fatal("explicit corrupted message was accepted")
	}
}

func TestContentPagesBoundProtocolMetadataAndDoNotInflateLargePreviews(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	record := contentRecordFixture(t, "large-metadata", at)
	record.Request.Messages[0].Blocks[0].Text = strings.Repeat("x", 2<<20)
	record.Request.Messages[0].Blocks[0].OriginalSize = 2 << 20
	record.Response.ProtocolEvidence = nil
	for i := range 5000 {
		record.Response.ProtocolEvidence = append(record.Response.ProtocolEvidence, protocolcore.ProtocolEvidenceValue{Name: fmt.Sprintf("provider.output.%04d.id", i), Value: strings.Repeat("v", 512)})
	}
	repository := store.ExchangeContentRepository()
	if err := repository.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	first, err := repository.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Response.ProtocolEvidence) != exchangecontent.PageMessageLimit || first.Page.ResponseEvidenceNextCursor == "" {
		t.Fatal("protocol evidence was not paged")
	}
	page, err := repository.GetContentPage(ctx, record.ExchangeID, at, first.Page.ResponseEvidenceNextCursor)
	if err != nil || page.Kind != "protocol" || page.Offset != exchangecontent.PageMessageLimit || len(page.ProtocolEvidence) != exchangecontent.PageMessageLimit || page.Total != 5000 {
		t.Fatalf("protocol page: %v", err)
	}
	badDigest, _, _ := encodeStoredBlock(record.Request.Messages[0].Blocks[0])
	if _, err := store.database.Exec(`UPDATE runtime_exchange_content_blocks SET payload=x'00' WHERE digest=?`, badDigest); err != nil {
		t.Fatal(err)
	}
	previews, err := repository.RequestPreviews(ctx, []string{record.ExchangeID}, at)
	if err != nil || len(previews) != 0 {
		t.Fatalf("directory inflated a large preview: %v", err)
	}
}

func TestContentPagesDeferLargeBodiesAndValidateCursors(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const fragment = "\u957f\u5185\u5bb9\U0001f642\n"
	text := strings.Repeat(fragment, 200000)
	record := transcriptContentRecordFixture(t, "large-output", at, exchangecontent.ParentRef{}, []transcriptMessage{{role: protocolcore.RoleUser, text: "question"}}, text)
	repository := store.ExchangeContentRepository()
	if err := repository.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	first, err := repository.GetPagedProjection(ctx, record.ExchangeID, at, exchangecontent.RequestViewIncremental)
	if err != nil {
		t.Fatal(err)
	}
	deferred := first.Response.Blocks[0].Deferred
	if deferred == nil {
		t.Fatal("large body was eagerly hydrated")
	}
	message, err := repository.GetContentPage(ctx, record.ExchangeID, at, deferred.Cursor)
	if err != nil || len(message.Blocks) != 1 || message.Blocks[0].Deferred == nil {
		t.Fatalf("large block page: %v", err)
	}
	bodyCursor := message.Blocks[0].Deferred.Cursor
	page, err := repository.GetContentPage(ctx, record.ExchangeID, at, bodyCursor)
	if err != nil || page.Kind != "text" || page.Offset != 0 || page.Total != len(text) || !utf8.ValidString(page.Text) || len(page.Text) > exchangecontent.PageBodyBytes || !strings.HasPrefix(text, page.Text) {
		t.Fatalf("body page: %v", err)
	}
	second, err := repository.GetContentPage(ctx, record.ExchangeID, at, page.NextCursor)
	if err != nil || second.Offset != len(page.Text) || text[second.Offset:second.Offset+len(second.Text)] != second.Text {
		t.Fatalf("body continuation: %v", err)
	}
	position, _ := decodeContentCursor(bodyCursor)
	position.Offset = len(text) - len(fragment)
	last, err := repository.GetContentPage(ctx, record.ExchangeID, at, encodeContentCursor(position))
	if err != nil || last.NextCursor != "" || last.Text != fragment {
		t.Fatalf("last body page: %+v %v", last, err)
	}
	for _, mutate := range []func(*contentCursor){
		func(c *contentCursor) { c.Exchange = "another-exchange" },
		func(c *contentCursor) { c.Root = strings.Repeat("a", 64) },
		func(c *contentCursor) { c.Location = "unknown" },
		func(c *contentCursor) { c.Block = -1 },
		func(c *contentCursor) { c.Block = 10000 },
		func(c *contentCursor) { c.Offset = 1 }, // Middle of a UTF-8 character.
	} {
		c, _ := decodeContentCursor(bodyCursor)
		mutate(&c)
		if _, err := repository.GetContentPage(ctx, record.ExchangeID, at, encodeContentCursor(c)); err == nil {
			t.Fatalf("invalid cursor accepted: %+v", c)
		}
	}
	if _, err := repository.GetContentPage(ctx, record.ExchangeID, record.ExpiresAt, bodyCursor); !errors.Is(err, exchangecontent.ErrNotFound) {
		t.Fatalf("expiry: %v", err)
	}
	copy := first.Clone()
	copy.Response.Blocks[0].Deferred.Cursor = "changed"
	if first.Response.Blocks[0].Deferred.Cursor != deferred.Cursor || first.Response.Blocks[0].Deferred.Cursor == "changed" {
		t.Fatal("placeholder aliases cloned evidence")
	}
	record.Response.Blocks = first.Response.Blocks
	if err := record.Validate(); err == nil {
		t.Fatal("read-time placeholder admitted to immutable recording")
	}
}
