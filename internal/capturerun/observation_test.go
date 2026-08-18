package capturerun

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGeneratedRunIDHasAControlResourcePrefix(t *testing.T) {
	t.Parallel()

	id, err := newRunID(bytes.NewReader(bytes.Repeat([]byte{0xfb}, runIDRandomBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, runIDPrefix) || id[0] == '-' || id[0] == '_' {
		t.Fatalf("generated CaptureRun ID = %q", id)
	}
}

func observationRecord(t *testing.T) DurableRecord {
	t.Helper()

	now := time.Unix(1785600000, 0).UTC()
	return DurableRecord{
		ID:                      "0123456789abcdefghij0123456789ab",
		ProxyCapabilityHash:     CapabilityDigest{0x11},
		ControlCapabilityHash:   CapabilityDigest{0x22},
		Observation:             ObservationWaitingForTraffic,
		CWD:                     "/tmp/workspace",
		CanonicalExecutablePath: "/usr/local/bin/claude",
		ExecutableLabel:         "claude",
		CatalogRevision:         1,
		State:                   StateCreated,
		CreatedAt:               now,
		ExpiresAt:               now.Add(time.Minute),
		UpdatedAt:               now,
	}
}

// Design 02 is explicit that a run created but never used is waiting for
// traffic rather than captured. A program that ignores proxy variables, clears
// its environment, dials a socket directly, or uses QUIC produces exactly this
// shape, and reporting it as captured is the difference between a working
// setup and a silently broken one.
func TestANewRunIsWaitingForTrafficRatherThanCaptured(t *testing.T) {
	t.Parallel()

	record := observationRecord(t)
	if record.Observation != ObservationWaitingForTraffic {
		t.Fatalf("new run observation = %q", record.Observation)
	}
	if record.Observed() {
		t.Fatal("a run with no traffic reported as observed")
	}
}

// Observation comes from real authenticated proxy traffic and is monotonic:
// the first connection marks it and later ones do not rewrite it.
func TestObservationIsMonotonicAndIdempotent(t *testing.T) {
	t.Parallel()

	record := observationRecord(t)
	first := time.Unix(1785600010, 0).UTC()
	marked, changed := record.WithObservedTraffic(first)
	if !changed || !marked.Observed() {
		t.Fatalf("first authenticated connection did not mark observation")
	}
	if !marked.FirstObservedAt.Equal(first) {
		t.Fatalf("first observation time = %v", marked.FirstObservedAt)
	}
	if err := marked.Validate(); err != nil {
		t.Fatal(err)
	}

	later := time.Unix(1785600020, 0).UTC()
	again, changedAgain := marked.WithObservedTraffic(later)
	if changedAgain {
		t.Fatal("a later connection rewrote the observation")
	}
	if !again.FirstObservedAt.Equal(first) {
		t.Fatalf("observation time moved to %v", again.FirstObservedAt)
	}
}

// A run that is no longer active cannot become observed afterwards.
func TestAFinishedRunCannotBecomeObserved(t *testing.T) {
	t.Parallel()

	for _, state := range []State{
		StateFinished,
		StateRevoked,
		StateExpired,
	} {
		record := observationRecord(t)
		record.State = state
		_, changed := record.WithObservedTraffic(
			time.Unix(1785600010, 0).UTC(),
		)
		if changed {
			t.Fatalf("a %q run became observed", state)
		}
	}
}

// An observation time is evidence and must be consistent with the record.
func TestObservationTimeMustBeConsistent(t *testing.T) {
	t.Parallel()

	record := observationRecord(t)
	record.Observation = ObservationObserved
	if err := record.Validate(); err == nil {
		t.Fatal("an observed run without an observation time was accepted")
	}

	record.FirstObservedAt = record.CreatedAt.Add(-time.Second)
	if err := record.Validate(); err == nil {
		t.Fatal("an observation before the run existed was accepted")
	}

	waiting := observationRecord(t)
	waiting.FirstObservedAt = waiting.CreatedAt.Add(time.Second)
	if err := waiting.Validate(); err == nil {
		t.Fatal("a waiting run carrying an observation time was accepted")
	}
}
