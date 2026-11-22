package protocolpath

import (
	"errors"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func pendingFixture(t *testing.T) (PendingTerminal, protocolcore.ToolCall) {
	t.Helper()
	call := testCall(t, "call_1", "Bash", `{"command":"ls"}`)
	response := terminalResponse(protocolcore.StopReasonToolUse, protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: call})
	report := protocolcore.NewTranslationReport(protocolcore.TranslationNotice{Code: protocolcore.NoticeRefusalRenderedAsText, Path: "$"})
	release := []byte("held")
	terminal := NewPendingTerminal(release, response, []protocolcore.ToolIntent{{ResponseID: "resp_terminal", Call: call}}, report)
	release[0] = 'X'
	return terminal, call
}

func TestPendingTerminalOwnsItsValues(t *testing.T) {
	t.Parallel()
	terminal, _ := pendingFixture(t)
	intents := terminal.ToolIntents()
	intents[0].Call.Name = "changed"
	response := terminal.DecodedResponse()
	response.Blocks[0].ToolCall.Name = "changed"
	if terminal.ToolIntents()[0].Call.Name != "Bash" || terminal.DecodedResponse().Blocks[0].ToolCall.Name != "Bash" ||
		terminal.BorrowDecodedResponse().ID != "resp_terminal" {
		t.Fatal("pending terminal exposed mutable state")
	}
	if notices := terminal.TranslationReport().Notices(); len(notices) != 1 {
		t.Fatalf("report = %+v", notices)
	}
	release, err := terminal.Approve()
	if err != nil || string(release) != "held" {
		t.Fatalf("Approve() = %q, %v (the caller's buffer was aliased)", release, err)
	}
}

func TestPendingTerminalDecidesOnce(t *testing.T) {
	t.Parallel()
	approved, _ := pendingFixture(t)
	if _, err := approved.Approve(); err != nil {
		t.Fatal(err)
	}
	if _, err := approved.Approve(); !errors.Is(err, ErrTerminalAlreadyDecided) {
		t.Fatalf("second Approve() error = %v", err)
	}
	if err := approved.Reject(); !errors.Is(err, ErrTerminalAlreadyDecided) {
		t.Fatalf("Reject() after Approve() error = %v", err)
	}
	rejected, _ := pendingFixture(t)
	if err := rejected.Reject(); err != nil {
		t.Fatal(err)
	}
	if release, err := rejected.Approve(); !errors.Is(err, ErrTerminalAlreadyDecided) || release != nil {
		t.Fatalf("Approve() after Reject() = %q, %v", release, err)
	}
	// The response stays readable for observation after the decision.
	if rejected.BorrowDecodedResponse().ID != "resp_terminal" {
		t.Fatal("decided terminal lost its response")
	}
}

func TestPendingTerminalConcurrentDecisionHasOneWinner(t *testing.T) {
	t.Parallel()
	terminal, _ := pendingFixture(t)
	var group sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func(approve bool) {
			defer group.Done()
			var err error
			if approve {
				_, err = terminal.Approve()
			} else {
				err = terminal.Reject()
			}
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(index%2 == 0)
	}
	group.Wait()
	if wins != 1 {
		t.Fatalf("%d decisions won", wins)
	}
}
