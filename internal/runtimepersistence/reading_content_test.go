package runtimepersistence

import (
	"context"
	"encoding/json"
	"path/filepath"
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
