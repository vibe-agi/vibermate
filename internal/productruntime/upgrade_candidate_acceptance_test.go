package productruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const task7ProducerSource = "candidate"

func task7BindAdmission(r *Runtime, e *exchangeBuildRequest, p *proxyBuildRequest) {
	e.bodyAdmission = r.bodyAdmission
	p.bodyAdmission = r.bodyAdmission
}
func task7Upgrade(t *testing.T, ctx context.Context, root string) {
	live := filepath.Join(root, "live")
	var receipt task7Receipt
	task7Load(t, filepath.Join(root, "old-receipt.json"), &receipt)
	history := make([]string, 4111)
	history[0] = "old-prefix-0"
	for i := 1; i < len(history); i++ {
		history[i] = fmt.Sprintf("old-continuation-%08d", i)
	}
	history[len(history)-1] = "task7-final-sentinel"
	if os.Getenv("TASK7_PHASE") == "upgrade" {
		task7VerifyStopped(t, live, receipt)
		f := task7Open(t, ctx, live)
		proxy := f.bind(http.HandlerFunc(f.ordinaryUpstream))
		f.send(proxy, receipt.Credentials[0], history, "ordinary-reply")
		f.send(proxy, receipt.Credentials[0], []string{"framed-only"}, strings.Repeat("&", 6<<20))
		f.close()
	}
	// Reopen the actual candidate Runtime and exhaust its full/paged reader.
	f := task7Open(t, ctx, live)
	for _, id := range task7IDs(t, live) {
		record, e := f.runtime.ExchangeContents().Get(ctx, id)
		task7Must(t, e)
		if record.Response == nil || record.Response.ID != "resp_task7" || record.Response.Usage != (exchangecontent.Usage{Output: exchangecontent.UsageValue{Known: true, Tokens: 2, Source: "openai-responses"}}) {
			t.Fatalf("response identity/usage lost for %s", id)
		}
		if record.Mode == environment.ContentRecordingMetadataOnly {
			if record.Request.Messages[0].Blocks[0].Text != "" || record.Response.Blocks[0].Text != "" {
				t.Fatal("metadata body leak")
			}
		}
		if _, old := receipt.Records[id]; !old {
			want := history
			reply := "ordinary-reply"
			if len(record.Request.Messages) == 1 {
				want = []string{"framed-only"}
				reply = strings.Repeat("&", 6<<20)
			}
			task7CheckRecord(t, record, want, reply)
			if record.Parent.ManualCaptureID != receipt.Captures[0] {
				t.Fatal("capture identity changed")
			}
		}
		task7CheckPages(t, ctx, f.runtime.ExchangeContents(), record)
	}
	f.close()
	task7CheckGraph(t, live, true)
	before, after := receipt.Snapshot, task7Snapshot(t, live)
	if !reflect.DeepEqual(before["sqlite_master"], after["sqlite_master"]) || !reflect.DeepEqual(before["runtime_metadata"], after["runtime_metadata"]) {
		t.Fatal("schema changed")
	}
	appendOnly := map[string]bool{"runtime_activities": true, "runtime_connection_events": true, "runtime_egress_attempts": true, "runtime_usage_observations": true, "runtime_raw_evidence_writer_sessions": true, "runtime_exchange_content_blocks": true, "runtime_exchange_content_messages": true, "runtime_exchange_content_block_refs": true, "runtime_exchange_content_transcripts": true, "runtime_exchange_contents": true}
	for table, oldRows := range before {
		if table == "manual_captures" || table == "sqlite_sequence" {
			continue
		}
		if !appendOnly[table] && !reflect.DeepEqual(oldRows, after[table]) {
			t.Fatalf("unexpected mutation in %s", table)
		}
		set := map[string]bool{}
		for _, row := range after[table] {
			set[row] = true
		}
		for _, row := range before[table] {
			if !set[row] {
				t.Fatalf("immutable old row changed in %s", table)
			}
		}
	}
	decodeRows := func(rows []string) map[string][]string {
		result := map[string][]string{}
		for _, row := range rows[1:] {
			var cells []string
			task7Must(t, json.Unmarshal([]byte(row), &cells))
			result[cells[0]] = cells
		}
		return result
	}
	oldManual, newManual := decodeRows(before["manual_captures"]), decodeRows(after["manual_captures"])
	if len(oldManual) != len(newManual) {
		t.Fatal("capture catalog changed")
	}
	for key, old := range oldManual {
		current := newManual[key]
		if len(current) != len(old) {
			t.Fatal("capture row lost")
		}
		for i, value := range old {
			if value == current[i] {
				continue
			}
			if key != "string:"+strconv.Quote(receipt.Captures[0]) || (i != 11 && i != 13) {
				t.Fatalf("unexpected manual capture cell mutation %s/%d", key, i)
			}
			a, e := strconv.ParseInt(strings.TrimPrefix(value, "int64:"), 10, 64)
			task7Must(t, e)
			b, e := strconv.ParseInt(strings.TrimPrefix(current[i], "int64:"), 10, 64)
			task7Must(t, e)
			if b <= a {
				t.Fatal("capture observed timestamp did not advance")
			}
		}
	}
	oldSequences, newSequences := decodeRows(before["sqlite_sequence"]), decodeRows(after["sqlite_sequence"])
	if len(oldSequences) != len(newSequences) {
		t.Fatal("unexpected sequence catalog change")
	}
	for key, old := range oldSequences {
		current := newSequences[key]
		var table string
		task7Must(t, json.Unmarshal([]byte(strings.TrimPrefix(key, "string:")), &table))
		a, e := strconv.ParseInt(strings.TrimPrefix(old[1], "int64:"), 10, 64)
		task7Must(t, e)
		b, e := strconv.ParseInt(strings.TrimPrefix(current[1], "int64:"), 10, 64)
		task7Must(t, e)
		if b-a != int64(len(after[table])-len(before[table])) {
			t.Fatalf("sequence delta differs from actual added rows: %s", table)
		}
	}
	deltas := map[string]any{}
	for table, rows := range before {
		if !reflect.DeepEqual(rows, after[table]) {
			deltas[table] = map[string]any{"before": rows, "after": after[table]}
		}
	}
	task7Write(t, filepath.Join(root, "candidate-row-deltas.json"), deltas)
	if !reflect.DeepEqual(task7Read(t, live, receipt.Exchanges), receipt.Records) {
		t.Fatal("immutable old canonical records changed")
	}
	var added []string
	for _, id := range task7IDs(t, live) {
		if _, ok := receipt.Records[id]; !ok {
			added = append(added, id)
		}
	}
	if len(added) != 2 {
		t.Fatalf("new exchanges=%d", len(added))
	}
	task7Write(t, filepath.Join(root, "new-exchanges.json"), added)
	task7Backup(t, ctx, root, "post-upgrade-backup")
	task7Must(t, runtimedata.Restore(ctx, filepath.Join(root, "pre-upgrade-backup"), filepath.Join(root, "old-restored")))
	task7Must(t, runtimedata.Restore(ctx, filepath.Join(root, "post-upgrade-backup"), filepath.Join(root, "post-for-old-reader")))
}

