package runtimepersistence

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
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

func TestExchangePageCanSkipOnlyCompletedLocalIdentities(t *testing.T) {
	store := activityReadFixture(t, 120)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	for _, entry := range []struct {
		id, source string
	}{
		{"exchange-10", agentconversation.ClientIdentitySourceLocalState},
		{"exchange-11", agentconversation.ClientIdentitySourceProtocolEvidence},
	} {
		if err := store.ConversationIdentityRepository().PutConversationIdentity(ctx, entry.id, agentconversation.ClientIdentity{
			Client: "codex", SessionID: "session-1", SessionResumable: true,
			ProviderResponseID: "response-" + entry.id, Source: entry.source,
			Confidence: "exact", ObservedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	allQuery, allArgs := exchangePageQuery(activity.PageRequest{
		Limit: 100, CaptureRunID: "run-1",
	})
	filteredQuery, filteredArgs := exchangePageQuery(activity.PageRequest{
		Limit: 100, CaptureRunID: "run-1", WithoutLocalConversationIdentity: true,
	})
	all := readSequences(t, store, allQuery, allArgs)
	filtered := readSequences(t, store, filteredQuery, filteredArgs)
	sequence := func(id string) int64 {
		var value int64
		if err := store.database.QueryRowContext(ctx,
			`SELECT sequence FROM runtime_activities WHERE subject_id = ? AND kind = 'exchange.completed'`, id,
		).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	local, protocol := sequence("exchange-10"), sequence("exchange-11")
	if !slices.Contains(all, local) || !slices.Contains(all, protocol) ||
		slices.Contains(filtered, local) || !slices.Contains(filtered, protocol) {
		t.Fatalf("identity candidate filter changed unresolved or protocol rows: all=%v filtered=%v", all, filtered)
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

// Opt-in wall-clock samples complement the existing allocation benchmarks.
// Only synthetic data is used. These are storage timings, not UI latency.
func TestEvidenceReadPerformanceBaseline(t *testing.T) {
	if os.Getenv("VIBERMATE_PERFORMANCE") != "1" {
		t.Skip("set VIBERMATE_PERFORMANCE=1 to collect storage latency samples")
	}
	for _, scenario := range []struct{ activities, messages int }{{3000, 1}, {3000, 1000}, {30000, 1000}} {
		t.Run(fmt.Sprintf("activities=%d/messages=%d", scenario.activities, scenario.messages), func(t *testing.T) {
			store := activityReadFixture(t, scenario.activities)
			ctx := context.Background()
			now := time.Now().UTC()
			record := contentRecordFixture(t, "performance-content", now)
			record.Request.Messages = make([]exchangecontent.Message, scenario.messages)
			for i := range scenario.messages {
				record.Request.Messages[i] = exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{{
					Kind: "text", Availability: exchangecontent.AvailabilityRecorded,
					Text: fmt.Sprintf("Synthetic %d %s", i, strings.Repeat("context ", 128)),
				}}}
			}
			writeStart := time.Now()
			if err := store.ExchangeContentRepository().Put(ctx, record); err != nil {
				t.Fatal(err)
			}
			contentWrite := time.Since(writeStart)
			writer, err := rawevidence.Open(ctx, rawevidence.Options{
				Repository: store.RawEvidenceRepository(), Clock: rawevidence.SystemClock{},
				Random: rand.Reader, Config: rawevidence.DefaultConfig(),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := writer.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			observation := rawevidence.Observation{
				Context: rawevidence.Context{
					ScopeKind: rawevidence.ScopeManagedRun, ScopeID: "performance-run",
					ExchangeID: "performance-exchange", ConnectionID: "performance-connection",
					EnvironmentID: "performance-policy", EnvironmentRevision: 1,
					ClientEndpointID: "client", ClientEndpointRevision: 1,
					ProtocolPlanID: "plan", ProtocolPlanRevision: 1,
					Recording: rawevidence.RecordingFull, RetentionDays: 7,
				},
				Layer: rawevidence.LayerClientDownstream, ObservedAt: now,
				StatusCode: 200, Scheme: "https", Authority: "synthetic.example", Path: "/responses",
				ContentType: "application/json", Representation: "http_message",
				Body: []byte(`{"output":"synthetic"}`), Complete: true,
			}
			for _, concurrent := range []bool{false, true} {
				readCtx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				if concurrent {
					go func() {
						ticker := time.NewTicker(time.Millisecond)
						defer ticker.Stop()
						for {
							select {
							case <-readCtx.Done():
								done <- nil
								return
							case <-ticker.C:
								if _, err := writer.Observe(readCtx, observation); err != nil {
									if readCtx.Err() != nil {
										err = nil
									}
									done <- err
									return
								}
							}
						}
					}()
				} else {
					done <- nil
				}
				// Cleanup also joins the producer if a correctness assertion fails.
				joined := false
				t.Cleanup(func() {
					cancel()
					if !joined {
						<-done
					}
				})
				before := store.database.Stats()
				samples := map[string][]int64{}
				var peakHeap uint64
				var peakQueueBytes int64
				query, args := exchangePageQuery(activity.PageRequest{Limit: 100, ConversationProjectionID: "session-2"})
				for range 25 {
					for _, step := range []struct {
						name string
						run  func() error
					}{
						{"message_list", func() error {
							if len(readSequences(t, store, query, args)) == 0 {
								return fmt.Errorf("empty synthetic page")
							}
							return nil
						}},
						{"identity_metadata", func() error {
							_, err := store.ExchangeContentRepository().GetConversationEvidence(ctx, record.ExchangeID, now)
							return err
						}},
						{"full_projection", func() error {
							_, err := store.ExchangeContentRepository().GetProjection(ctx, record.ExchangeID, now, exchangecontent.RequestViewFull)
							return err
						}},
					} {
						start := time.Now()
						if err := step.run(); err != nil {
							t.Fatal(err)
						}
						samples[step.name] = append(samples[step.name], time.Since(start).Microseconds())
					}
					var memory runtime.MemStats
					runtime.ReadMemStats(&memory)
					peakHeap = max(peakHeap, memory.HeapAlloc)
					peakQueueBytes = max(peakQueueBytes, writer.Statistics().QueueBytes)
				}
				cancel()
				writerErr := <-done
				joined = true
				if writerErr != nil {
					t.Fatal(writerErr)
				}
				flushStart := time.Now()
				if err := writer.FlushScope(ctx, rawevidence.ScopeManagedRun, "performance-run"); err != nil {
					t.Fatal(err)
				}
				flushDuration := time.Since(flushStart)
				after := store.database.Stats()
				latencies := map[string]any{}
				for name, values := range samples {
					first := values[0]
					warm := slices.Clone(values[1:])
					slices.Sort(warm)
					latencies[name] = map[string]any{"firstReadUs": first, "warmP50Us": warm[11], "warmP95Us": warm[22], "samples": len(values)}
				}
				var databasePath string
				if err := store.database.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&databasePath); err != nil {
					t.Fatal(err)
				}
				walBytes := int64(0)
				if info, err := os.Stat(databasePath + "-wal"); err == nil {
					walBytes = info.Size()
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				report, err := json.Marshal(map[string]any{
					"scope": "synthetic-storage-only", "platform": runtime.GOOS + "/" + runtime.GOARCH, "go": runtime.Version(),
					"activities": scenario.activities, "messages": scenario.messages, "concurrentWriter": concurrent,
					"latencies": latencies, "contentWriteUs": contentWrite.Microseconds(), "rawFlushUs": flushDuration.Microseconds(),
					"poolWaitCount": after.WaitCount - before.WaitCount, "poolWaitUs": (after.WaitDuration - before.WaitDuration).Microseconds(),
					"sampledProcessHeapPeakBytes": peakHeap, "sampledQueuePeakBytes": peakQueueBytes, "walBytes": walBytes,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("EVIDENCE_STORAGE_BASELINE %s", report)
			}
		})
	}
}
