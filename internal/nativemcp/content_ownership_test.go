package nativemcp

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func nativeOwnedInput(raw []byte, part nativetool.ContentPart) (recording.Stamp, ContentInput) {
	epoch, _ := recording.NewEpoch(1)
	capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "owned-run"}
	stamp, _ := recording.NewStamp(epoch, capture, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
	now := time.Now().UTC().Truncate(time.Millisecond)
	return stamp, ContentInput{identity: nativetool.ContentIdentity{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Scope: stamp.Scope(), EnvironmentID: "environment", EnvironmentRevision: 1, EnvironmentDigest: environment.CandidateDigest{1}, Kind: nativetool.RequestToolsCall, ToolName: "read", TargetOrigin: "https://native.example", CredentialOwner: "original", Recording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, observedAt: now, part: part, raw: raw, profile: ContentProfile{contract: environment.ClientToolContractCodexLegacy, definition: nativetool.Digest{1}}, limits: DefaultLimits()}
}
func TestNativeContentOwnershipSanitizesAndClosesBorrow(t *testing.T) {
	raw := []byte(`{"_meta":{"transport":"private"},"content":[{"type":"resource","_meta":{"transport":"private"},"resource":{"uri":"file:///a","mimeType":"text/plain","text":"public","_meta":{"transport":"private"}}}],"structuredContent":{"_meta":{"business":"kept"}}}`)
	stamp, in := nativeOwnedInput(raw, nativetool.ContentToolResult)
	budget, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
	reservation, _ := budget.TryReserve(recording.Native, 4096)
	defer reservation.Release()
	owned, err := OwnContentWithin(stamp, in, reservation)
	if err != nil {
		t.Fatal(err)
	}
	clear(raw)
	in.identity.ToolName = "changed"
	got := owned.Observation()
	if bytes.Contains(got.Body, []byte("private")) || !bytes.Contains(got.Body, []byte(`"business":"kept"`)) || !bytes.Contains(got.Body, []byte("public")) || got.Identity.ToolName != "read" {
		t.Fatalf("retained projection: %+v %s", got.Identity, got.Body)
	}
	if owned.RetainedBytes() == 0 || budget.Snapshot().ReservedRecords != 1 {
		t.Fatal("owned accounting missing")
	}
}
func TestNativeContentOwnershipRefusesWithoutLeakingReservation(t *testing.T) {
	raw := []byte(`{"_meta":{"transport":"private"},"content":[{"type":"text","text":"` + strings.Repeat("x", 16384) + `","_meta":{"private":true}}]}`)
	stamp, in := nativeOwnedInput(raw, nativetool.ContentToolResult)
	in.profile.result = []string{"/content/0/missing"}
	states := map[int64]bool{}
	for _, capacity := range []int64{4096, 8192, 32768, 65536, 131072, 262144, 524288, 1048576, 2097152} {
		b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: capacity})
		r, _ := b.TryReserve(recording.Native, 4096)
		owned, err := OwnContentWithin(stamp, in, r)
		if err != nil && !errors.Is(err, ErrContentOwnershipCapacity) {
			t.Fatalf("capacity %d: %v", capacity, err)
		}
		if err == nil && owned == nil {
			t.Fatal("missing owned body")
		}
		if b.Snapshot().ReservedRecords != 1 {
			t.Fatal("helper took caller reservation")
		}
		states[b.Snapshot().ReservedBytes] = true
		r.Release()
		if b.Snapshot().ReservedRecords != 0 || b.Snapshot().ReservedBytes != 0 {
			t.Fatal("refused growth leaked")
		}
	}
	if len(states) < 4 {
		t.Fatalf("growth refusal matrix did not reach enough preparation phases: %v", states)
	}
}
func TestNativeContentOwnershipSensitivePathsAndOriginalScope(t *testing.T) {
	for _, test := range []struct{ body, path string }{{`{"a/b":{"~key":"private"}}`, "/a~1b/~0key"}, {`{"items":[{"proof":"private"}]}`, "/items/0/proof"}} {
		stamp, in := nativeOwnedInput([]byte(test.body), nativetool.ContentToolArguments)
		in.profile.arguments = []string{test.path}
		b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
		r, _ := b.TryReserve(recording.Native, 4096)
		owned, err := OwnContentWithin(stamp, in, r)
		r.Release()
		if err == nil || owned != nil {
			t.Fatal("sensitive argument retained")
		}
	}
	stamp, in := nativeOwnedInput([]byte(`{"public":true}`), nativetool.ContentToolArguments)
	in.identity.Scope.Fence.ArchiveEpoch++
	b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
	r, _ := b.TryReserve(recording.Native, 4096)
	defer r.Release()
	if owned, err := OwnContentWithin(stamp, in, r); err == nil || owned != nil {
		t.Fatal("original scope replaced")
	}
}

func TestNativeContentPreparationAllocationsFitReservation(t *testing.T) {
	for _, body := range []string{
		`{"content":[{"type":"text","text":"` + strings.Repeat("<&>", 2048) + `","_meta":{"transport":true}}]}`,
		`{"content":[{"type":"resource","resource":{"uri":"file:///a","text":"` + strings.Repeat("x", 32768) + `","_meta":{"transport":true}}}]}`,
		`{"content":[` + strings.TrimSuffix(strings.Repeat(`{"type":"text","text":"x","_meta":{}},`, 128), ",") + `]}`,
	} {
		stamp, input := nativeOwnedInput([]byte(body), nativetool.ContentToolResult)
		input.profile.result = []string{"/content/0/missing"}
		b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
		reservation, _ := b.TryReserve(recording.Native, 4096)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		owned, err := OwnContentWithin(stamp, input, reservation)
		runtime.ReadMemStats(&after)
		charged := b.Snapshot().ReservedBytes
		reservation.Release()
		if err != nil || owned == nil {
			t.Fatalf("preparation refused bounded fixture: %v", err)
		}
		if allocation := after.TotalAlloc - before.TotalAlloc; allocation > uint64(charged) {
			t.Fatalf("preparation allocation %d exceeded reservation %d", allocation, charged)
		}
	}
}