func task7CheckRecord(t *testing.T, r exchangecontent.Record, want []string, response string) {
	t.Helper()
	if len(r.Request.Messages) != len(want) {
		t.Fatalf("history count=%d want=%d", len(r.Request.Messages), len(want))
	}
	for i, m := range r.Request.Messages {
		if m.Role != "user" || len(m.Blocks) != 1 || m.Blocks[0].Text != want[i] {
			t.Fatalf("history occurrence %d changed", i)
		}
	}
	if r.Response == nil || len(r.Response.Blocks) != 1 || r.Response.Blocks[0].Text != response {
		t.Fatal("response body changed")
	}
	t.Logf("full record exchange=%s history=%d response_bytes=%d response_sha256=%s", r.ExchangeID, len(want), len(response), task7Hash([]byte(response)))
}
func task7CheckPages(t *testing.T, ctx context.Context, reader exchangecontent.Reader, record exchangecontent.Record) {
	t.Helper()
	full, e := reader.GetProjection(ctx, record.ExchangeID, exchangecontent.RequestViewFull)
	task7Must(t, e)
	if !reflect.DeepEqual(full.Request.Messages, record.Request.Messages) || !reflect.DeepEqual(full.Response, record.Response) {
		t.Fatal("full projection changed complete evidence")
	}
	p, e := reader.GetPagedProjection(ctx, record.ExchangeID, exchangecontent.RequestViewFull)
	task7Must(t, e)
	seen := make([]bool, len(record.Request.Messages))
	var checkBlocks func([]exchangecontent.Block, []exchangecontent.Block)
	checkBlocks = func(got, want []exchangecontent.Block) {
		if len(got) == 1 && got[0].Deferred != nil {
			page, e := reader.GetContentPage(ctx, record.ExchangeID, got[0].Deferred.Cursor)
			task7Must(t, e)
			if len(page.Blocks) > 0 {
				checkBlocks(page.Blocks, want)
				return
			}
		}
		if len(got) != len(want) {
			t.Fatalf("paged block shape=%d want=%d", len(got), len(want))
		}
		for i, b := range got {
			if b.Deferred == nil {
				if !reflect.DeepEqual(b, want[i]) {
					t.Fatal("paged block changed")
				}
				continue
			}
			cursor := b.Deferred.Cursor
			var body strings.Builder
			offset := 0
			for cursor != "" {
				page, e := reader.GetContentPage(ctx, record.ExchangeID, cursor)
				task7Must(t, e)
				if page.Offset != offset {
					t.Fatal("body offset discontinuity")
				}
				body.WriteString(page.Text)
				offset += len(page.Text)
				cursor = page.NextCursor
				if len(page.Data) > 0 {
					t.Fatal("unexpected canonical-only fixture body")
				}
			}
			if body.String() != want[i].Text {
				t.Fatal("paged body hash mismatch")
			}
		}
	}
	check := func(messages []exchangecontent.Message, offset int) {
		for i, m := range messages {
			index := offset + i
			if index < 0 || index >= len(seen) || seen[index] {
				t.Fatal("duplicate/out-of-range history page")
			}
			seen[index] = true
			want := record.Request.Messages[index]
			if m.Role != "unknown" && m.Role != want.Role {
				t.Fatal("page role changed")
			}
			checkBlocks(m.Blocks, want.Blocks)
		}
	}
	offset := 0
	cursor := ""
	if p.Page != nil {
		offset = p.Page.RequestOffset
		cursor = p.Page.RequestNextCursor
	}
	check(p.Request.Messages, offset)
	for cursor != "" {
		page, e := reader.GetContentPage(ctx, record.ExchangeID, cursor)
		task7Must(t, e)
		check(page.Messages, page.Offset)
		cursor = page.NextCursor
	}
	for _, ok := range seen {
		if !ok {
			t.Fatal("incomplete paged history")
		}
	}
	if p.Response == nil {
		t.Fatal("missing paged response")
	}
	checkBlocks(p.Response.Blocks, record.Response.Blocks)
	t.Logf("exhausted full/body cursors exchange=%s occurrences=%d", record.ExchangeID, len(seen))
}
func task7CheckGraph(t *testing.T, directory string, requireFrames bool) {
	db, e := sql.Open("sqlite", "file:"+filepath.Join(directory, "runtime.db")+"?mode=ro")
	task7Must(t, e)
	defer db.Close()
	rows, e := db.Query(`PRAGMA foreign_key_check`)
	task7Must(t, e)
	if rows.Next() {
		t.Fatal("dangling foreign key")
	}
	task7Must(t, rows.Err())
	rows.Close()
	for _, query := range []string{
		`SELECT count(*) FROM runtime_exchange_content_blocks b WHERE NOT EXISTS(SELECT 1 FROM runtime_exchange_content_block_refs r WHERE r.block_digest=b.digest)`,
		`SELECT count(*) FROM runtime_exchange_content_messages m WHERE NOT EXISTS(SELECT 1 FROM runtime_exchange_content_transcripts n WHERE n.message_digest=m.digest) AND NOT EXISTS(SELECT 1 FROM runtime_exchange_contents c WHERE c.response_message_digest=m.digest OR c.system_message_digest=m.digest)`,
		`WITH RECURSIVE expected(message_digest,block_digest,manifest,position) AS (SELECT digest,substr(block_manifest,1,64),block_manifest,1 FROM runtime_exchange_content_messages UNION ALL SELECT message_digest,substr(manifest,position+64,64),manifest,position+64 FROM expected WHERE position+64<=length(manifest)) SELECT count(*) FROM (SELECT DISTINCT message_digest,block_digest FROM expected EXCEPT SELECT message_digest,block_digest FROM runtime_exchange_content_block_refs)`,
		`WITH RECURSIVE expected(message_digest,block_digest,manifest,position) AS (SELECT digest,substr(block_manifest,1,64),block_manifest,1 FROM runtime_exchange_content_messages UNION ALL SELECT message_digest,substr(manifest,position+64,64),manifest,position+64 FROM expected WHERE position+64<=length(manifest)) SELECT count(*) FROM (SELECT message_digest,block_digest FROM runtime_exchange_content_block_refs EXCEPT SELECT DISTINCT message_digest,block_digest FROM expected)`,
		`WITH RECURSIVE reachable(digest) AS (SELECT request_transcript_digest FROM runtime_exchange_contents UNION SELECT expected_transcript_digest FROM runtime_exchange_contents UNION SELECT base_transcript_digest FROM runtime_exchange_contents WHERE base_transcript_digest IS NOT NULL UNION SELECT n.parent_digest FROM runtime_exchange_content_transcripts n JOIN reachable r ON r.digest=n.digest WHERE n.parent_digest IS NOT NULL) SELECT count(*) FROM runtime_exchange_content_transcripts n WHERE NOT EXISTS(SELECT 1 FROM reachable r WHERE r.digest=n.digest)`,
	} {
		var n int
		task7Must(t, db.QueryRow(query).Scan(&n))
		if n != 0 {
			t.Fatalf("reference graph mismatch=%d", n)
		}
	}
	var frames int
	task7Must(t, db.QueryRow(`SELECT max(length(m.block_manifest)/64) FROM runtime_exchange_content_messages m JOIN runtime_exchange_contents c ON c.response_message_digest=m.digest`).Scan(&frames))
	if requireFrames && frames <= 1 {
		t.Fatal("six-MiB ampersands did not produce multiple stored frames")
	}
	t.Logf("actual stored response frame count=%d", frames)
}
