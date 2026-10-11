package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/localdiscovery"
)

const packagedDesktopDiagnosticTimeout = time.Second

type desktopLaunchDiagnosticInput struct {
	phase       string
	sink        func(desktopExitDiagnostic)
	preferences func() string
}

type desktopDiagnosticProcessObservation struct {
	State             string `json:"state"`
	ProcessID         int    `json:"processId,omitempty"`
	BirthSeconds      int64  `json:"birthSeconds,omitempty"`
	BirthMicroseconds int64  `json:"birthMicroseconds,omitempty"`
}

type desktopExitSnapshot struct {
	State        string                              `json:"state"`
	StartedMS    int64                               `json:"startedMs"`
	FinishedMS   int64                               `json:"finishedMs"`
	Launcher     desktopDiagnosticProcessObservation `json:"launcher"`
	App          desktopDiagnosticProcessObservation `json:"app"`
	Sidecar      desktopDiagnosticProcessObservation `json:"sidecar"`
	Registration string                              `json:"registration"`
	Discovery    string                              `json:"discovery"`
	Preferences  string                              `json:"preferences"`
}

type desktopWaitObservation struct {
	State     string `json:"state"`
	ElapsedMS int64  `json:"elapsedMs"`
}

type desktopCleanupObservation struct {
	Request      string                              `json:"request"`
	Force        string                              `json:"force"`
	Outcome      string                              `json:"outcome"`
	ElapsedMS    int64                               `json:"elapsedMs"`
	App          desktopDiagnosticProcessObservation `json:"app"`
	LauncherWait string                              `json:"launcherWait"`
}

type desktopOutputObservation struct {
	Bytes    int  `json:"bytes"`
	Overflow bool `json:"overflow"`
}

type desktopExitDiagnostic struct {
	Launch            string                    `json:"launch"`
	Stage             string                    `json:"stage"`
	Outcome           string                    `json:"outcome"`
	Selected          string                    `json:"selected"`
	SelectedMS        int64                     `json:"selectedMs"`
	Request           string                    `json:"request"`
	RequestMS         int64                     `json:"requestMs"`
	AcknowledgementMS int64                     `json:"acknowledgementMs"`
	Wait              desktopWaitObservation    `json:"waitAtSnapshot"`
	FinalWait         desktopWaitObservation    `json:"waitAfterCleanup"`
	Snapshot          desktopExitSnapshot       `json:"snapshot"`
	Cleanup           desktopCleanupObservation `json:"cleanup"`
	Stdout            desktopOutputObservation  `json:"stdout"`
	Stderr            desktopOutputObservation  `json:"stderr"`
}

type desktopDiagnosticWait struct {
	finished chan struct{}
	mu       sync.Mutex
	started  time.Time
	result   desktopWaitObservation
}

// The original error channel is still used by launch checks. This independent
// closed channel remains available to cleanup even after a check consumes it.
func newDesktopDiagnosticWait(started time.Time) *desktopDiagnosticWait {
	return &desktopDiagnosticWait{finished: make(chan struct{}), started: started, result: desktopWaitObservation{State: "pending", ElapsedMS: -1}}
}
func (wait *desktopDiagnosticWait) complete(err error, at time.Time) {
	wait.mu.Lock()
	wait.result = desktopWaitObservation{State: "success", ElapsedMS: at.Sub(wait.started).Milliseconds()}
	if err != nil {
		wait.result.State = "error"
	}
	wait.mu.Unlock()
	close(wait.finished)
}
func (wait *desktopDiagnosticWait) observation() desktopWaitObservation {
	wait.mu.Lock()
	defer wait.mu.Unlock()
	return wait.result
}

type desktopExitRecorder struct {
	started                                  time.Time
	input                                    desktopLaunchDiagnosticInput
	wait                                     *desktopDiagnosticWait
	stage, selected, request                 string
	selectedMS, requestMS, acknowledgementMS int64
}

func newDesktopExitRecorder(input desktopLaunchDiagnosticInput) *desktopExitRecorder {
	switch input.phase {
	case "first_restore", "second_restore":
	default:
		input.phase = "standalone"
	}
	started := time.Now()
	return &desktopExitRecorder{started: started, input: input, wait: newDesktopDiagnosticWait(started), stage: "startup", selected: "not_reached", request: "not_requested", selectedMS: -1, requestMS: -1, acknowledgementMS: -1}
}

func (recorder *desktopExitRecorder) finish(original error, cleaned bool, initial desktopExitSnapshot, sample func(context.Context) desktopExitSnapshot, cleanup func() desktopCleanupObservation, stdout, stderr *boundedBuffer) error {
	diagnostic := desktopExitDiagnostic{Launch: recorder.input.phase, Stage: recorder.stage, Outcome: "failed", Selected: recorder.selected, SelectedMS: recorder.selectedMS, Request: recorder.request, RequestMS: recorder.requestMS, AcknowledgementMS: recorder.acknowledgementMS}
	// Sampling is separately bounded, before cleanup. It never affects the
	// original error or the original acceptance deadline/success conditions.
	ctx, cancel := context.WithTimeout(context.Background(), packagedDesktopDiagnosticTimeout)
	diagnostic.Snapshot = captureDesktopExitSnapshot(ctx, recorder.started, initial, sample)
	cancel()
	diagnostic.Wait = recorder.wait.observation()
	diagnostic.Cleanup = desktopCleanupObservation{Request: "not_requested", Force: "not_requested", Outcome: "not_needed", App: diagnostic.Snapshot.App, LauncherWait: "not_needed"}
	if cleaned {
		diagnostic.Outcome = "launch_checks_passed"
	} else {
		diagnostic.Cleanup = cleanup()
	}
	diagnostic.FinalWait = recorder.wait.observation()
	counts := func(buffer *boundedBuffer) desktopOutputObservation {
		if buffer != nil {
			encoded, overflow := buffer.snapshot()
			return desktopOutputObservation{Bytes: len(encoded), Overflow: overflow}
		}
		return desktopOutputObservation{}
	}
	diagnostic.Stdout, diagnostic.Stderr = counts(stdout), counts(stderr)
	if recorder.input.sink != nil {
		recorder.input.sink(diagnostic)
	}
	return original
}

