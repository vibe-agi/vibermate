package capturecontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// These cover the decision itself: whether this launch carries the Root. The
// route that consults it is covered by the CaptureRun control tests, and the
// whole path by the live agent runs.

// recordingApprover answers a fixed way and remembers what it was asked.
type recordingApprover struct {
	allow    bool
	fail     error
	requests []toolapproval.ClientRootAskRequest
}

func (approver *recordingApprover) AskClientRoot(
	_ context.Context,
	request toolapproval.ClientRootAskRequest,
) (toolapproval.ClientRootAskOutcome, error) {
	approver.requests = append(approver.requests, request)
	if approver.fail != nil {
		return toolapproval.ClientRootAskOutcome{}, approver.fail
	}
	return toolapproval.ClientRootAskOutcome{Allowed: approver.allow}, nil
}

func recognizedSigner() clientadapter.SignerEvidence {
	return clientadapter.SignerEvidence{
		ID:              "codex-cli",
		Revision:        1,
		CatalogRevision: 1,
		InstallShape:    clientadapter.InstallNPMWrapperNativeChild,
		LaunchRecipe:    clientadapter.LaunchSSLCertFile,
		SignedPath:      "/opt/example/bin/codex",
	}
}

func TestARecognizedClientReceivesTheRootOnlyAfterAnAllow(t *testing.T) {
	t.Parallel()

	signer := recognizedSigner()
	approver := &recordingApprover{allow: true}
	granted, err := (&Handler{rootAsk: approver}).askClientRoot(
		context.Background(), signer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !granted {
		t.Fatal("an allowed recognized client was refused the Root")
	}
	if len(approver.requests) != 1 {
		t.Fatalf("the person was asked %d times", len(approver.requests))
	}
	asked := approver.requests[0]
	if asked.SignerID != signer.ID ||
		asked.SignerRevision != uint64(signer.Revision) ||
		asked.SignedPath != signer.SignedPath {
		t.Fatalf("the question did not describe what was recognized: %+v", asked)
	}
}

func TestARecognizedClientIsRefusedTheRootOnADeny(t *testing.T) {
	t.Parallel()

	approver := &recordingApprover{allow: false}
	granted, err := (&Handler{rootAsk: approver}).askClientRoot(
		context.Background(), recognizedSigner(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if granted {
		t.Fatal("a denied recognized client was given the Root")
	}
}

// A launch that cannot reach anybody launches without a Root. Handing it out
// because a prompt could not be shown would be worse than not asking.
func TestNobodyToAskMeansNoRoot(t *testing.T) {
	t.Parallel()

	granted, err := (&Handler{}).askClientRoot(
		context.Background(), recognizedSigner(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if granted {
		t.Fatal("the Root was handed out with nothing able to ask")
	}
}

func TestAFailedAskMeansNoRoot(t *testing.T) {
	t.Parallel()

	approver := &recordingApprover{fail: errors.New("the approval store is down")}
	granted, _ := (&Handler{rootAsk: approver}).askClientRoot(
		context.Background(), recognizedSigner(),
	)
	if granted {
		t.Fatal("a failed ask handed out the Root")
	}
}
