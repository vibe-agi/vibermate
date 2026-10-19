package runtimepersistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

type usageInspectionFixture struct {
	reader  *UsageInspectionReader
	current *ObservationStore
	control *ControlStore
	legacy  *sql.DB
	now     time.Time
	user    runtimeuser.UserID
}

func newUsageInspectionFixture(t *testing.T) *usageInspectionFixture {
	return newUsageInspectionFixtureRevision(t, 2)
}
func newUsageInspectionFixtureRevision(t *testing.T, revision int) *usageInspectionFixture {
	t.Helper()
	ctx := context.Background()
	options, current := inspectionSourcesFixture(t)
	if revision == 1 {
		path, db := extractionFixture(t, 1)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(options.Root, "archives/legacy/runtime.db"), data, 0600); err != nil {
			t.Fatal(err)
		}
		options.Sources[0].Database.Revision = 1
	}
	control := inspectionControlFixture(t)
	legacy, err := sql.Open("sqlite", filepath.Join(options.Root, "archives/legacy/runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { legacy.Close() })
	nativeExec(t, legacy, `DELETE FROM runtime_usage_retention_caps`)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	nativeExec(t, control.store.database, `UPDATE runtime_usage_policy SET revision=3,collecting_since_unix_ms=?`, now.AddDate(0, -1, 0).UnixMilli())
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','usage-owner',1,0),('managed_run','usage-other',1,0)`)
	seedCompleteInspectionMirror(t, current, control)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(ctx) })
	return &usageInspectionFixture{reader: NewUsageInspectionReader(catalog, control), current: current, control: control, legacy: legacy, now: now, user: runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA")}
}
func (f *usageInspectionFixture) value(id, profile string) runtimeusage.Observation {
	return runtimeusage.Observation{ExchangeID: id, CaptureRunID: "usage-owner", UserID: f.user, Source: "member", StartedAt: f.now.Add(-time.Hour), OccurredAt: f.now.Add(-time.Hour), Status: activity.StatusSucceeded, EnvironmentID: profile, EnvironmentRevision: 1, EnvironmentName: "Frozen " + profile, UpstreamModel: "model", Usage: protocolcore.Usage{InputUncached: protocolcore.UsageValue{Known: true, Tokens: 10, Source: "fixture"}, Output: protocolcore.UsageValue{Known: true, Tokens: 0, Source: "fixture"}}, Attribution: &runtimeusage.Attribution{CallerID: string(f.user), CallerLabel: "Frozen owner", CallerKind: "member"}}
}
func (f *usageInspectionFixture) put(t *testing.T, current bool, v runtimeusage.Observation, expiry time.Time, policy int64) {
	t.Helper()
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	if current {
		epoch, _ := recording.NewEpoch(1)
		stamp, err := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: v.CaptureRunID}, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.current.PutCollectedUsage(context.Background(), runtimeusage.CollectedObservation{Value: v, PolicyRevision: policy, ExpiresAt: expiry, Stamp: stamp}); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.legacy.Exec(`INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json) VALUES(?,?,?,?,?,?,?)`, v.ExchangeID, v.CaptureRunID, v.ManualCaptureID, v.UserID, v.OccurredAt.UnixMilli(), expiry.UnixMilli(), string(data)); err != nil {
		t.Fatal(err)
	}
}
func (f *usageInspectionFixture) query(t *testing.T) runtimeusage.AggregationQuery {
	t.Helper()
	period, err := runtimeusage.NewQuery("2026-10-01", "2026-10-20", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	return runtimeusage.AggregationQuery{Period: period}
}
func TestUsageInspectionRanksGloballyAndKeepsAuthenticatedOwner(t *testing.T) {
	f := newUsageInspectionFixture(t)
	ctx := context.Background()
	for _, source := range []struct {
		current bool
		profile string
		count   int
	}{{false, "A", 5}, {false, "C", 4}, {true, "B", 5}, {true, "C", 4}} {
		for i := 0; i < source.count; i++ {
			v := f.value(fmt.Sprintf("%v-%s-%d", source.current, source.profile, i), source.profile)
			f.put(t, source.current, v, f.now.Add(30*24*time.Hour), 2)
		}
	}
	// Same logical Exchange exists in both sources with overlapping local sequences.
	f.put(t, false, f.value("true-C-0", "C"), f.now.Add(30*24*time.Hour), 0)
	for i := 0; i < 20; i++ {
		v := f.value(fmt.Sprintf("other-%d", i), "other")
		v.UserID = "user.BBBBBBBBBBBBBBBBBBBBBBBBBBA"
		v.CaptureRunID = "usage-other"
		v.Attribution = &runtimeusage.Attribution{CallerID: string(v.UserID), CallerLabel: "Other owner", CallerKind: "member"}
		f.put(t, true, v, f.now.Add(30*24*time.Hour), 2)
	}
	query := f.query(t)
	query.Dimension = "profile"
	query.Limit = 1
	calls := 0
	result, err := f.reader.ScanUsage(ctx, query, f.user, f.now, func(b runtimeusage.UsageBucket) error { calls += b.Calls; return nil })
	if err != nil || len(result.Groups) != 1 || result.Groups[0].ID != "C" || result.Groups[0].Label != "Frozen C" || result.Groups[0].AgentAPICalls != 8 || calls != 8 || result.NextCursor == "" {
		t.Fatalf("global owner-scoped top group lost: %+v calls=%d err=%v", result, calls, err)
	}
	query.Cursor = result.NextCursor
	query.Snapshot = result.Snapshot
	calls = 0
	next, err := f.reader.ScanUsage(ctx, query, f.user, f.now, func(b runtimeusage.UsageBucket) error { calls += b.Calls; return nil })
	if err != nil || len(next.Groups) != 1 || next.Groups[0].ID != "A" || calls != 5 {
		t.Fatalf("global group continuation: %+v %v", next, err)
	}
}

func (f *usageInspectionFixture) scan(t *testing.T) (runtimeusage.AggregationResult, int, error) {
	t.Helper()
	calls := 0
	result, err := f.reader.ScanUsage(context.Background(), f.query(t), f.user, f.now, func(b runtimeusage.UsageBucket) error { calls += b.Calls; return nil })
	return result, calls, err
}
func TestUsageInspectionIntersectsDuplicateRetentionCeilings(t *testing.T) {
	for _, mode := range []string{"stored", "materialized", "pending-range", "policy-revision"} {
		t.Run(mode, func(t *testing.T) {
			f := newUsageInspectionFixture(t)
			duplicate := f.value("duplicate", "profile")
			duplicate.OccurredAt = f.now.Add(-10 * 24 * time.Hour)
			duplicate.StartedAt = duplicate.OccurredAt
			expiry := f.now.Add(30 * 24 * time.Hour)
			legacyExpiry := expiry
			if mode == "stored" {
				legacyExpiry = f.now.Add(-time.Hour)
			}
			f.put(t, false, duplicate, legacyExpiry, 0)
			later := duplicate
			later.ExchangeID = "later-current"
			f.put(t, true, later, expiry, 3)
			f.put(t, true, duplicate, expiry, 3)
			switch mode {
			case "materialized":
				nativeExec(t, f.legacy, `INSERT INTO runtime_usage_retention_caps(retention_days,after_sequence,through_sequence) VALUES(7,0,1)`)
				tx, err := f.legacy.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = applyUsageRetention(context.Background(), tx, 10); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				var count int
				if err = f.legacy.QueryRow(`SELECT count(*) FROM runtime_usage_retention_caps`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("materialized source cap remains: %d %v", count, err)
				}
			case "pending-range":
				nativeExec(t, f.control.store.database, `INSERT INTO runtime_usage_legacy_retention_caps VALUES('legacy',7,0,1)`)
				// A distinct current Exchange at sequence 1 is not subject to legacy seq1.
				// The duplicate above IS subject to its own legacy copy's ceiling.
			case "policy-revision":
				nativeExec(t, f.control.store.database, `INSERT INTO runtime_usage_retention_rules VALUES(7,2)`)
			}
			seedCompleteInspectionMirror(t, f.current, f.control)
			_, calls, err := f.scan(t)
			if err != nil || calls != 1 {
				t.Fatalf("%s ceiling lost or applied to later current row: calls=%d err=%v", mode, calls, err)
			}
			var stored int64
			if err = f.current.store.database.QueryRow(`SELECT expires_at_unix_ms FROM runtime_usage_observations WHERE exchange_id='duplicate'`).Scan(&stored); err != nil || stored != expiry.UnixMilli() {
				t.Fatalf("reader rewrote current original expiry: %d %v", stored, err)
			}
		})
	}
}
func TestUsageInspectionDuplicateImmutableConflictsRefuseBeforeVisitor(t *testing.T) {
	for _, field := range []string{"metric", "owner", "time", "scope", "attribution"} {
		t.Run(field, func(t *testing.T) {
			f := newUsageInspectionFixture(t)
			a := f.value("duplicate", "profile")
			b := f.value("duplicate", "profile")
			switch field {
			case "metric":
				b.Usage.InputUncached.Tokens++
			case "owner":
				b.UserID = "user.BBBBBBBBBBBBBBBBBBBBBBBBBBA"
			case "time":
				b.OccurredAt = b.OccurredAt.Add(time.Minute)
			case "scope":
				b.CaptureRunID = "usage-other"
			case "attribution":
				b.Attribution.CallerLabel = "Changed"
			}
			f.put(t, false, a, f.now.Add(time.Hour), 0)
			f.put(t, true, b, f.now.Add(2*time.Hour), 3)
			_, calls, err := f.scan(t)
			if !errors.Is(err, ErrInspectionUnavailable) || calls != 0 {
				t.Fatalf("immutable conflict disclosed: calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestUsageInspectionMirrorAndOriginalScopes(t *testing.T) {
	for _, mode := range []string{"missing", "partial", "stale", "deleted", "unknown", "wrong-original"} {
		t.Run(mode, func(t *testing.T) {
			f := newUsageInspectionFixture(t)
			f.put(t, true, f.value("current", "profile"), f.now.Add(time.Hour), 3)
			switch mode {
			case "missing":
				nativeExec(t, f.current.store.database, `DELETE FROM inspection_mirror_active`)
			case "partial":
				nativeExec(t, f.current.store.database, `UPDATE inspection_mirror_generations SET complete=0`)
			case "stale":
				nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
			case "deleted":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='usage-owner'`)
				seedCompleteInspectionMirror(t, f.current, f.control)
			case "unknown":
				nativeExec(t, f.current.store.database, `UPDATE runtime_usage_observations SET recording_scope_kind='',recording_scope_id='',recording_capture_generation=0`)
			case "wrong-original":
				nativeExec(t, f.current.store.database, `UPDATE runtime_usage_observations SET recording_capture_generation=2`)
			}
			_, calls, err := f.scan(t)
			if mode == "missing" || mode == "partial" || mode == "stale" {
				if !errors.Is(err, ErrInspectionUnavailable) {
					t.Fatalf("invalid mirror accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("invisible original scope leaked %d calls", calls)
			}
		})
	}
}
func TestUsageInspectionSnapshotIgnoresUnrelatedOwnerInserts(t *testing.T) {
	f := newUsageInspectionFixture(t)
	f.put(t, true, f.value("current", "profile"), f.now.Add(time.Hour), 3)
	first, calls, err := f.scan(t)
	if err != nil || calls != 1 {
		t.Fatalf("first scan: %d %v", calls, err)
	}
	other := f.value("other", "other")
	other.UserID = "user.BBBBBBBBBBBBBBBBBBBBBBBBBBA"
	other.CaptureRunID = "usage-other"
	f.put(t, true, other, f.now.Add(time.Hour), 3)
	second, _, err := f.scan(t)
	if err != nil || second.Snapshot != first.Snapshot {
		t.Fatalf("unrelated insertion changed member snapshot: %+v %v", second, err)
	}
	f.put(t, true, f.value("second", "profile"), f.now.Add(time.Hour), 3)
	third, calls, err := f.scan(t)
	if err != nil || calls != 2 || third.Snapshot == first.Snapshot {
		t.Fatalf("member insertion not visible: %d %v", calls, err)
	}
	q := f.query(t)
	q.Snapshot = first.Snapshot
	if _, err = f.reader.ScanUsage(context.Background(), q, f.user, f.now, func(runtimeusage.UsageBucket) error { t.Fatal("stale snapshot visitor"); return nil }); !errors.Is(err, runtimeusage.ErrSnapshotChanged) {
		t.Fatalf("old snapshot accepted: %v", err)
	}
}

type usageInspectionChangingVisibility struct {
	*ControlStore
	beforeCheck func()
}

func (v *usageInspectionChangingVisibility) RecheckInspectionStamp(ctx context.Context, s recording.InspectionStamp) error {
	if v.beforeCheck != nil {
		fn := v.beforeCheck
		v.beforeCheck = nil
		fn()
	}
	return v.ControlStore.RecheckInspectionStamp(ctx, s)
}
func TestUsageInspectionRechecksBeforeAndAfterVisitor(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			f := newUsageInspectionFixture(t)
			f.put(t, true, f.value("current", "profile"), f.now.Add(time.Hour), 3)
			mutate := func() {
				nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
			}
			visibility := &usageInspectionChangingVisibility{ControlStore: f.control}
			f.reader.visibility = visibility
			if phase == "before" {
				visibility.beforeCheck = mutate
			}
			calls := 0
			_, err := f.reader.ScanUsage(context.Background(), f.query(t), f.user, f.now, func(b runtimeusage.UsageBucket) error {
				calls += b.Calls
				if phase == "after" {
					mutate()
				}
				return nil
			})
			if !errors.Is(err, ErrInspectionChanged) || (phase == "before" && calls != 0) {
				t.Fatalf("%s recheck bypassed: calls=%d err=%v", phase, calls, err)
			}
		})
	}
}
func TestUsageInspectionProjectorSeparatesPolicyAndReadPorts(t *testing.T) {
	f := newUsageInspectionFixture(t)
	f.put(t, true, f.value("current", "profile"), f.now.Add(time.Hour), 3)
	projector, err := runtimeusage.New(runtimeusage.Options{Reader: f.reader, Policies: f.control.UsagePolicyRepository(), Clock: runtimeUserTestClock{now: f.now}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := projector.ReportForUser(context.Background(), f.query(t), f.user)
	if err != nil || report.Total == nil || report.Total.AgentAPICalls != 1 {
		t.Fatalf("independent reader projection: %+v %v", report, err)
	}
	cached, err := projector.ReportForUser(context.Background(), f.query(t), f.user)
	if err != nil || cached.Snapshot != report.Snapshot || cached.Total.AgentAPICalls != 1 {
		t.Fatalf("independent reader cache: %+v %v", cached, err)
	}
	nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	if err = projector.CheckReport(context.Background(), cached); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("cached report lost original guard: %v", err)
	}
}

type usageInspectionHTTPSessions struct{}

func (usageInspectionHTTPSessions) IsOwner(runtimeuser.UserID) bool              { return false }
func (usageInspectionHTTPSessions) EnsureOwner(runtimeuser.UserID) (bool, error) { return true, nil }
func (usageInspectionHTTPSessions) RevokeUserSessions(runtimeuser.UserID)        {}

type usageInspectionHTTPProjector struct {
	*runtimeusage.Projector
	afterReport func()
}

func (p *usageInspectionHTTPProjector) Report(ctx context.Context, q runtimeusage.AggregationQuery) (runtimeusage.Report, error) {
	report, err := p.Projector.Report(ctx, q)
	if err == nil && p.afterReport != nil {
		p.afterReport()
	}
	return report, err
}
func TestUsageInspectionHTTPRechecksActualReaderAfterProjection(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f := newUsageInspectionFixture(t)
			f.put(t, true, f.value("current", "profile"), f.now.Add(time.Hour), 3)
			clock := runtimeUserTestClock{now: f.now}
			projector, err := runtimeusage.New(runtimeusage.Options{Reader: f.reader, Policies: f.control.UsagePolicyRepository(), Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			users, err := runtimeuser.New(runtimeuser.Options{Repository: f.control.RuntimeUserRepository(), Clock: clock, Random: rand.Reader, SessionLifetime: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			adapter := &usageInspectionHTTPProjector{Projector: projector}
			if changed {
				adapter.afterReport = func() {
					nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
				}
			}
			handler, err := servercontrol.NewRuntimeUsers(servercontrol.RuntimeUsersOptions{Users: users, Usage: adapter, Sessions: usageInspectionHTTPSessions{}})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, servercontrol.RuntimeUserUsagePath+"?from=2026-10-01&until=2026-10-20&timeZone=UTC", nil))
			expected := http.StatusOK
			if changed {
				expected = http.StatusServiceUnavailable
			}
			if response.Code != expected {
				t.Fatalf("HTTP published stale independent inspection: %d %s", response.Code, response.Body.String())
			}
			if !changed && (!strings.Contains(response.Body.String(), `"agentApiCalls":1`) || response.Header().Get("Cache-Control") != "no-store") {
				t.Fatalf("independent report response: %s", response.Body.String())
			}
			if strings.Contains(response.Body.String(), "InspectionToken") || strings.Contains(response.Body.String(), "SourceSet") {
				t.Fatal("internal token leaked")
			}
		})
	}
}

func TestUsageInspectionCanceledVisitorRetainsOwnerUntilReturn(t *testing.T) {
	f := newUsageInspectionFixture(t)
	for i := 0; i < 2; i++ {
		v := f.value(fmt.Sprint(i), "profile")
		v.Usage.InputUncached.Tokens += int64(i)
		f.put(t, true, v, f.now.Add(time.Hour), 3)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	calls := 0
	go func() {
		_, err := f.reader.ScanUsage(ctx, f.query(t), f.user, f.now, func(runtimeusage.UsageBucket) error {
			calls++
			if calls == 1 {
				close(entered)
				<-release
			}
			return nil
		})
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("visitor not entered")
	}
	var busy, log, done int
	if err := f.current.store.database.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &done); err != nil || busy != 0 {
		t.Fatalf("SQL held across visitor: busy=%d err=%v", busy, err)
	}
	cancel()
	drain, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := f.reader.catalog.Close(drain); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled callback released owner early: %v", err)
	}
	f.reader.catalog.mu.Lock()
	active := f.reader.catalog.active
	f.reader.catalog.mu.Unlock()
	if active != 1 {
		t.Fatalf("visitor ownership lost: %d", active)
	}
	unblock()
	if err := <-result; err == nil || calls != 1 {
		t.Fatalf("canceled visitor continued or succeeded: calls=%d err=%v", calls, err)
	}
}
func TestUsageInspectionBucketBudgetRefusesWithoutPartialVisitors(t *testing.T) {
	f := newUsageInspectionFixture(t)
	f.reader.catalog.timeout = inspectionStressBudget(20 * time.Second)
	f.put(t, true, f.value("seed", "profile"), f.now.Add(time.Hour), 3)
	nativeExec(t, f.current.store.database, `WITH RECURSIVE n(i) AS(VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<65537) INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json,policy_revision,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation) SELECT 'budget-'||i,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,json_set(observation_json,'$.exchangeId','budget-'||i,'$.usage.InputUncached.Tokens',i),policy_revision,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation FROM n CROSS JOIN runtime_usage_observations WHERE exchange_id='seed'`)
	started := time.Now()
	_, calls, err := f.scan(t)
	t.Logf("usage bucket scan budget=%s elapsed=%s calls=%d err=%v", f.reader.catalog.timeout, time.Since(started), calls, err)
	if !errors.Is(err, ErrInspectionSourceLimit) || calls != 0 {
		t.Fatalf("bucket budget returned partial output: calls=%d err=%v", calls, err)
	}
}

func TestUsageInspectionMalformedNumericBucketRefusesBeforeVisitor(t *testing.T) {
	f := newUsageInspectionFixture(t)
	f.put(t, true, f.value("current", "profile"), f.now.Add(time.Hour), 3)
	nativeExec(t, f.current.store.database, `PRAGMA ignore_check_constraints=ON;UPDATE runtime_usage_observations SET observation_json=json_set(observation_json,'$.usage.InputUncached.Tokens',-1);PRAGMA ignore_check_constraints=OFF`)
	_, calls, err := f.scan(t)
	if err == nil || calls != 0 {
		t.Fatalf("malformed aggregate accepted: calls=%d err=%v", calls, err)
	}
}

type usageInspectionClock struct{ now time.Time }

func (c *usageInspectionClock) Now() time.Time { return c.now }
func TestUsageInspectionReportExpiresDuringOuterEnrichment(t *testing.T) {
	f := newUsageInspectionFixture(t)
	f.put(t, true, f.value("current", "profile"), f.now.Add(time.Second), 3)
	clock := &usageInspectionClock{now: f.now}
	projector, err := runtimeusage.New(runtimeusage.Options{Reader: f.reader, Policies: f.control.UsagePolicyRepository(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	report, err := projector.ReportForUser(context.Background(), f.query(t), f.user)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	if err = projector.CheckReport(context.Background(), report); !errors.Is(err, runtimeusage.ErrSnapshotChanged) {
		t.Fatalf("expired retained report passed outer guard: %v", err)
	}
}

func (p *usageInspectionHTTPProjector) ReportForUser(ctx context.Context, q runtimeusage.AggregationQuery, user runtimeuser.UserID) (runtimeusage.Report, error) {
	report, err := p.Projector.ReportForUser(ctx, q, user)
	if err == nil && p.afterReport != nil {
		p.afterReport()
	}
	return report, err
}
func TestUsageInspectionAuthenticatedHTTPUsesActualMemberAndGuard(t *testing.T) {
	f := newUsageInspectionFixture(t)
	ctx := context.Background()
	clock := runtimeUserTestClock{now: f.now}
	users, err := runtimeuser.New(runtimeuser.Options{Repository: f.control.RuntimeUserRepository(), Clock: clock, Random: rand.Reader, SessionLifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := users.Create(ctx, runtimeuser.CreateCommand{Username: "owner", Password: []byte("owner password 123")})
	if err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(ctx, runtimeuser.CreateCommand{Username: "member", Password: []byte("member password 123")})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	authority, err := serveradmin.Open(serveradmin.Options{DataDirectory: dir, Clock: clock, Random: rand.Reader, SessionLifetime: time.Hour, LookupUser: users.User})
	if err != nil {
		t.Fatal(err)
	}
	key, err := serveradmin.ReadRecoveryKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = authority.ClaimOwner(key, owner.ID); err != nil {
		t.Fatal(err)
	}
	session, err := authority.LoginUser(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	f.user = member.ID
	f.put(t, true, f.value("member-observation", "member-profile"), f.now.Add(time.Hour), 3)
	own := f.value("owner-observation", "private-owner-profile")
	own.UserID = owner.ID
	own.CaptureRunID = "usage-other"
	f.put(t, true, own, f.now.Add(time.Hour), 3)
	projector, err := runtimeusage.New(runtimeusage.Options{Reader: f.reader, Policies: f.control.UsagePolicyRepository(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &usageInspectionHTTPProjector{Projector: projector}
	handler, err := servercontrol.NewWebSelf(servercontrol.WebSelfOptions{Sessions: authority, Usage: adapter})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, servercontrol.WebSelfUsagePath+"?from=2026-10-01&until=2026-10-20&timeZone=UTC&groupBy=profile&limit=10", nil)
		req.Header.Set("Authorization", "Bearer "+session.ReadToken.Value())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	response := request()
	if response.Code != 200 || !strings.Contains(response.Body.String(), "member-profile") || strings.Contains(response.Body.String(), "private-owner-profile") {
		t.Fatalf("actual authenticated source isolation: %d %s", response.Code, response.Body.String())
	}
	adapter.afterReport = func() {
		nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	}
	response = request()
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "member-profile") {
		t.Fatalf("self HTTP stale report: %d %s", response.Code, response.Body.String())
	}
}
func TestUsageInspectionSourceGenerationRecheckedAfterVisitor(t *testing.T) {
	f := newUsageInspectionFixture(t)
	f.put(t, true, f.value("current", "profile"), f.now.Add(time.Hour), 3)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	result := make(chan error, 1)
	go func() {
		_, err := f.reader.ScanUsage(context.Background(), f.query(t), f.user, f.now, func(runtimeusage.UsageBucket) error { close(entered); <-release; return nil })
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("visitor not entered")
	}
	f.reader.catalog.mu.Lock()
	changed := f.reader.catalog.changed
	f.reader.catalog.mu.Unlock()
	type maintenanceResult struct {
		token *InspectionMaintenance
		err   error
	}
	drained := make(chan maintenanceResult, 1)
	go func() {
		token, err := f.reader.catalog.BeginMaintenance(context.Background(), 2)
		drained <- maintenanceResult{token, err}
	}()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("source generation not reserved")
	}
	select {
	case v := <-drained:
		if v.token != nil {
			v.token.Close()
		}
		t.Fatal("maintenance did not retain actual visitor owner")
	default:
	}
	unblock()
	if err := <-result; !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("old source generation reported success: %v", err)
	}
	v := <-drained
	if v.err != nil {
		t.Fatal(v.err)
	}
	v.token.Close()
}

func TestUsageInspectionFrozenV1AndCurrentDeduplicate(t *testing.T) {
	f := newUsageInspectionFixtureRevision(t, 1)
	v := f.value("both", "profile")
	f.put(t, false, v, f.now.Add(time.Hour), 0)
	f.put(t, true, v, f.now.Add(2*time.Hour), 3)
	_, calls, err := f.scan(t)
	if err != nil || calls != 1 {
		t.Fatalf("frozen v1/current observation: calls=%d err=%v", calls, err)
	}
}
