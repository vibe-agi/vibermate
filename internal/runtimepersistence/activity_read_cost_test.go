package runtimepersistence

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
)

// Keep the old whole-archive window query as an independent correctness and
// performance reference, including completions whose projection differs from
// their earlier pending record.
func rankedPageReference(request activity.PageRequest) (string, []any) {
	query, args := exchangePageQuery(request)
	columns, rest, _ := strings.Cut(query, "FROM runtime_activities AS candidate")
	filters, _, _ := strings.Cut(rest, " AND NOT EXISTS")
	return `WITH ranked AS (
	 SELECT *, ROW_NUMBER() OVER (PARTITION BY subject_id
	 ORDER BY CASE kind WHEN 'exchange.completed' THEN 0 ELSE 1 END, sequence DESC) AS rank
	 FROM runtime_activities WHERE kind IN ('exchange.started', 'exchange.completed')) ` +
		columns + " FROM ranked " + filters + " AND rank = 1 ORDER BY sequence DESC LIMIT ?", args
}

func activityReadFixture(t testing.TB, size int) *Store {
	t.Helper()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { shutdownTestStore(t, store) })
	_, err := store.database.Exec(`WITH RECURSIVE n(x) AS (
	 VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < ?)
	 INSERT INTO runtime_activities(activity_id, occurred_at_unix_ms, kind, subject_id,
	 status, capture_run_id, conversation_projection_id, conversation_kind, conversation_evidence,
	 environment_id, environment_revision, environment_digest, client_endpoint_id,
	 client_endpoint_revision, protocol_plan_id, protocol_plan_revision)
	 SELECT 'a-'||x, 1000+x,
	 CASE WHEN x%3=0 THEN 'exchange.completed' ELSE 'exchange.started' END,
	 'exchange-'||(x/3), 'succeeded', 'run-'||(x/30),
	 CASE WHEN x%3=0 THEN 'session-'||(x/300) ELSE 'pending-'||(x/3) END,
	 'main', 'explicit_session', 'env', 1, ?, 'endpoint', 1, 'protocol', 1 FROM n`, size, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func readSequences(t testing.TB, store *Store, query string, args []any) []int64 {
	t.Helper()
	rows, err := store.database.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var sequences []int64
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, values[0].(int64))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return sequences
}

func TestExchangePageIndexedQueryPreservesLifecycleAndPagination(t *testing.T) {
	store := activityReadFixture(t, 1200)
	for _, request := range []activity.PageRequest{
		{Limit: 100}, {Limit: 7, BeforeSequence: 650},
		{Limit: 100, CaptureRunID: "run-4"},
		{Limit: 100, ConversationProjectionID: "session-2"},
		{Limit: 100, ConversationProjectionID: "pending-200"},
		{Limit: 100, BeforeSequence: 602, ConversationProjectionID: "pending-200"},
		{Limit: 100, OccurredAtOrAfter: time.UnixMilli(1100), OccurredBefore: time.UnixMilli(1131)},
	} {
		query, args := exchangePageQuery(request)
		old, oldArgs := rankedPageReference(request)
		if got, want := readSequences(t, store, query, args), readSequences(t, store, old, oldArgs); !reflect.DeepEqual(got, want) {
			t.Fatalf("%+v: got %v want %v", request, got, want)
		}
	}
	query, args := exchangePageQuery(activity.PageRequest{Limit: 100, ConversationProjectionID: "session-2"})
	rows, err := store.database.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "runtime_activities_exchange_conversation_latest") || !strings.Contains(plan, "runtime_activities_exchange_subject") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("expected indexed scope + subject probes without archive sorting:\n%s", plan)
	}
}

func BenchmarkConversationPageRead(b *testing.B) {
	for _, size := range []int{3000, 30000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			store := activityReadFixture(b, size)
			request := activity.PageRequest{Limit: 100, ConversationProjectionID: "session-2"}
			for _, old := range []bool{true, false} {
				name := "indexed"
				query, args := exchangePageQuery(request)
				if old {
					name = "whole_archive"
					query, args = rankedPageReference(request)
				}
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						readSequences(b, store, query, args)
					}
				})
			}
		})
	}
}
