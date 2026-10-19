//go:build vibermate_acceptance_synthetic

package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func nativeQueueInput(q *observationRecorder) (recording.Stamp, nativemcp.ContentInput) {
	stamp := q.ScopeStamp(captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "recording-run"})
	now := time.Now().UTC().Truncate(time.Millisecond)
	identity := nativetool.ContentIdentity{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Scope: stamp.Scope(), EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: environment.CandidateDigest{1}, Kind: nativetool.RequestToolsCall, ToolName: "synthetic_read", TargetOrigin: "https://native.example", CredentialOwner: "original", Recording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	return stamp, nativemcp.AcceptanceSyntheticContentInput(identity, nativetool.ContentToolResult, []byte(`{"_meta":{"private":true},"content":[{"type":"text","text":"safe","_meta":{"private":true}}]}`))
}
func TestNativeContentAdmissionSkipsOptionalPreparation(t *testing.T) {
	for _, mode := range []string{"unavailable", "items", "bytes", "stale", "closed"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
			q := newObservationRecorder(context.Background(), b, nil, nil)
			defer q.Shutdown(context.Background())
			stamp, input := nativeQueueInput(q)
			owner, err := runtimepersistence.OpenObservations(context.Background(), runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "native-admission", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Shutdown(context.Background())
			q.nativeContent = owner
			want := recording.DroppedCapacity
			switch mode {
			case "unavailable":
				q.nativeContent = nil
				want = recording.DroppedUnavailable
			case "items":
				r, _ := b.TryReserve(recording.Raw, 0)
				defer r.Release()
			case "bytes":
				r, _ := b.TryReserve(recording.Raw, 64<<20)
				defer r.Release()
			case "stale":
				q.epoch.Advance(2)
				want = recording.DroppedStale
			case "closed":
				q.mu.Lock()
				q.closed = true
				q.mu.Unlock()
				want = recording.DroppedClosed
			}
			var got recording.OfferResult
			allocations := testing.AllocsPerRun(20, func() { got = q.ObserveNativeContent(stamp, input) })
			if got != want || allocations != 0 {
				t.Fatalf("optional sanitizer entered: result=%v want=%v allocations=%v", got, want, allocations)
			}
		})
	}
}

func TestNativeQueuedContentHasNoProducerAliases(t *testing.T) {
	for _, part := range []nativetool.ContentPart{nativetool.ContentToolArguments, nativetool.ContentToolResult} {
		t.Run(string(part), func(t *testing.T) {
			ctx := context.Background()
			owner, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "native-owned", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Shutdown(ctx)
			budget, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
			q := newObservationRecorder(ctx, budget, nil, nil)
			defer q.Shutdown(ctx)
			sink := &heldNativeContentStore{owner: owner, entered: make(chan struct{}), release: make(chan struct{})}
			q.nativeContent = sink
			var once sync.Once
			release := func() { once.Do(func() { close(sink.release) }) }
			defer release()
			stamp := q.ScopeStamp(captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "recording-run"})
			now := time.Now().UTC().Truncate(time.Millisecond)
			toolBytes := []byte("original-tool")
			captureBytes := []byte("recording-run")
			identity := nativetool.ContentIdentity{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Scope: stamp.Scope(), EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: environment.CandidateDigest{1}, Kind: nativetool.RequestToolsCall, ToolName: unsafe.String(&toolBytes[0], len(toolBytes)), TargetOrigin: "https://native.example", CredentialOwner: "original", Recording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
			identity.Scope.Capture.ID = unsafe.String(&captureBytes[0], len(captureBytes))
			raw := []byte(`{"path":"public"}`)
			if part == nativetool.ContentToolResult {
				raw = []byte(`{"content":[{"type":"text","text":"public","_meta":{"private":true}}]}`)
			}
			in := nativemcp.AcceptanceSyntheticContentInput(identity, part, raw)
			if got := q.ObserveNativeContent(stamp, in); got != recording.Accepted {
				t.Fatalf("offer %v", got)
			}
			<-sink.entered
			clear(raw)
			clear(toolBytes)
			clear(captureBytes)
			if budget.Snapshot().ReservedRecords != 1 {
				t.Fatal("actual writer no longer owns reservation")
			}
			release()
			if err := q.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := owner.GetNativeContent(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", part)
			if err != nil || got.Identity.ToolName != "original-tool" || got.Identity.Scope.Capture.ID != "recording-run" || !strings.Contains(string(got.Body), "public") || strings.Contains(string(got.Body), "private") {
				t.Fatalf("producer aliases reached persistence: %+v %v", got, err)
			}
			if budget.Snapshot().ReservedRecords != 0 {
				t.Fatal("settled ownership leaked")
			}
		})
	}
}
