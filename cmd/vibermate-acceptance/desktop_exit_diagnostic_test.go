package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

// Removing the phase wrapper must make this fail without starting any process.
func TestDesktopExitDiagnosticLabelsStartupFailures(t *testing.T) {
	err := exercisePackagedDesktopLaunch(context.Background(), "relative.app", runtimepath.Layout{}, "", func(context.Context, <-chan error) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "standalone") || !strings.Contains(err.Error(), "Desktop App path is invalid") {
		t.Fatalf("startup failure lost its phase or original cause: %v", err)
	}
}

// Treating an inspection error as disappearance would hide a real orphan.
func TestDesktopExitDiagnosticUnknownIsNotAbsence(t *testing.T) {
	birth := desktopProcessStart{seconds: 12, microseconds: 34}
	for _, test := range []struct {
		name     string
		observed desktopProcessSnapshot
		err      error
		want     string
	}{
		{"same_birth", desktopProcessSnapshot{started: birth}, nil, "present"},
		{"missing", desktopProcessSnapshot{}, errDesktopProcessUnavailable, "absent"},
		{"reused_pid", desktopProcessSnapshot{started: desktopProcessStart{seconds: 13}}, nil, "absent"},
		{"inspection_error", desktopProcessSnapshot{}, errors.New("private-error-token"), "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := desktopDiagnosticProcess(123, birth, func(int) (desktopProcessSnapshot, error) { return test.observed, test.err })
			if got.State != test.want || got.ProcessID != 123 || got.BirthSeconds != 12 || got.BirthMicroseconds != 34 {
				t.Fatalf("identity observation = %+v", got)
			}
		})
	}
	called := false
	got := desktopDiagnosticProcess(123, desktopProcessStart{}, func(int) (desktopProcessSnapshot, error) { called = true; return desktopProcessSnapshot{}, nil })
	if called || got.State != "unbound" || got.ProcessID != 0 {
		t.Fatalf("unbound identity was inspected: %+v, called=%v", got, called)
	}
}

// Recording or inspecting Wait must not steal cleanup's completion notification.
func TestDesktopExitDiagnosticWaitCanBeObservedRepeatedly(t *testing.T) {
	started := time.Now()
	wait := newDesktopDiagnosticWait(started)
	wait.complete(errors.New("private-error-token"), started.Add(25*time.Millisecond))
	for range 2 {
		select {
		case <-wait.finished:
		default:
			t.Fatal("completion notification was consumed")
		}
		if got := wait.observation(); got.State != "error" || got.ElapsedMS != 25 {
			t.Fatalf("Wait observation = %+v", got)
		}
	}
}

