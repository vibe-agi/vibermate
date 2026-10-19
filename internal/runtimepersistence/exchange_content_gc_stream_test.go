package runtimepersistence

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestContentGCKeepsRepeatedMessagesOnRetainedParentChain(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	tx, err := store.database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	messages := make([]string, 3)
	for i := range messages {
		block := deletionFixtureDigest(fmt.Sprintf("stream-block-%d", i))
		messages[i] = deletionFixtureDigest(fmt.Sprintf("stream-message-%d", i))
		if _, err = tx.Exec(`INSERT INTO runtime_exchange_content_blocks VALUES(?,4,'identity',?)`, block, []byte("body")); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO runtime_exchange_content_messages(digest,role,block_manifest) VALUES(?,'user',?)`, messages[i], block); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 257; i++ {
		var parent any
		if i > 1 {
			parent = deletionFixtureDigest(fmt.Sprintf("stream-node-%d", i-1))
		}
		message := messages[i%2]
		if i == 257 {
			message = messages[2]
		}
		if _, err = tx.Exec(`INSERT INTO runtime_exchange_content_transcripts(digest,parent_digest,message_digest,depth) VALUES(?,?,?,?)`, deletionFixtureDigest(fmt.Sprintf("stream-node-%d", i)), parent, message, i); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []struct {
		id    string
		depth int
	}{{"expired-long-chain", 257}, {"retained-prefix", 128}} {
		digest := deletionFixtureDigest(fmt.Sprintf("stream-node-%d", root.depth))
		if _, err = tx.Exec(`INSERT INTO runtime_exchange_contents(exchange_id,mode,recorded_at_unix_ms,expires_at_unix_ms,request_transcript_digest,expected_transcript_digest,request_message_count,expected_message_count,inherited_message_count,response_message_digest,system_message_digest,manifest_json) VALUES(?,'full',1,2,?,?,?,?,0,?,?,?)`, root.id, digest, digest, root.depth, root.depth, messages[0], messages[1], []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	remove := func(id string) {
		t.Helper()
		tx, err := store.database.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if n, err := deleteExchangeContent(t.Context(), tx, `DELETE FROM runtime_exchange_contents WHERE exchange_id=?`, id); err != nil || n != 1 {
			t.Fatalf("remove root: %d %v", n, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	remove("expired-long-chain")
	var nodes, retainedMessages, blocks int
	if err = store.database.QueryRow(`SELECT (SELECT count(*) FROM runtime_exchange_content_transcripts),(SELECT count(*) FROM runtime_exchange_content_messages),(SELECT count(*) FROM runtime_exchange_content_blocks)`).Scan(&nodes, &retainedMessages, &blocks); err != nil || nodes != 128 || retainedMessages != 2 || blocks != 2 {
		t.Fatalf("retained repeated-message prefix damaged: %d %d %d %v", nodes, retainedMessages, blocks, err)
	}
	remove("retained-prefix")
	if err = store.database.QueryRow(`SELECT (SELECT count(*) FROM runtime_exchange_content_transcripts)+(SELECT count(*) FROM runtime_exchange_content_messages)+(SELECT count(*) FROM runtime_exchange_content_blocks)`).Scan(&nodes); err != nil || nodes != 0 {
		t.Fatalf("last repeated reference leaked graph: %d %v", nodes, err)
	}
}
