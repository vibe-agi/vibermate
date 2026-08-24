package desktopcontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func TestArchiveClearHoldsTheExclusiveBarrierAcrossHolderCheckAndTransaction(
	t *testing.T,
) {
	t.Parallel()

	barrier := &recordingClearBarrier{}
	archive := &barrierCheckingArchive{barrier: barrier}
	handler := Handler{
		archive:        archive,
		archiveBarrier: barrier,
		captureRuns:    &deletionRunReader{},
		manualCaptures: &deletionManualCaptures{},
		idempotent:     newIdempotencyCache(),
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/evidence/actions/clear",
		nil,
	)
	request.Header.Set("If-Match", "1")
	request.Header.Set("Idempotency-Key", "clear-evidence-test-key")
	response := httptest.NewRecorder()

	handler.clearArchive(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !archive.sawBarrier || barrier.calls != 1 || barrier.releases != 1 ||
		barrier.active != 0 {
		t.Fatalf(
			"archive barrier saw=%t calls=%d releases=%d active=%d",
			archive.sawBarrier,
			barrier.calls,
			barrier.releases,
			barrier.active,
		)
	}
}

type recordingClearBarrier struct {
	active   int
	calls    int
	releases int
}

func (barrier *recordingClearBarrier) BeginClear(
	context.Context,
) (evidencearchive.Release, error) {
	barrier.calls++
	barrier.active++
	return func() {
		barrier.releases++
		barrier.active--
	}, nil
}

type barrierCheckingArchive struct {
	barrier    *recordingClearBarrier
	sawBarrier bool
}

func (*barrierCheckingArchive) DeleteCapture(
	context.Context,
	string,
	string,
) (resourcedeletion.Released, error) {
	return resourcedeletion.Released{}, errors.New("unexpected DeleteCapture")
}

func (archive *barrierCheckingArchive) ClearEvidence(
	context.Context,
) (resourcedeletion.Released, error) {
	archive.sawBarrier = archive.barrier.active == 1
	return resourcedeletion.Released{}, nil
}

type deletionRunReader struct {
	pages    [][]capturerun.View
	requests []capturerun.PageRequest
}

func (reader *deletionRunReader) ListRuns(
	_ context.Context,
	request capturerun.PageRequest,
) (capturerun.Page, error) {
	reader.requests = append(reader.requests, request)
	index := len(reader.requests) - 1
	if index >= len(reader.pages) {
		return capturerun.Page{Items: []capturerun.View{}}, nil
	}
	return capturerun.Page{
		Items: append([]capturerun.View(nil), reader.pages[index]...),
	}, nil
}

func (*deletionRunReader) GetRun(
	context.Context,
	string,
) (capturerun.View, error) {
	return capturerun.View{}, capturerun.ErrNotFound
}

type deletionManualCaptures struct {
	view     manualcapture.View
	pages    [][]manualcapture.View
	requests []manualcapture.PageRequest
}

func (*deletionManualCaptures) Create(
	context.Context,
	manualcapture.CreateCommand,
) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("Create was not expected")
}

func (*deletionManualCaptures) Rotate(
	context.Context,
	manualcapture.RotateCommand,
) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("Rotate was not expected")
}

func (*deletionManualCaptures) Revoke(
	context.Context,
	manualcapture.RevokeCommand,
) (manualcapture.View, error) {
	return manualcapture.View{}, errors.New("Revoke was not expected")
}

func (captures *deletionManualCaptures) Get(
	_ context.Context,
	_ manualcapture.OwnerScope,
	id manualcapture.ID,
) (manualcapture.View, error) {
	if captures.view.ID != id.String() {
		return manualcapture.View{}, manualcapture.ErrNotFound
	}
	return captures.view, nil
}

func (captures *deletionManualCaptures) List(
	_ context.Context,
	request manualcapture.PageRequest,
) (manualcapture.Page, error) {
	captures.requests = append(captures.requests, request)
	index := len(captures.requests) - 1
	if index >= len(captures.pages) {
		return manualcapture.Page{Items: []manualcapture.View{}}, nil
	}
	return manualcapture.Page{
		Items: append([]manualcapture.View(nil), captures.pages[index]...),
	}, nil
}

func TestRunningCaptureGuardChecksTheExactManualCapture(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	manual := &deletionManualCaptures{view: manualcapture.View{
		ID:          "manual-active",
		DisplayName: "Figma Desktop",
		State:       manualcapture.StateActive,
		UpdatedAt:   now,
	}}
	handler := Handler{
		captureRuns:    &deletionRunReader{},
		manualCaptures: manual,
	}
	target, err := captureidentity.New(
		captureidentity.KindManualCapture,
		"manual-active",
	)
	if err != nil {
		t.Fatal(err)
	}

	holders, err := handler.runningCaptureHolders(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(holders) != 1 {
		t.Fatalf("holders = %+v, want the active ManualCapture", holders)
	}
	if holders[0].ID != "manual_capture:manual-active" ||
		holders[0].Label != "Figma Desktop" ||
		holders[0].Detail != string(manualcapture.StateActive) {
		t.Fatalf("holder = %+v", holders[0])
	}
}

func TestArchiveClearGuardReadsEveryCaptureCatalogPage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	managedFirst := make([]capturerun.View, capturerun.MaxPageLimit)
	manualFirst := make([]manualcapture.View, manualcapture.MaxPageLimit)
	for index := range managedFirst {
		managedFirst[index] = capturerun.View{
			ID:              fmt.Sprintf("run.%03d", index),
			ExecutableLabel: "Claude Code",
			State:           capturerun.StateAttached,
			UpdatedAt:       now,
		}
		manualFirst[index] = manualcapture.View{
			ID:          fmt.Sprintf("manual.%03d", index),
			DisplayName: "Desktop client",
			State:       manualcapture.StateActive,
			UpdatedAt:   now,
		}
	}
	runs := &deletionRunReader{pages: [][]capturerun.View{
		managedFirst,
		{{
			ID: "run.200", ExecutableLabel: "Codex", State: capturerun.StateCreated,
			UpdatedAt: now.Add(-time.Second),
		}},
	}}
	manual := &deletionManualCaptures{pages: [][]manualcapture.View{
		manualFirst,
		{{
			ID: "manual.200", DisplayName: "IDE plugin", State: manualcapture.StateActive,
			UpdatedAt: now.Add(-time.Second),
		}},
	}}
	handler := Handler{captureRuns: runs, manualCaptures: manual}

	holders, err := handler.runningCaptureHolders(
		context.Background(),
		captureidentity.Reference{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(holders) != 402 {
		t.Fatalf("holders = %d, want 402", len(holders))
	}
	if len(runs.requests) != 2 || runs.requests[1].Cursor == nil ||
		!runs.requests[1].Cursor.Valid() {
		t.Fatalf("managed pagination requests = %+v", runs.requests)
	}
	if len(manual.requests) != 2 || manual.requests[1].Cursor == nil ||
		!manual.requests[1].Cursor.Valid() {
		t.Fatalf("manual pagination requests = %+v", manual.requests)
	}
}
