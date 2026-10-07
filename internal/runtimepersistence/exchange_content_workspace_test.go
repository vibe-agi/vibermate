package runtimepersistence

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// Restoring a scratch-bearing stateless canonical writer for each first-pass
// message/block makes this real Store operation allocate at least8MiB solely
// for those1024 pairs. Warm fixed database/codec setup, then require less than
// that amount while independently verifying all occurrences and their tail.
func TestContentSourceCanonicalWorkspaceAvoidsPerOccurrenceScratch(t *testing.T) {
	ctx := context.Background()
	limits := candidateContentLimits()
	store := openCandidateContentStore(t, filepath.Join(t.TempDir(), "workspace.db"), &limits)
	warm := sourceOnlyRecord(t, "workspace-warm", sourceText("warm"))
	if err := store.exchangeContents.Put(ctx, warm); err != nil {
		t.Fatal(err)
	}
	record := sourceOnlyRecord(t, "workspace-repeated", sourceText("same"))
	record.Request.Messages = make([]exchangecontent.Message, 1024)
	for i := range record.Request.Messages {
		record.Request.Messages[i] = exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{sourceText("same")}}
	}
	record.Request.Messages[1023].Blocks[0] = sourceText("TAIL")
	source, err := exchangecontent.SourceFromRecordWithin(limits, record)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = putCandidateSource(t, store, source)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("real Store1024-occurrence canonical allocation=%d B", allocated)
	if allocated >= 8<<20 {
		t.Errorf("per-occurrence fixed scratch retained: allocated=%d B want<8MiB", allocated)
	}
	got, err := store.exchangeContents.Get(ctx, record.ExchangeID, record.RecordedAt)
	if err != nil || len(got.Request.Messages) != 1024 {
		t.Fatalf("complete occurrence readback: %v", err)
	}
	for i, message := range got.Request.Messages {
		want := "same"
		if i == 1023 {
			want = "TAIL"
		}
		if len(message.Blocks) != 1 || message.Blocks[0].Text != want {
			t.Fatalf("changed occurrence%d", i)
		}
	}
	sourceDatabaseIntegrity(t, store)
}
