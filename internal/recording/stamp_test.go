package recording

import (
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"strings"
	"sync"
	"testing"
)

func TestStampScopeAndEpochIsolation(t *testing.T) {
	e, _ := NewEpoch(1)
	other, _ := NewEpoch(1)
	ref, _ := captureidentity.New(captureidentity.KindManagedRun, "run")
	a, _ := NewScopeToken(e, ref, Fence{1, 1})
	b, _ := NewScopeToken(e, ref, Fence{1, 2})
	c, _ := NewScopeToken(other, captureidentity.Reference{}, Fence{1, 0})
	s := a.Stamp()
	if !s.Current() || s.Capture() != ref || s.Fence() != (Fence{1, 1}) {
		t.Fatal("invalid stamp")
	}
	a.Revoke()
	a.Revoke()
	if s.Current() || !b.Stamp().Current() {
		t.Fatal("revocation crossed scopes")
	}
	if !e.Advance(2) || e.Advance(1) || e.Advance(0) || e.Advance(2) {
		t.Fatal("epoch not monotonic")
	}
	if b.Stamp().Current() || !c.Stamp().Current() {
		t.Fatal("epoch isolation failed")
	}
	if (Stamp{}).Current() {
		t.Fatal("zero stamp valid")
	}
}
func TestInvalidStampInputs(t *testing.T) {
	if _, err := NewEpoch(0); err == nil {
		t.Fatal("zero epoch")
	}
	e, _ := NewEpoch(2)
	for _, f := range []Fence{{0, 0}, {1, 0}, {2, 1}} {
		if _, err := NewScopeToken(e, captureidentity.Reference{}, f); err == nil {
			t.Fatal("invalid runtime scope accepted")
		}
	}
	if _, err := NewScopeToken(nil, captureidentity.Reference{}, Fence{2, 0}); err == nil {
		t.Fatal("nil epoch")
	}
	ref, _ := captureidentity.New(captureidentity.KindManualCapture, "capture")
	if _, err := NewScopeToken(e, ref, Fence{2, 0}); err == nil {
		t.Fatal("zero generation")
	}
	if _, err := NewScopeToken(e, captureidentity.Reference{ID: "bad"}, Fence{2, 1}); err == nil {
		t.Fatal("invalid reference")
	}
}
func TestConcurrentStampRevocation(t *testing.T) {
	e, _ := NewEpoch(1)
	token, _ := NewScopeToken(e, captureidentity.Reference{}, Fence{1, 0})
	s := token.Stamp()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 1000 {
				s.Current()
				e.Advance(2)
				token.Revoke()
			}
		})
	}
	wg.Wait()
	if s.Current() {
		t.Fatal("stale stamp current")
	}
}

func TestInlineStampRetainedCoordinatesAllocateNothing(t *testing.T) {
	epoch, _ := NewEpoch(1)
	reference := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "inline-capture"}
	var retained [1024]Stamp
	index := 0
	allocs := testing.AllocsPerRun(1000, func() {
		stamp, err := NewStamp(epoch, reference, Fence{1, 1})
		if err != nil {
			panic(err)
		}
		if !stamp.HasCapture() || stamp.CaptureIDBytes() != len(reference.ID) || !stamp.SameScope(stamp) || !stamp.MatchesCapture(reference) {
			panic("lost coordinates")
		}
		retained[index%len(retained)] = stamp
		index++
	})
	if allocs != 0 {
		t.Fatalf("retaining inline stamps allocated %g producer copies", allocs)
	}
	for _, stamp := range retained[:1000] {
		if stamp.Capture() != reference || !stamp.Current() {
			t.Fatal("retained inline coordinates changed")
		}
	}
	epoch.Advance(2)
	for _, stamp := range retained[:1000] {
		if stamp.Current() {
			t.Fatal("inline stamp survived epoch revocation")
		}
	}
}

func TestInlineStampMaximumCaptureCoordinates(t *testing.T) {
	epoch, _ := NewEpoch(4)
	ref := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: strings.Repeat("x", captureidentity.MaxIDBytes)}
	stamp, err := NewStamp(epoch, ref, Fence{4, 3})
	if err != nil {
		t.Fatal(err)
	}
	if stamp.Capture() != ref || !stamp.MatchesCapture(ref) || stamp.CaptureIDBytes() != captureidentity.MaxIDBytes {
		t.Fatal("inline maximum-length ID changed")
	}
	ref.ID += "x"
	if stamp.MatchesCapture(ref) {
		t.Fatal("inline comparison truncated oversized identity")
	}
	if _, err := NewStamp(epoch, ref, Fence{4, 3}); err == nil {
		t.Fatal("oversized inline coordinates accepted")
	}
}
