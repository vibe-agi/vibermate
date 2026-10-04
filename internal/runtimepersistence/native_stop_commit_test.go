package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type nativeStopObserver struct {
	calls    int
	callback func(context.Context, captureidentity.Reference) error
}

func (o *nativeStopObserver) Revoke(ctx context.Context, c captureidentity.Reference, reason string) error {
	if reason != "capture_stopped" {
		return errors.New("unexpected stop reason")
	}
	o.calls++
	if o.callback != nil {
		return o.callback(ctx, c)
	}
	return nil
}

type stopReadUnavailable struct{ db *sql.DB }

type countedStopTerminal struct {
	commits, aborts   int
	onCommit, onAbort func()
}

func (terminal *countedStopTerminal) Commit() {
	terminal.commits++
	if terminal.onCommit != nil {
		terminal.onCommit()
	}
}
func (terminal *countedStopTerminal) Abort() {
	terminal.aborts++
	if terminal.onAbort != nil {
		terminal.onAbort()
	}
}

type countedManualStopBarrier struct {
	terminal *countedStopTerminal
	prepare  func(context.Context, string) error
}

func (barrier countedManualStopBarrier) PrepareManualCapture(ctx context.Context, id string) (manualcapture.TerminalEvidence, error) {
	if err := barrier.prepare(ctx, id); err != nil {
		return nil, err
	}
	return barrier.terminal, nil
}

type countedManagedStopBarrier struct {
	terminal *countedStopTerminal
	prepare  func(context.Context, string) error
}

func (barrier countedManagedStopBarrier) PrepareManagedRun(ctx context.Context, id string) (capturerun.TerminalEvidence, error) {
	if err := barrier.prepare(ctx, id); err != nil {
		return nil, err
	}
	return barrier.terminal, nil
}

func (f stopReadUnavailable) Commit(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil {
		return err
	}
	_ = f.db.Close()
	return errInjectedCommitResult
}

