package evidencearchive

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBarrierLetsCaptureCreationsOverlapButMakesArchiveClearExclusive(
	t *testing.T,
) {
	t.Parallel()

	barrier := NewBarrier()
	first, err := barrier.BeginCaptureCreation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := barrier.BeginCaptureCreation(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	blockedClear, cancelClear := context.WithTimeout(
		context.Background(), 50*time.Millisecond,
	)
	defer cancelClear()
	if _, err := barrier.BeginClear(blockedClear); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("clear while Capture creation leases are held error = %v", err)
	}
	first()
	second()

	clear, err := barrier.BeginClear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blockedCapture, cancelCapture := context.WithTimeout(
		context.Background(), 50*time.Millisecond,
	)
	defer cancelCapture()
	if _, err := barrier.BeginCaptureCreation(blockedCapture); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Capture creation while archive clear lease is held error = %v", err)
	}
	clear()

	capture, err := barrier.BeginCaptureCreation(context.Background())
	if err != nil {
		t.Fatalf("Capture creation after archive clear = %v", err)
	}
	capture()
}

func TestBarrierReleaseIsIdempotent(t *testing.T) {
	t.Parallel()

	barrier := NewBarrier()
	release, err := barrier.BeginClear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()

	next, err := barrier.BeginClear(context.Background())
	if err != nil {
		t.Fatalf("second clear after duplicate release = %v", err)
	}
	next()
}
