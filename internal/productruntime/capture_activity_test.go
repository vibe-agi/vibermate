package productruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func TestCaptureActivityResolverUsesTheTypedCaptureLifecycleAuthority(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	runs := &recordingRunActivity{active: true}
	manuals := &recordingManualActivity{active: false}
	resolver, err := newCaptureActivityResolver(
		runs,
		manuals,
		captureActivityClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	managed, _ := captureidentity.New(captureidentity.KindManagedRun, "run-1")
	active, err := resolver.Active(context.Background(), managed)
	if err != nil || !active || runs.id != "run-1" || !runs.now.Equal(now) {
		t.Fatalf("managed activity = %v, %v; reader=%+v", active, err, runs)
	}
	manual, _ := captureidentity.New(captureidentity.KindManualCapture, "manual-1")
	active, err = resolver.Active(context.Background(), manual)
	if err != nil || active || manuals.id.String() != "manual-1" || !manuals.now.Equal(now) {
		t.Fatalf("manual activity = %v, %v; reader=%+v", active, err, manuals)
	}
}

type captureActivityClock struct{ now time.Time }

func (clock captureActivityClock) Now() time.Time { return clock.now }

type recordingRunActivity struct {
	id     string
	now    time.Time
	active bool
	err    error
}

func (reader *recordingRunActivity) Active(
	_ context.Context,
	id string,
	now time.Time,
) (bool, error) {
	reader.id, reader.now = id, now
	return reader.active, reader.err
}

type recordingManualActivity struct {
	id     manualcapture.ID
	now    time.Time
	active bool
	err    error
}

func (reader *recordingManualActivity) Active(
	_ context.Context,
	id manualcapture.ID,
	now time.Time,
) (bool, error) {
	reader.id, reader.now = id, now
	return reader.active, reader.err
}

func TestCaptureActivityResolverFailsClosedOnLifecycleReadError(t *testing.T) {
	t.Parallel()
	want := errors.New("lifecycle unavailable")
	resolver, err := newCaptureActivityResolver(
		&recordingRunActivity{err: want},
		&recordingManualActivity{},
		captureActivityClock{now: time.Now()},
	)
	if err != nil {
		t.Fatal(err)
	}
	reference, _ := captureidentity.New(captureidentity.KindManagedRun, "run-1")
	if _, err := resolver.Active(context.Background(), reference); !errors.Is(err, want) {
		t.Fatalf("activity error = %v", err)
	}
}
