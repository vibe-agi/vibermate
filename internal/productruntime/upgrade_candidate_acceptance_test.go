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
	var added []string
	for _, id := range task7IDs(t, live) {
		if _, ok := receipt.Records[id]; !ok {
			added = append(added, id)
		}
	}
	if len(added) != 2 {
		t.Fatalf("new exchanges=%d", len(added))
	}
	// Expected traffic additions are derived from these two exchanges, not from
	// a table-wide append allowance.
	if err := task7VerifyTrafficAdditions(before, after, task7TrafficExpectation{Exchanges: added, Capture: receipt.Captures[0]}); err != nil {
		t.Fatal(err)
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

type task7TrafficExpectation struct {
	Exchanges []string
	Capture   string
}

func task7VerifyTrafficAdditions(before, after map[string][]string, want task7TrafficExpectation) error {
	if len(want.Exchanges) != 2 || want.Exchanges[0] == "" || want.Exchanges[1] == "" || want.Exchanges[0] == want.Exchanges[1] || want.Capture == "" {
		return fmt.Errorf("invalid traffic expectation")
	}
	for _, table := range []string{"runtime_activities", "runtime_connection_events", "runtime_egress_attempts", "runtime_usage_observations", "runtime_raw_evidence_writer_sessions"} {
		for _, row := range before[table][1:] {
			if !task7ContainsRow(after[table], row) {
				return fmt.Errorf("old row changed: %s", table)
			}
		}
	}
	added := map[string][][]string{}
	for _, table := range []string{"runtime_activities", "runtime_connection_events", "runtime_egress_attempts", "runtime_usage_observations", "runtime_raw_evidence_writer_sessions"} {
		rows, err := task7AddedSnapshotRows(before[table], after[table])
		if err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		added[table] = rows
	}
	wantCounts := map[string]int{"runtime_activities": 4, "runtime_connection_events": 8, "runtime_egress_attempts": 2, "runtime_usage_observations": 2, "runtime_raw_evidence_writer_sessions": 2}
	for table, n := range wantCounts {
		if len(added[table]) != n {
			return fmt.Errorf("%s additions=%d want=%d", table, len(added[table]), n)
		}
	}
	activityColumns := task7SnapshotColumns(before["runtime_activities"])
	activityIndex := task7ColumnIndex(activityColumns)
	activityByKey := map[string][]string{}
	for _, row := range added["runtime_activities"] {
		if err := task7RequireColumns(row, activityIndex, "activity"); err != nil {
			return err
		}
		id, ok := task7SnapshotString(row[activityIndex["subject_id"]])
		if !ok {
			return fmt.Errorf("activity subject is not a string")
		}
		kind, ok := task7SnapshotString(row[activityIndex["kind"]])
		if !ok {
			return fmt.Errorf("activity kind is not a string")
		}
		if !task7StringField(row, activityIndex, "environment_id", "task7-full") || !task7IntField(row, activityIndex, "environment_revision", 1) || !task7StringField(row, activityIndex, "environment_digest", "aed2809ac3c9e27329233811e6eac21090c6b5c812bf0ad1f74c1ea214f47208") || !task7StringField(row, activityIndex, "client_endpoint_id", "endpoint.test") || !task7IntField(row, activityIndex, "client_endpoint_revision", 1) || !task7StringField(row, activityIndex, "protocol_plan_id", "plan.test") || !task7IntField(row, activityIndex, "protocol_plan_revision", 1) || !task7StringField(row, activityIndex, "route_id", "route.test") || !task7IntField(row, activityIndex, "route_revision", 1) || !task7StringField(row, activityIndex, "source_kind", "manual_proxy") || !task7StringField(row, activityIndex, "source_display_name", "Task7 full") || !task7StringField(row, activityIndex, "source_recognition", "configured") || !task7StringField(row, activityIndex, "manual_capture_id", want.Capture) || !task7StringField(row, activityIndex, "conversation_projection_id", "exchange:"+id) {
			return fmt.Errorf("activity policy or ownership mismatch for %s", id)
		}
		if kind != "exchange.started" && kind != "exchange.completed" {
			return fmt.Errorf("unexpected activity kind %q", kind)
		}
		key := id + "\x00" + kind
		if _, exists := activityByKey[key]; exists {
			return fmt.Errorf("duplicate activity %s", key)
		}
		activityByKey[key] = row
		if kind == "exchange.started" {
			if !task7StringField(row, activityIndex, "status", "pending") || !task7StringField(row, activityIndex, "account_id", "") || !task7IntField(row, activityIndex, "account_revision", 0) || !task7IntField(row, activityIndex, "credential_epoch", 0) || !task7StringField(row, activityIndex, "conversation_kind", "pending_exchange") {
				return fmt.Errorf("started activity mismatch for %s", id)
			}
		} else if !task7StringField(row, activityIndex, "status", "succeeded") || !task7StringField(row, activityIndex, "account_id", "task7-account") || !task7IntField(row, activityIndex, "account_revision", 1) || !task7IntField(row, activityIndex, "credential_epoch", 1) || !task7StringField(row, activityIndex, "conversation_kind", "isolated_exchange") || !task7StringField(row, activityIndex, "conversation_evidence", "exchange_boundary") {
			return fmt.Errorf("completed activity mismatch for %s", id)
		}
	}
	for _, id := range want.Exchanges {
		for _, kind := range []string{"exchange.started", "exchange.completed"} {
			if _, ok := activityByKey[id+"\x00"+kind]; !ok {
				return fmt.Errorf("missing %s activity for %s", kind, id)
			}
		}
	}

	egressColumns := task7SnapshotColumns(before["runtime_egress_attempts"])
	egressIndex := task7ColumnIndex(egressColumns)
	egressByExchange := map[string]string{}
	for _, row := range added["runtime_egress_attempts"] {
		if err := task7RequireColumns(row, egressIndex, "egress"); err != nil {
			return err
		}
		exchangeID, ok := task7SnapshotString(row[egressIndex["parent_exchange_id"]])
		if !ok || !task7ExpectedExchange(want.Exchanges, exchangeID) {
			return fmt.Errorf("egress has unexpected exchange %q", exchangeID)
		}
		if _, exists := egressByExchange[exchangeID]; exists {
			return fmt.Errorf("duplicate egress for %s", exchangeID)
		}
		for _, field := range []string{"purpose", "payload_class", "parent_kind", "caller_kind", "target_origin", "policy_id", "policy_authority", "rule_id", "proxy_id", "account_id", "outcome", "error_class"} {
			wantValue := map[string]string{"purpose": "provider_attempt", "payload_class": "client_semantic", "parent_kind": "upstream_attempt", "caller_kind": "core", "target_origin": "https://chatgpt.com", "policy_id": "task7-full", "policy_authority": "environment", "rule_id": "route.test", "proxy_id": "profile.direct", "account_id": "task7-account", "outcome": "completed", "error_class": ""}[field]
			if !task7StringField(row, egressIndex, field, wantValue) {
				return fmt.Errorf("egress %s field %s mismatch", exchangeID, field)
			}
		}
		for _, field := range []string{"policy_revision", "proxy_revision", "account_settings_revision", "reused_transport"} {
			if !task7IntField(row, egressIndex, field, 1) && field != "reused_transport" {
				return fmt.Errorf("egress %s field %s mismatch", exchangeID, field)
			}
		}
		if !task7IntField(row, egressIndex, "reused_transport", 0) {
			return fmt.Errorf("egress %s transport reuse mismatch", exchangeID)
		}
		connection, ok := task7SnapshotString(row[egressIndex["connection_id"]])
		if !ok || connection == "" {
			return fmt.Errorf("egress %s has no connection", exchangeID)
		}
		egressByExchange[exchangeID] = connection
	}

	usageColumns := task7SnapshotColumns(before["runtime_usage_observations"])
	usageIndex := task7ColumnIndex(usageColumns)
	usageByExchange := map[string]bool{}
	for _, row := range added["runtime_usage_observations"] {
		if err := task7RequireColumns(row, usageIndex, "usage"); err != nil {
			return err
		}
		exchangeID, ok := task7SnapshotString(row[usageIndex["exchange_id"]])
		if !ok || !task7ExpectedExchange(want.Exchanges, exchangeID) || usageByExchange[exchangeID] {
			return fmt.Errorf("usage has unexpected or duplicate exchange %q", exchangeID)
		}
		usageByExchange[exchangeID] = true
		if !task7StringField(row, usageIndex, "capture_run_id", "") || !task7StringField(row, usageIndex, "manual_capture_id", want.Capture) || !task7StringField(row, usageIndex, "runtime_user_id", "") || !task7StringField(row, usageIndex, "status", "succeeded") || !task7StringField(row, usageIndex, "source", "manual") || !task7StringField(row, usageIndex, "environment_id", "task7-full") || !task7StringField(row, usageIndex, "account_id", "task7-account") || !task7StringField(row, usageIndex, "model_id", "task7") || !task7StringField(row, usageIndex, "caller_id", "") || !task7StringField(row, usageIndex, "project_id", "") || !task7StringField(row, usageIndex, "branch_id", "") || !task7StringField(row, usageIndex, "client", "") || !task7StringField(row, usageIndex, "session_id", "") {
			return fmt.Errorf("usage ownership/policy mismatch for %s", exchangeID)
		}
		if !task7SnapshotNil(row[usageIndex["input_tokens"]]) || !task7SnapshotNil(row[usageIndex["cache_read_tokens"]]) || !task7SnapshotNil(row[usageIndex["cache_write_tokens"]]) || !task7IntField(row, usageIndex, "output_tokens", 2) || !task7SnapshotNil(row[usageIndex["reasoning_tokens"]]) {
			return fmt.Errorf("usage whole-value mismatch for %s", exchangeID)
		}
		observation, ok := task7SnapshotString(row[usageIndex["observation_json"]])
		if !ok {
			return fmt.Errorf("usage observation missing for %s", exchangeID)
		}
		var value struct {
			ExchangeID, ManualCaptureID, Source, Status, EnvironmentID, AccountID, RequestedModel, UpstreamModel string
			Usage                                                                                                struct {
				InputUncached, CacheWrite, CacheRead, Reasoning struct {
					Tokens int64
					Known  bool
					Source string
				}
				Output struct {
					Tokens int64
					Known  bool
					Source string
				}
			}
		}
		if json.Unmarshal([]byte(observation), &value) != nil || value.ExchangeID != exchangeID || value.ManualCaptureID != want.Capture || value.Source != "manual" || value.Status != "succeeded" || value.EnvironmentID != "task7-full" || value.AccountID != "task7-account" || value.RequestedModel != "task7" || value.UpstreamModel != "task7" || value.Usage.Output != struct {
			Tokens int64
			Known  bool
			Source string
		}{2, true, "openai-responses"} || value.Usage.InputUncached.Known || value.Usage.CacheWrite.Known || value.Usage.CacheRead.Known || value.Usage.Reasoning.Known {
			return fmt.Errorf("usage observation value mismatch for %s", exchangeID)
		}
	}
	for _, id := range want.Exchanges {
		if !usageByExchange[id] || egressByExchange[id] == "" {
			return fmt.Errorf("missing usage or egress for %s", id)
		}
	}

	connectionColumns := task7SnapshotColumns(before["runtime_connection_events"])
	connectionIndex := task7ColumnIndex(connectionColumns)
	connectionPhases := map[string]map[string]bool{}
	for _, row := range added["runtime_connection_events"] {
		if err := task7RequireColumns(row, connectionIndex, "connection"); err != nil {
			return err
		}
		connection, ok := task7SnapshotString(row[connectionIndex["connection_id"]])
		if !ok || connection == "" {
			return fmt.Errorf("connection row has no identity")
		}
		if !task7ExpectedConnection(egressByExchange, connection) {
			return fmt.Errorf("unmatched connection %s", connection)
		}
		phase, ok := task7SnapshotString(row[connectionIndex["phase"]])
		if !ok || !map[string]bool{"attempted": true, "decided": true, "connected": true, "closed": true}[phase] {
			return fmt.Errorf("unexpected connection phase %q", phase)
		}
		if !task7IntField(row, connectionIndex, "port", 443) || !task7StringField(row, connectionIndex, "requested_host", "chatgpt.com") {
			return fmt.Errorf("connection %s endpoint mismatch", connection)
		}
		if phase == "attempted" {
			if !task7StringField(row, connectionIndex, "source_confidence", "unknown") || !task7StringField(row, connectionIndex, "decryption", "none") {
				return fmt.Errorf("connection %s attempted policy mismatch", connection)
			}
		} else {
			if !task7StringField(row, connectionIndex, "source_label", "Task7 full") || !task7StringField(row, connectionIndex, "source_confidence", "configured") || !task7StringField(row, connectionIndex, "environment_id", "task7-full") || !task7IntField(row, connectionIndex, "environment_revision", 1) || !task7StringField(row, connectionIndex, "environment_name", "Task7 full") || !task7StringField(row, connectionIndex, "decision", "allow") || !task7StringField(row, connectionIndex, "rule_id", "task7.chatgpt") || !task7StringField(row, connectionIndex, "egress_scope", "environment") || !task7StringField(row, connectionIndex, "egress_source", "environment_default") || !task7IntField(row, connectionIndex, "egress_policy_revision", 1) || !task7StringField(row, connectionIndex, "decryption", "mitm") {
				return fmt.Errorf("connection %s policy mismatch", connection)
			}
		}
		if phase == "closed" && !task7StringField(row, connectionIndex, "outcome", "completed") {
			return fmt.Errorf("connection %s close outcome mismatch", connection)
		}
		if phase != "closed" && !task7StringField(row, connectionIndex, "outcome", "") {
			return fmt.Errorf("connection %s nonterminal outcome mismatch", connection)
		}
		if connectionPhases[connection] == nil {
			connectionPhases[connection] = map[string]bool{}
		}
		if connectionPhases[connection][phase] {
			return fmt.Errorf("duplicate connection phase %s/%s", connection, phase)
		}
		connectionPhases[connection][phase] = true
	}
	for _, connection := range egressByExchange {
		for _, phase := range []string{"attempted", "decided", "connected", "closed"} {
			if !connectionPhases[connection][phase] {
				return fmt.Errorf("missing connection phase %s/%s", connection, phase)
			}
		}
	}

	writerColumns := task7SnapshotColumns(before["runtime_raw_evidence_writer_sessions"])
	writerIndex := task7ColumnIndex(writerColumns)
	writerIDs := map[string]bool{}
	for _, row := range added["runtime_raw_evidence_writer_sessions"] {
		if err := task7RequireColumns(row, writerIndex, "writer"); err != nil {
			return err
		}
		id, ok := task7SnapshotString(row[writerIndex["writer_id"]])
		if !ok || id == "" || writerIDs[id] {
			return fmt.Errorf("writer identity is missing or duplicated")
		}
		writerIDs[id] = true
		if !task7IntField(row, writerIndex, "maximum_unflushed_ms", 100) || !task7StringField(row, writerIndex, "state", "closed") {
			return fmt.Errorf("writer %s state mismatch", id)
		}
		started, okStarted := task7SnapshotIntValue(row[writerIndex["started_at_unix_ms"]])
		ended, okEnded := task7SnapshotIntValue(row[writerIndex["ended_at_unix_ms"]])
		if !okStarted || !okEnded || started <= 0 || ended < started {
			return fmt.Errorf("writer %s timestamps mismatch", id)
		}
	}
	return nil
}

func task7ContainsRow(rows []string, row string) bool {
	for _, candidate := range rows[1:] {
		if candidate == row {
			return true
		}
	}
	return false
}

func task7SnapshotColumns(rows []string) []string {
	if len(rows) == 0 {
		return nil
	}
	var columns []string
	_ = json.Unmarshal([]byte(rows[0]), &columns)
	return columns
}

func task7ColumnIndex(columns []string) map[string]int {
	result := make(map[string]int, len(columns))
	for i, column := range columns {
		result[column] = i
	}
	return result
}

func task7AddedSnapshotRows(before, after []string) ([][]string, error) {
	if !reflect.DeepEqual(task7SnapshotColumns(before), task7SnapshotColumns(after)) {
		return nil, fmt.Errorf("column schema changed")
	}
	counts := map[string]int{}
	for _, row := range before[1:] {
		counts[row]++
	}
	var added [][]string
	for _, row := range after[1:] {
		if counts[row] > 0 {
			counts[row]--
			continue
		}
		var cells []string
		if json.Unmarshal([]byte(row), &cells) != nil {
			return nil, fmt.Errorf("invalid row encoding")
		}
		added = append(added, cells)
	}
	return added, nil
}

func task7RequireColumns(row []string, columns map[string]int, table string) error {
	for name, index := range columns {
		if index >= len(row) {
			return fmt.Errorf("%s row missing column %s", table, name)
		}
	}
	return nil
}

func task7SnapshotString(value string) (string, bool) {
	if !strings.HasPrefix(value, "string:") {
		return "", false
	}
	var result string
	if json.Unmarshal([]byte(strings.TrimPrefix(value, "string:")), &result) != nil {
		return "", false
	}
	return result, true
}

func task7SnapshotIntValue(value string) (int64, bool) {
	if !strings.HasPrefix(value, "int64:") {
		return 0, false
	}
	result, err := strconv.ParseInt(strings.TrimPrefix(value, "int64:"), 10, 64)
	return result, err == nil
}

func task7SnapshotNil(value string) bool { return value == "<nil>:null" }

func task7StringField(row []string, index map[string]int, field, expected string) bool {
	value, ok := task7SnapshotString(row[index[field]])
	return ok && value == expected
}

func task7IntField(row []string, index map[string]int, field string, expected int64) bool {
	value, ok := task7SnapshotIntValue(row[index[field]])
	return ok && value == expected
}

func task7ExpectedExchange(exchanges []string, id string) bool {
	return len(exchanges) == 2 && (exchanges[0] == id || exchanges[1] == id)
}

func task7ExpectedConnection(egress map[string]string, connection string) bool {
	for _, expected := range egress {
		if expected == connection {
			return true
		}
	}
	return false
}
func TestTask7TrafficDeltaVerifierControls(t *testing.T) {
	root := os.Getenv("TASK7_RECHECK_ROOT")
	if root == "" {
		t.Skip("requires preserved Task7 artifacts")
	}
	var receipt task7Receipt
	task7Load(t, filepath.Join(root, "old-receipt.json"), &receipt)
	var deltas map[string]struct{ Before, After []string }
	task7Load(t, filepath.Join(root, "candidate-row-deltas.json"), &deltas)
	after := map[string][]string{}
	for table, rows := range receipt.Snapshot {
		after[table] = rows
	}
	for table, delta := range deltas {
		after[table] = delta.After
	}
	var ids []string
	task7Load(t, filepath.Join(root, "new-exchanges.json"), &ids)
	want := task7TrafficExpectation{Exchanges: ids, Capture: receipt.Captures[0]}
	if err := task7VerifyTrafficAdditions(receipt.Snapshot, after, want); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"extra_usage", "missing_usage", "extra_audit", "missing_audit", "wrong_usage", "wrong_owner", "extra_writer", "missing_connection"} {
		t.Run(variant, func(t *testing.T) {
			copy := map[string][]string{}
			for table, rows := range after {
				copy[table] = append([]string(nil), rows...)
			}
			table := "runtime_usage_observations"
			if strings.Contains(variant, "audit") {
				table = "runtime_egress_attempts"
			}
			if variant == "extra_writer" {
				table = "runtime_raw_evidence_writer_sessions"
			}
			if variant == "missing_connection" {
				table = "runtime_connection_events"
			}
			old := map[string]bool{}
			for _, row := range receipt.Snapshot[table] {
				old[row] = true
			}
			index := -1
			for i, row := range copy[table] {
				if !old[row] {
					index = i
					break
				}
			}
			if index < 0 {
				t.Fatal("no real added control row")
			}
			if strings.HasPrefix(variant, "missing") {
				copy[table] = append(copy[table][:index], copy[table][index+1:]...)
			} else if strings.HasPrefix(variant, "extra") {
				copy[table] = append(copy[table], copy[table][index])
			} else {
				row := copy[table][index]
				if variant == "wrong_owner" {
					row = strings.ReplaceAll(row, receipt.Captures[0], "manual.unrelated")
				} else {
					row = strings.ReplaceAll(row, `int64:2`, `int64:9`)
				}
				copy[table][index] = row
			}
			if err := task7VerifyTrafficAdditions(receipt.Snapshot, copy, want); err == nil {
				t.Fatal("corrupted traffic delta accepted")
			}
		})
	}
}
