package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// SQL triggers observe actual message/block INSERT attempts, including conflict
// upserts. Repeating publication work, losing a transcript occurrence, caching
// success across rollback, or skipping the first collision guard breaks these
// real Store assertions.
func TestContentSourcePublicationReusesSuccessfulMessagesWithinTransaction(t *testing.T) {
	for _, scenario := range []string{"mixed_completion", "rollback_retry", "collision_first_publication", "block_collision_first_publication"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			limits := candidateContentLimits()
			store := openCandidateContentStore(t, filepath.Join(t.TempDir(), "publication.db"), &limits)
			for _, statement := range []string{
				`CREATE TABLE task6_publication_attempts(kind TEXT NOT NULL,digest TEXT NOT NULL)`,
				`CREATE TRIGGER task6_message_attempt BEFORE INSERT ON runtime_exchange_content_messages BEGIN INSERT INTO task6_publication_attempts VALUES('message',NEW.digest); END`,
				`CREATE TRIGGER task6_block_attempt BEFORE INSERT ON runtime_exchange_content_blocks BEGIN INSERT INTO task6_publication_attempts VALUES('block',NEW.digest); END`,
			} {
				if _, err := store.database.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			record := sourceOnlyRecord(t, "publication", sourceText("same"))
			texts := []string{"same", "same", "unique", "same", "TAIL"}
			record.Request.Messages = nil
			for _, text := range texts {
				record.Request.Messages = append(record.Request.Messages, exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{sourceText(text)}})
			}
			source, err := exchangecontent.SourceFromRecordWithin(limits, record)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "rollback_retry" {
				if _, err := store.database.Exec(`CREATE TRIGGER task6_node_fault BEFORE INSERT ON runtime_exchange_content_transcripts WHEN NEW.depth=3 BEGIN SELECT RAISE(ABORT,'task6 injected node failure'); END`); err != nil {
					t.Fatal(err)
				}
				if err := putCandidateSource(t, store, source); err == nil {
					t.Fatal("node fault published content")
				}
				for _, table := range []string{"runtime_exchange_contents", "runtime_exchange_content_messages", "runtime_exchange_content_blocks", "runtime_exchange_content_transcripts", "task6_publication_attempts"} {
					if countRows(t, store, table) != 0 {
						t.Fatalf("rollback retained rows in %s", table)
					}
				}
				if _, err := store.database.Exec(`DROP TRIGGER task6_node_fault`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "collision_first_publication" || scenario == "block_collision_first_publication" {
				base := sourceOnlyRecord(t, "existing", sourceText("same"))
				if err := store.exchangeContents.Put(ctx, base); err != nil {
					t.Fatal(err)
				}
				mutation := `UPDATE runtime_exchange_content_messages SET role='assistant'`
				if scenario == "block_collision_first_publication" {
					mutation = `UPDATE runtime_exchange_content_blocks SET plain_bytes=plain_bytes-1`
				}
				if _, err := store.database.Exec(mutation); err != nil {
					t.Fatal(err)
				}
				err := putCandidateSource(t, store, source)
				if err == nil || (scenario == "collision_first_publication" && !errors.Is(err, exchangecontent.ErrInvalidEvidence)) {
					t.Fatalf("first message collision guard bypassed: %v", err)
				}
				if countRows(t, store, "runtime_exchange_contents") != 1 || countRows(t, store, "runtime_exchange_content_messages") != 1 || countRows(t, store, "runtime_exchange_content_transcripts") != 1 {
					t.Fatal("collision published partial content")
				}
				restore := `UPDATE runtime_exchange_content_messages SET role='user'`
				if scenario == "block_collision_first_publication" {
					restore = `UPDATE runtime_exchange_content_blocks SET plain_bytes=plain_bytes+1`
				}
				if _, err := store.database.Exec(restore); err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.Exec(`DELETE FROM task6_publication_attempts`); err != nil {
					t.Fatal(err)
				}
			}
			if err := putCandidateSource(t, store, source); err != nil {
				t.Fatal(err)
			}
			got, err := store.exchangeContents.Get(ctx, record.ExchangeID, record.RecordedAt)
			if err != nil || !reflect.DeepEqual(got.Request.Messages, record.Request.Messages) {
				t.Fatalf("complete ordered occurrences changed: %v", err)
			}
			if countRows(t, store, "runtime_exchange_content_messages") != 3 || countRows(t, store, "runtime_exchange_content_transcripts") != 5 {
				t.Fatal("distinct rows or complete parent-chain depths changed")
			}
			if countRows(t, store, "runtime_exchange_content_blocks") != 3 || countRows(t, store, "runtime_exchange_content_block_refs") != 3 {
				t.Fatal("distinct physical rows or manifest refs changed")
			}
			assertAttempts := func(want int) {
				t.Helper()
				for _, kind := range []string{"message", "block"} {
					var attempts int
					if err := store.database.QueryRow(`SELECT count(*) FROM task6_publication_attempts WHERE kind=?`, kind).Scan(&attempts); err != nil {
						t.Fatal(err)
					}
					if attempts != want {
						t.Errorf("%s publication attempts=%d want=%d distinct messages", kind, attempts, want)
					}
				}
			}
			assertAttempts(3)
			if scenario == "mixed_completion" {
				completed := record.Clone()
				completed.Response = &exchangecontent.Response{ID: "response", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model", StopReason: "end_turn", Blocks: []exchangecontent.Block{sourceText("REPLY")}}
				final, err := exchangecontent.SourceFromRecordWithin(limits, completed)
				if err != nil {
					t.Fatal(err)
				}
				if err := putCandidateSource(t, store, final); err != nil {
					t.Fatal(err)
				}
				got, err := store.exchangeContents.Get(ctx, record.ExchangeID, record.RecordedAt)
				if err != nil || !reflect.DeepEqual(got.Request.Messages, record.Request.Messages) || got.Response == nil || got.Response.Blocks[0].Text != "REPLY" {
					t.Fatalf("completion changed history or lost response: %v", err)
				}
				if countRows(t, store, "runtime_exchange_content_transcripts") != 6 {
					t.Fatal("completion lost node")
				}
				assertAttempts(4)
			}
			sourceDatabaseIntegrity(t, store)
		})
	}
}
