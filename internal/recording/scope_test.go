package recording

import (
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"testing"
)

func TestPersistenceScopeCoordinates(t *testing.T) {
	ref := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "capture"}
	for _, s := range []Scope{{}, {Fence: Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, {Capture: ref, Fence: Fence{ArchiveEpoch: 1}}, {Capture: captureidentity.Reference{ID: "bad"}, Fence: Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}} {
		if s.Validate() == nil {
			t.Fatalf("invalid scope accepted: %+v", s)
		}
	}
	epoch, _ := NewEpoch(7)
	scope := Scope{Capture: ref, Fence: Fence{ArchiveEpoch: 7, CaptureGeneration: 2}}
	if scope.Validate() != nil {
		t.Fatal("valid scope rejected")
	}
	token, err := NewScopeToken(epoch, scope.Capture, scope.Fence)
	if err != nil {
		t.Fatal(err)
	}
	if epoch.Value() != 7 || token.Stamp().Scope() != scope {
		t.Fatal("scope coordinates lost")
	}
	epoch.Advance(8)
	if epoch.Value() != 8 || token.Stamp().Scope() != scope || token.Stamp().Current() {
		t.Fatal("epoch invalidation changed immutable scope")
	}
	if (Scope{Fence: Fence{ArchiveEpoch: 1}}).Validate() != nil {
		t.Fatal("global scope rejected")
	}
}
