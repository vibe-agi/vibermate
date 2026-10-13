package productruntime

import (
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRuntimeNativeInheritedVLESSChoiceStoreReceipt(t *testing.T) {
	for _, choice := range []string{"global_vless", "global_vless_large"} {
		t.Run(choice, func(t *testing.T) {
			f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: choice})
			f.mode = "matrix"
			f.handshake(t)
			baseline := runtimeNativeCounts{DirectAttempts: 3, DirectConnections: 3, TLS: 3, HTTP: 3}
			assertNativeCounts(t, f, baseline)
			want := runtimeNativeExpected{"profile.native-a", 1, egressprofile.SelectionGlobal, 2}
			call := f.beginPhase(t, "POST", `{"jsonrpc":"2.0","id":45,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "", want, &baseline)
			f.allow(t, call.ID)
			if got := f.join(t, call); got.Outcome != nativemcp.OutcomeCompleted {
				t.Fatalf("native VLESS call: %+v", got)
			}
			f.assertAudit(t, call.ID, want, "completed", "")
			assertNativeMethods(t, f, 1, 0, 0, 4)
			assertNativeCounts(t, f, runtimeNativeCounts{DirectAttempts: 4, DirectConnections: 4, TLS: 4, HTTP: 4})
			if replay := f.r.nativeTools.Serve(t.Context(), call.Captured, httptest.NewRecorder()); replay.Outcome != nativemcp.OutcomeRejected || replay.Reason != nativemcp.ReasonProtocolInvalid {
				t.Fatalf("replayed captured request: %+v", replay)
			}
			scope, err := f.r.NativeToolRepository().GetScope(t.Context(), f.capture)
			if err != nil {
				t.Fatal(err)
			}
			if receipt, err := f.r.NativeToolRepository().Start(t.Context(), call.ID, "fresh-replay-attempt", scope.Revision, time.Now().UTC()); !errors.Is(err, nativetool.ErrReplay) || receipt != (nativetool.StartReceipt{}) {
				t.Fatalf("replayed Start: %+v %v", receipt, err)
			}
			assertNativeMethods(t, f, 1, 0, 0, 4)
		})
	}
}