func desktopDiagnosticProcess(pid int, birth desktopProcessStart, inspect func(int) (desktopProcessSnapshot, error)) desktopDiagnosticProcessObservation {
	if pid <= 0 || birth.seconds <= 0 {
		return desktopDiagnosticProcessObservation{State: "unbound"}
	}
	result := desktopDiagnosticProcessObservation{State: "unknown", ProcessID: pid, BirthSeconds: birth.seconds, BirthMicroseconds: birth.microseconds}
	observed, err := inspect(pid)
	present, err := desktopProcessIdentityPresent(birth, observed, err)
	if err == nil {
		if present {
			result.State = "present"
		} else {
			result.State = "absent"
		}
	}
	return result
}

func captureDesktopExitSnapshot(ctx context.Context, started time.Time, initial desktopExitSnapshot, sample func(context.Context) desktopExitSnapshot) desktopExitSnapshot {
	at := time.Since(started).Milliseconds()
	result := make(chan desktopExitSnapshot, 1)
	go func() { result <- sample(ctx) }()
	select {
	case observed := <-result:
		// A result racing the separate deadline cannot claim a timely sample.
		if ctx.Err() == nil {
			initial = observed
			initial.State = "complete"
		} else {
			initial.State = "timeout"
		}
	case <-ctx.Done():
		initial.State = "timeout"
	}
	initial.StartedMS = at
	initial.FinishedMS = time.Since(started).Milliseconds()
	return initial
}

func desktopDiagnosticPreferences(path string, previous desktopPreferencesFile, expected []byte) string {
	if previous.info == nil {
		return "unavailable"
	}
	current, err := readDesktopPreferencesFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}
	if err != nil {
		return "unknown"
	}
	if !bytes.Equal(current.encoded, expected) || os.SameFile(previous.info, current.info) {
		return "not_rewritten"
	}
	return "rewritten"
}

// The initial value contains only already-bound identities. If a probe times
// out, their status stays unknown rather than being replaced by zero/absent.
func initialDesktopExitSnapshot(launcherPID int, launcherBirth desktopProcessStart, app desktopApplicationIdentity, sidecarPID int) desktopExitSnapshot {
	unknown := func(pid int, birth desktopProcessStart) desktopDiagnosticProcessObservation {
		return desktopDiagnosticProcess(pid, birth, func(int) (desktopProcessSnapshot, error) { return desktopProcessSnapshot{}, context.DeadlineExceeded })
	}
	return desktopExitSnapshot{Launcher: unknown(launcherPID, launcherBirth), App: unknown(app.ProcessID, app.started), Sidecar: unknown(sidecarPID, app.sidecarStarted), Registration: "unknown", Discovery: "unknown", Preferences: "unavailable"}
}

func collectDesktopExitSnapshot(ctx context.Context, initial desktopExitSnapshot, app desktopApplicationIdentity, discovery *localdiscovery.File, generation localdiscovery.Session, preferences func() string) desktopExitSnapshot {
	for _, process := range []*desktopDiagnosticProcessObservation{&initial.Launcher, &initial.App, &initial.Sidecar} {
		if ctx.Err() != nil {
			return initial
		}
		*process = desktopDiagnosticProcess(process.ProcessID, desktopProcessStart{seconds: process.BirthSeconds, microseconds: process.BirthMicroseconds}, inspectDesktopProcess)
	}
	if ctx.Err() != nil {
		return initial
	}
	if app.started.seconds <= 0 {
		initial.Registration = "unbound"
	} else {
		applications, err := desktopApplications(ctx)
		if err == nil {
			initial.Registration = "unregistered"
			for _, candidate := range applications {
				if candidate.ProcessID == app.ProcessID && candidate.BundlePath == app.BundlePath && candidate.ExecutablePath == app.ExecutablePath {
					initial.Registration = "registered"
				}
			}
		}
	}
	if ctx.Err() != nil {
		return initial
	}
	if discovery == nil || generation.InstanceID == "" {
		initial.Discovery = "unbound"
	} else {
		current, err := discovery.Load()
		switch {
		case errors.Is(err, os.ErrNotExist):
			initial.Discovery = "absent"
		case err != nil:
			initial.Discovery = "unknown"
		case current.InstanceID == generation.InstanceID:
			initial.Discovery = "owned"
		default:
			initial.Discovery = "other_generation"
		}
	}
	if ctx.Err() == nil && preferences != nil {
		initial.Preferences = preferences()
	}
	return initial
}
