package protocolpath

import (
	"bytes"
	"errors"
	"sync"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// ErrTerminalAlreadyDecided is returned by a second Approve or Reject.
var ErrTerminalAlreadyDecided = errors.New("stream terminal was already decided")

type pendingDecision uint8

const (
	pendingUndecided pendingDecision = iota
	pendingApproved
	pendingRejected
)

type pendingTerminal struct {
	mu       sync.Mutex
	release  []byte
	response protocolcore.Response
	intents  []protocolcore.ToolIntent
	report   protocolcore.TranslationReport
	decision pendingDecision
}

// NewPendingTerminal is a generic PendingTerminal: release holds the client
// bytes withheld until the tool decision, which is taken exactly once.
// Approve returns them; Reject discards them.
func NewPendingTerminal(
	release []byte,
	response protocolcore.Response,
	intents []protocolcore.ToolIntent,
	report protocolcore.TranslationReport,
) PendingTerminal {
	cloned := make([]protocolcore.ToolIntent, len(intents))
	for index, intent := range intents {
		cloned[index] = intent.Clone()
	}
	return &pendingTerminal{
		release:  bytes.Clone(release),
		response: response.Clone(),
		intents:  cloned,
		report:   report,
	}
}

func (terminal *pendingTerminal) ToolIntents() []protocolcore.ToolIntent {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	intents := make([]protocolcore.ToolIntent, len(terminal.intents))
	for index, intent := range terminal.intents {
		intents[index] = intent.Clone()
	}
	return intents
}

func (terminal *pendingTerminal) DecodedResponse() protocolcore.Response {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.response.Clone()
}

func (terminal *pendingTerminal) BorrowDecodedResponse() protocolcore.Response {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.response
}

func (terminal *pendingTerminal) TranslationReport() protocolcore.TranslationReport {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.report
}

func (terminal *pendingTerminal) Approve() ([]byte, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.decision != pendingUndecided {
		return nil, ErrTerminalAlreadyDecided
	}
	terminal.decision = pendingApproved
	release := terminal.release
	terminal.release = nil
	return release, nil
}

func (terminal *pendingTerminal) Reject() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.decision != pendingUndecided {
		return ErrTerminalAlreadyDecided
	}
	terminal.decision = pendingRejected
	terminal.release = nil
	return nil
}
