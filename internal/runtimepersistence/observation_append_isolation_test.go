package runtimepersistence

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

// The former desktophost persistent-trigger fixture corrupted the admitted
// schema, testing rejection of all recording instead of one append failure.
// These connection-local TEMP triggers fail the actual owner SQL while keeping
// the installed schema valid. Queue continuation is covered by productruntime's
// collected-usage/identity tests; host forwarding/restart retains its own test.
func TestTerminalObservationFailuresKeepIndependentRecords(t *testing.T) {
	for _, stage := range []string{"activity_start", "activity_terminal", "usage"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "observations.db")
			options := ObservationOptions{DatabasePath: path, LayoutID: "append-isolation", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second}
			store, err := OpenObservations(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			table, predicate := "runtime_activities", "NEW.kind='exchange.started'"
			if stage == "activity_terminal" {
				predicate = "NEW.kind='exchange.completed'"
			}
			if stage == "usage" {
				table, predicate = "runtime_usage_observations", "1"
			}
			if _, err := store.store.database.Exec(`CREATE TEMP TRIGGER reject_selected_append BEFORE INSERT ON main.` + table + ` WHEN ` + predicate + ` BEGIN SELECT RAISE(ABORT,'selected append failure'); END`); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			epoch, _ := recording.NewEpoch(1)
			stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "audit-a"}, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
			appendAll := func(suffix string, failing bool) {
				t.Helper()
				subject := "independent-" + suffix
				for _, kind := range []activity.Kind{activity.KindExchangeStarted, activity.KindExchangeCompleted} {
					record := inspectionActivity(string(kind)+suffix, subject, kind, now)
					_, err := store.ActivityRepository().(activity.ScopedRepository).AppendScoped(t.Context(), stamp.Scope(), record)
					wantFailure := failing && (stage == "activity_start" && kind == activity.KindExchangeStarted || stage == "activity_terminal" && kind == activity.KindExchangeCompleted)
					if wantFailure {
						if err == nil || !strings.Contains(err.Error(), "selected append failure") || errors.Is(err, recording.ErrObservationDiscarded) {
							t.Fatalf("selected %s SQL failure not exercised: %v", kind, err)
						}
					} else if err != nil {
						t.Fatalf("independent %s append: %v", kind, err)
					}
				}
				usage := runtimeusage.CollectedObservation{Value: runtimeusage.Observation{ExchangeID: subject, CaptureRunID: "audit-a", Source: "local", StartedAt: now, OccurredAt: now, Status: activity.StatusSucceeded, EnvironmentID: "env", EnvironmentName: "Frozen environment", AccountID: "account", AccountName: "Frozen account", UpstreamModel: "claude-test", Usage: protocolcore.Usage{InputUncached: protocolcore.UsageValue{Known: true, Tokens: 4, Source: "fixture"}, Output: protocolcore.UsageValue{Known: true, Tokens: 2, Source: "fixture"}}}, PolicyRevision: 9, ExpiresAt: now.Add(24 * time.Hour), Stamp: stamp}
				err := store.PutCollectedUsage(t.Context(), usage)
				if failing && stage == "usage" {
					if err == nil || !strings.Contains(err.Error(), "selected append failure") || errors.Is(err, recording.ErrObservationDiscarded) {
						t.Fatalf("selected Usage SQL failure not exercised: %v", err)
					}
				} else if err != nil {
					t.Fatal("independent Usage append", err)
				}
				attempt := providerAttempt(t, "independent-egress-"+suffix)
				terminal, err := attempt.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: attempt.StartedAt().Add(time.Second), BytesIn: 23, BytesOut: 17})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.EgressAttemptRepository().(egressaudit.ScopedAttemptStore).PutScopedAttempt(t.Context(), stamp.Scope(), terminal); err != nil {
					t.Fatal("independent Egress append", err)
				}
			}
			assertRows := func(recovered bool) {
				t.Helper()
				extra := 0
				if recovered {
					extra = 1
				}
				for _, check := range []struct {
					table, where string
					want         int
				}{{"runtime_activities", "kind='exchange.started'", 1 + extra}, {"runtime_activities", "kind='exchange.completed'", 1 + extra}, {"runtime_usage_observations", "1", 1 + extra}, {"runtime_egress_attempts", "outcome='completed' AND bytes_in=23 AND bytes_out=17", 1 + extra}} {
					if stage == "activity_start" && check.where == "kind='exchange.started'" || stage == "activity_terminal" && check.where == "kind='exchange.completed'" || stage == "usage" && check.table == "runtime_usage_observations" {
						check.want--
					}
					var got int
					if err := store.store.database.QueryRow(`SELECT count(*) FROM ` + check.table + ` WHERE ` + check.where + ` AND recording_archive_epoch=1 AND recording_capture_generation=1`).Scan(&got); err != nil || got != check.want {
						t.Fatalf("independent %s/%s rows=%d want=%d err=%v", check.table, check.where, got, check.want, err)
					}
				}
				if stage != "usage" || recovered {
					var wire string
					if err := store.store.database.QueryRow(`SELECT observation_json FROM runtime_usage_observations LIMIT 1`).Scan(&wire); err != nil {
						t.Fatal(err)
					}
					var v runtimeusage.Observation
					if err := json.Unmarshal([]byte(wire), &v); err != nil || v.AccountID != "account" || v.UpstreamModel != "claude-test" || v.Usage.InputUncached.Tokens != 4 || v.Usage.Output.Tokens != 2 {
						t.Fatalf("frozen Usage changed: %+v %v", v, err)
					}
				}
			}
			appendAll("fault", true)
			assertRows(false)
			if _, err := store.store.database.Exec(`DROP TRIGGER temp.reject_selected_append`); err != nil {
				t.Fatal(err)
			}
			appendAll("recovered", false)
			assertRows(true)
			if err := store.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			store, err = OpenObservations(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			assertRows(true)
		})
	}
}