// Moving sampling after cleanup, leaking raw buffers/errors, or changing the
// original failure must all fail this consumer-visible observation test.
func TestDesktopExitDiagnosticSamplesBeforeCleanupAndRedacts(t *testing.T) {
	var order []string
	var got desktopExitDiagnostic
	secret := "private-error-token"
	out := newBoundedBuffer(4)
	_, _ = out.Write([]byte(secret))
	recorder := newDesktopExitRecorder(desktopLaunchDiagnosticInput{phase: "first_restore", sink: func(value desktopExitDiagnostic) { order = append(order, "emit"); got = value }})
	recorder.selected = "exit_deadline"
	recorder.wait.complete(errors.New(secret), recorder.started.Add(4*time.Second))
	original := errors.New("packaged Desktop graceful exit deadline exceeded")
	result := recorder.finish(original, false, desktopExitSnapshot{}, func(context.Context) desktopExitSnapshot {
		order = append(order, "snapshot")
		return desktopExitSnapshot{App: desktopDiagnosticProcessObservation{State: "present"}, Preferences: "not_rewritten"}
	}, func() desktopCleanupObservation {
		order = append(order, "cleanup")
		return desktopCleanupObservation{Force: "accepted", Outcome: "force_requested"}
	}, out, newBoundedBuffer(4))
	if result != original || !reflect.DeepEqual(order, []string{"snapshot", "cleanup", "emit"}) || got.Snapshot.App.State != "present" || got.Cleanup.Force != "accepted" || got.Outcome != "failed" || got.Selected != "exit_deadline" {
		t.Fatalf("lost pre-cleanup evidence or original failure: order=%v observation=%+v", order, got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "private") || got.Stdout.Bytes != 4 || !got.Stdout.Overflow {
		t.Fatalf("unsafe or incorrect redacted observation: %s", encoded)
	}
}

// Successful launch checks still emit a snapshot without invoking cleanup.
func TestDesktopExitDiagnosticRetainsNormalExit(t *testing.T) {
	var got desktopExitDiagnostic
	recorder := newDesktopExitRecorder(desktopLaunchDiagnosticInput{phase: "second_restore", sink: func(value desktopExitDiagnostic) { got = value }})
	recorder.selected = "launcher_wait"
	recorder.wait.complete(nil, recorder.started.Add(10*time.Millisecond))
	cleanup := false
	if err := recorder.finish(nil, true, desktopExitSnapshot{}, func(context.Context) desktopExitSnapshot { return desktopExitSnapshot{Preferences: "rewritten"} }, func() desktopCleanupObservation { cleanup = true; return desktopCleanupObservation{} }, newBoundedBuffer(1), newBoundedBuffer(1)); err != nil {
		t.Fatal(err)
	}
	if cleanup || got.Launch != "second_restore" || got.Outcome != "launch_checks_passed" || got.Wait.State != "success" || got.Cleanup.Outcome != "not_needed" || got.Snapshot.Preferences != "rewritten" {
		t.Fatalf("normal exit evidence = %+v, cleanup=%v", got, cleanup)
	}
}

// A timed-out probe remains unknown, preserves bound identity, and reports the
// actual later snapshot time instead of attributing it to the 3s deadline.
func TestDesktopExitDiagnosticSnapshotHasSeparateBound(t *testing.T) {
	started := time.Now().Add(-4 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	initial := desktopExitSnapshot{App: desktopDiagnosticProcessObservation{State: "unknown", ProcessID: 123, BirthSeconds: 12}}
	got := captureDesktopExitSnapshot(ctx, started, initial, func(ctx context.Context) desktopExitSnapshot { <-ctx.Done(); return initial })
	if got.State != "timeout" || got.App.State != "unknown" || got.App.ProcessID != 123 || got.StartedMS < 4000 || got.FinishedMS < got.StartedMS {
		t.Fatalf("bounded snapshot = %+v", got)
	}
}

// Contents equal on the same inode are not an atomic rewrite; malformed private
// files are unknown, not absent. No file contents enter observations.
func TestDesktopExitDiagnosticPreferenceRewriteUsesAtomicEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences", desktopPreferencesStateFile)
	expected := canonicalDesktopPreferences("en-US", "settings", nil, nil)
	previous, err := publishDesktopPreferencesFixture(path, expected)
	if err != nil {
		t.Fatal(err)
	}
	if got := desktopDiagnosticPreferences(path, previous, expected); got != "not_rewritten" {
		t.Fatalf("same inode = %s", got)
	}
	if _, err := publishDesktopPreferencesFixture(path, expected); err != nil {
		t.Fatal(err)
	}
	if got := desktopDiagnosticPreferences(path, previous, expected); got != "rewritten" {
		t.Fatalf("atomic rewrite = %s", got)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := desktopDiagnosticPreferences(path, previous, expected); got != "unknown" {
		t.Fatalf("unsafe file = %s", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := desktopDiagnosticPreferences(path, previous, expected); got != "absent" {
		t.Fatalf("missing file = %s", got)
	}
}

// The existing restore error must not bypass diagnostic redaction by embedding
// the file's bytes in t.Fatal's error output.
func TestDesktopExitDiagnosticRestoreDeadlineDoesNotPrintFileContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences", desktopPreferencesStateFile)
	private := []byte("private-file-marker")
	previous, err := publishDesktopPreferencesFixture(path, private)
	if err != nil {
		t.Fatal("could not publish private fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = waitForDesktopPreferencesRewrite(ctx, make(chan error), path, previous, []byte("expected"))
	if err == nil || !strings.Contains(err.Error(), "packaged Desktop preference restore deadline exceeded") {
		t.Fatal("restore deadline cause was not retained")
	}
	if strings.Contains(err.Error(), string(private)) {
		t.Fatal("restore deadline error contains raw fixture contents")
	}
}
