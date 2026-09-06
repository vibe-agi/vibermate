package exchange

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestRejectedRequestRetainsExplicitSessionAtTerminal(t *testing.T) {
	admission, err := captureadmission.NewManagedRun(captureadmission.ManagedRunEvidence{
		CaptureRunID: "run-session", SourceLabel: "codex", WorkspaceRoot: "/workspace/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := correlatedRequest(t, "manual-fixture", "connection-fixture", WithClientProtocolEvidence([]protocolcore.ProtocolEvidenceValue{
		{Name: "openai_responses.session_id", Value: "session-exact"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	// The request body cannot be decoded. Only the already validated ingress
	// metadata is available to the terminal projection.
	request.admission = admission
	ref := terminalConversationRef(request, nil)
	if ref.Evidence != agentconversation.EvidenceExplicitSession {
		t.Fatalf("terminal request lost session: %#v", ref)
	}
}