func TestNativeManualStopNotifiesOnlyAuthenticatedCommittedOrUnknownStop(t *testing.T) {
	for _, scenario := range []string{"success", "commit_then_error", "rollback", "unknown", "unknown_observer_error", "wrong_owner", "stale_revision", "not_found", "observer_error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			defer func() { _ = store.Shutdown(ctx) }()
			observer := &nativeStopObserver{}
			terminal := &countedStopTerminal{}
			prepared := 0
			var prepareRead func(context.Context, string) error
			options := manualcapture.DefaultOptions(store.ManualCaptureRepository())
			options.NativeRevocation = observer
			options.EvidenceBarrier = countedManualStopBarrier{terminal: terminal, prepare: func(ctx context.Context, id string) error { prepared++; return prepareRead(ctx, id) }}
			manager, err := manualcapture.NewManager(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Shutdown(ctx)
			grant, err := manager.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic stop", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeUntilRevoked})
			if err != nil {
				t.Fatal(err)
			}
			id, _ := manualcapture.ParseID(grant.Capture.ID)
			prepareRead = func(ctx context.Context, _ string) error {
				_, err := manager.Get(ctx, manualcapture.NewLocalOwnerScope(), id)
				return err
			}
			terminal.onCommit = func() {
				view, err := manager.Get(ctx, manualcapture.NewLocalOwnerScope(), id)
				if strings.HasPrefix(scenario, "unknown") {
					if err == nil {
						t.Error("unknown stop read unexpectedly became available")
					}
					return
				}
				if err != nil || view.State != manualcapture.StateRevoked {
					t.Errorf("reentrant Commit durable state %+v %v", view, err)
				}
			}
			terminal.onAbort = func() {
				view, err := manager.Get(ctx, manualcapture.NewLocalOwnerScope(), id)
				if err != nil || view.State != manualcapture.StateActive {
					t.Errorf("reentrant Abort changed known-live state %+v %v", view, err)
				}
			}
			command := manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: grant.Capture.CredentialRevision}
			repository := store.ManualCaptureRepository().(*manualCaptureRepository)
			switch scenario {
			case "commit_then_error":
				repository.stopCommitter = commitThenError{}
			case "rollback":
				repository.stopCommitter = rollbackThenError{}
			case "unknown", "unknown_observer_error":
				repository.stopCommitter = stopReadUnavailable{store.database}
			case "wrong_owner":
				command.Owner, _ = manualcapture.NewProxyClientOwnerScope("wrong-owner")
			case "stale_revision":
				command.ExpectedCredentialRevision++
			case "not_found":
				command.ID, _ = manualcapture.ParseID("manual-not-found")
			}
			observerOrderOK := true
			observer.callback = func(ctx context.Context, c captureidentity.Reference) error {
				if terminal.commits != 1 || terminal.aborts != 0 {
					observerOrderOK = false
				}
				if c.Kind != captureidentity.KindManualCapture || c.ID != grant.Capture.ID {
					observerOrderOK = false
				}
				if strings.HasPrefix(scenario, "unknown") {
					if scenario == "unknown_observer_error" {
						return errors.New("synthetic observer unavailable")
					}
					return nil
				}
				view, err := manager.Get(ctx, manualcapture.NewLocalOwnerScope(), id)
				if err != nil {
					return err
				}
				if c.Kind != captureidentity.KindManualCapture || c.ID != grant.Capture.ID || view.State != manualcapture.StateRevoked {
					return errors.New("notification preceded durable stop")
				}
				if scenario == "observer_error" {
					return errors.New("synthetic observer unavailable")
				}
				return nil
			}
			view, err := manager.Revoke(ctx, command)
			want := 0
			if scenario == "success" || scenario == "commit_then_error" || strings.HasPrefix(scenario, "unknown") || scenario == "observer_error" {
				want = 1
			}
			if observer.calls != want {
				t.Fatalf("notification count%d want%d error%v", observer.calls, want, err)
			}
			if prepared != 1 || !observerOrderOK {
				t.Fatal("terminal barrier/observer order was not exercised")
			}
			wantCommit, wantAbort := 0, 1
			if want == 1 {
				wantCommit, wantAbort = 1, 0
			}
			if terminal.commits != wantCommit || terminal.aborts != wantAbort {
				t.Fatalf("terminal Commit%d Abort%d want%d/%d", terminal.commits, terminal.aborts, wantCommit, wantAbort)
			}
			if scenario == "success" || scenario == "commit_then_error" {
				if err != nil || view.State != manualcapture.StateRevoked {
					t.Fatalf("committed stop %+v %v", view, err)
				}
			} else if err == nil {
				t.Fatal("failed stop reported success")
			}
			if errors.Is(err, manualcapture.ErrCommitOutcomeUnknown) != strings.HasPrefix(scenario, "unknown") {
				t.Fatalf("wrong unknown classification %v", err)
			}
			if scenario == "observer_error" {
				stored, readErr := manager.Get(ctx, manualcapture.NewLocalOwnerScope(), id)
				if readErr != nil || stored.State != manualcapture.StateRevoked {
					t.Fatal("observer error rolled back durable stop")
				}
			}
		})
	}
}

