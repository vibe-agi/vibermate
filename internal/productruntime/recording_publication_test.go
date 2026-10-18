package productruntime

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func TestRuntimeOwnerPublishesAndRefreshesCompleteMirror(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	assertReadable := func() {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := r.connectionInspection.List(context.Background(), connectionevent.PageRequest{Limit: 20}); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("actual runtime inspection stayed unavailable without production mirror publication")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	assertReadable()
	if _, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "new fence", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked}); err != nil {
		t.Fatal(err)
	}
	policy, err := r.UsageRepository().UsagePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policy.RetentionDays = 7
	if _, err = r.UsageRepository().SetUsagePolicy(t.Context(), policy, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	assertReadable()
}
