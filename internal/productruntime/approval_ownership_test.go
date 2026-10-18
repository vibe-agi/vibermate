package productruntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestRuntimeOwnedToolApprovalIgnoresUnavailableRecording(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{recoverEgress: func(context.Context, egressaudit.Repository, time.Time) error {
		return errors.New("synthetic unavailable optional history")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, r)
	dir := t.TempDir()
	grant, err := r.CaptureRuns().Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := r.captureRuns.AuthorizeProxy(t.Context(), grant.ProxyCapability)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte(`{}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := protocolcore.NewCallKey("openai-chat", "call-1")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := exchange.NewToolDecisionContext(environment.DefaultPolicySet(), "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewToolDecisionRequest("approval-without-history", environment.EnvironmentID("env.test"), 1, environment.CandidateDigest{1}, environment.UpstreamRouteID("route.test"), 1, policy, []protocolcore.ToolIntent{{ResponseID: "response-1", Ordinal: 0, Call: protocolcore.ToolCall{Key: key, Name: "read_file", Arguments: arguments}}})
	if err != nil {
		t.Fatal(err)
	}
	scopeRequest, ok := any(request).(interface {
		WithOriginalScope(recording.Scope) (exchange.ToolDecisionRequest, error)
	})
	if !ok {
		t.Fatal("required ToolDecisionRequest cannot carry original ownership")
	}
	request, err = scopeRequest.WithOriginalScope(admission.RecordingScope)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		decision exchange.ToolDecision
		err      error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { d, e := r.approvals.Decide(ctx, request); done <- result{d, e} }()
	var pending []toolapproval.Record
	deadline := time.Now().Add(2 * time.Second)
	for len(pending) == 0 && time.Now().Before(deadline) {
		pending, err = r.storage.ToolApprovalRepository().List(t.Context(), toolapproval.PageRequest{State: toolapproval.StatePending, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if len(pending) != 1 || pending[0].OwnerKind != toolapproval.OwnershipCapture || pending[0].OwnerScope != admission.RecordingScope {
		t.Fatal("approval lost required original ownership while recording unavailable")
	}
	if r.recordingStorage.available() {
		t.Fatal("fixture history became available")
	}
	if err = r.CaptureRuns().Finish(t.Context(), grant.Run.ID, grant.ControlCapability); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.decision.Outcome != exchange.ToolDecisionRejected {
			t.Fatalf("owned waiter survived Finish: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Finish did not release the existing owned approval waiter")
	}
	if _, err = r.approvals.Decide(t.Context(), request); err == nil {
		t.Fatal("late original-owner request created authority after Finish")
	}
}