func TestNativeManagedStopNotifiesOnlyAuthenticatedCommittedOrUnknownStop(t *testing.T) {
	for _, scenario := range []string{"success", "commit_then_error", "rollback", "unknown", "unknown_observer_error", "bad_capability", "not_found", "observer_error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			defer func() { _ = store.Shutdown(ctx) }()
			observer := &nativeStopObserver{}
			terminal := &countedStopTerminal{}
			prepared := 0
			var prepareRead func(context.Context, string) error
			options := capturerun.DefaultOptions(store.CaptureRunRepository())
			options.NativeRevocation = observer
			options.EvidenceBarrier = countedManagedStopBarrier{terminal: terminal, prepare: func(ctx context.Context, id string) error { prepared++; return prepareRead(ctx, id) }}
			manager, err := capturerun.NewManager(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Shutdown(ctx)
			create := func() capturerun.LaunchGrant {
				grant, err := manager.Create(ctx, capturerun.CreateCommand{CWD: "/synthetic-workspace", CanonicalExecutablePath: "/synthetic-codex", ExecutableLabel: "Synthetic", CatalogRevision: 1, Lifetime: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				return grant
			}
			grant := create()
			prepareRead = func(ctx context.Context, _ string) error { _, err := manager.GetRun(ctx, grant.Run.ID); return err }
			terminal.onCommit = func() {
				view, err := manager.GetRun(ctx, grant.Run.ID)
				if strings.HasPrefix(scenario, "unknown") {
					if err == nil {
						t.Error("unknown stop read unexpectedly became available")
					}
					return
				}
				if err != nil || view.State != capturerun.StateFinished {
					t.Errorf("reentrant Commit durable state %+v %v", view, err)
				}
			}
			terminal.onAbort = func() {
				view, err := manager.GetRun(ctx, grant.Run.ID)
				if err != nil || view.State != capturerun.StateCreated {
					t.Errorf("reentrant Abort changed known-live state %+v %v", view, err)
				}
			}
			runID, capability := grant.Run.ID, grant.ControlCapability
			repository := store.CaptureRunRepository().(*captureRunRepository)
			switch scenario {
			case "commit_then_error":
				repository.stopCommitter = commitThenError{}
			case "rollback":
				repository.stopCommitter = rollbackThenError{}
			case "unknown", "unknown_observer_error":
				repository.stopCommitter = stopReadUnavailable{store.database}
			case "bad_capability":
				capability = create().ControlCapability
			case "not_found":
				runID = "run.not-found"
			}
			observerOrderOK := true
			observer.callback = func(ctx context.Context, c captureidentity.Reference) error {
				if terminal.commits != 1 || terminal.aborts != 0 {
					observerOrderOK = false
				}
				if c.Kind != captureidentity.KindManagedRun || c.ID != grant.Run.ID {
					observerOrderOK = false
				}
				if strings.HasPrefix(scenario, "unknown") {
					if scenario == "unknown_observer_error" {
						return errors.New("synthetic observer unavailable")
					}
					return nil
				}
				view, err := manager.GetRun(ctx, grant.Run.ID)
				if err != nil {
					return err
				}
				if c.Kind != captureidentity.KindManagedRun || c.ID != grant.Run.ID || view.State != capturerun.StateFinished {
					return errors.New("notification preceded durable stop")
				}
				if scenario == "observer_error" {
					return errors.New("synthetic observer unavailable")
				}
				return nil
			}
			err = manager.Finish(ctx, runID, capability)
			want := 0
			if scenario == "success" || scenario == "commit_then_error" || strings.HasPrefix(scenario, "unknown") || scenario == "observer_error" {
				want = 1
			}
			if observer.calls != want {
				t.Fatalf("notification count%d want%d error%v", observer.calls, want, err)
			}
			if prepared != 1 || !observerOrderOK {
				t.Fatal("terminal barrier/observer order was not exercised")
			}
			wantCommit, wantAbort := 0, 1
			if want == 1 {
				wantCommit, wantAbort = 1, 0
			}
			if terminal.commits != wantCommit || terminal.aborts != wantAbort {
				t.Fatalf("terminal Commit%d Abort%d want%d/%d", terminal.commits, terminal.aborts, wantCommit, wantAbort)
			}
			if scenario == "success" || scenario == "commit_then_error" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failed stop reported success")
			}
			if errors.Is(err, capturerun.ErrCommitOutcomeUnknown) != strings.HasPrefix(scenario, "unknown") {
				t.Fatalf("wrong unknown classification %v", err)
			}
			if scenario == "observer_error" {
				stored, readErr := manager.GetRun(ctx, grant.Run.ID)
				if readErr != nil || stored.State != capturerun.StateFinished {
					t.Fatal("observer error rolled back durable stop")
				}
			}
		})
	}
}
