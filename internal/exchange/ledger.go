package exchange

import (
	"errors"
	"sync"
)

type RetryBlockReason string

const (
	RetryAllowed                    RetryBlockReason = ""
	RetryBlockedPolicy              RetryBlockReason = "retry_policy_disabled"
	RetryBlockedReplayClass         RetryBlockReason = "replay_class_not_safe"
	RetryBlockedEnvelopeMissing     RetryBlockReason = "hold_envelope_not_committed"
	RetryBlockedOrdinaryHeaders     RetryBlockReason = "ordinary_headers_committed"
	RetryBlockedDownstreamSemantics RetryBlockReason = "downstream_semantics_committed"
	RetryBlockedToolExposure        RetryBlockReason = "downstream_tool_exposed"
	RetryBlockedTerminal            RetryBlockReason = "downstream_terminal_committed"
	RetryBlockedFailure             RetryBlockReason = "downstream_failure_committed"
)

type ledgerState struct {
	upstreamSends            uint32
	upstreamResponses        uint32
	upstreamBodyBytes        int64
	downstreamHoldEnvelope   bool
	downstreamOrdinaryHeader bool
	downstreamSemanticBytes  int64
	downstreamSemanticWrites uint32
	downstreamToolKeys       []string
	downstreamTerminal       bool
	downstreamFailure        bool
}

// CommitLedger separates possible provider processing from client-visible
// semantic commitment. Every mutation occurs only after the corresponding
// boundary reports success or committed bytes.
type CommitLedger struct {
	mu    sync.Mutex
	state ledgerState
}

type LedgerSnapshot struct {
	UpstreamSends             uint32
	UpstreamResponses         uint32
	UpstreamBodyBytes         int64
	DownstreamHoldEnvelope    bool
	DownstreamOrdinaryHeaders bool
	DownstreamSemanticBytes   int64
	DownstreamSemanticWrites  uint32
	DownstreamTerminal        bool
	DownstreamFailure         bool
	downstreamToolKeys        []string
}

func (snapshot LedgerSnapshot) DownstreamToolKeys() []string {
	return cloneStrings(snapshot.downstreamToolKeys)
}

func (ledger *CommitLedger) RecordUpstreamSend(bodyBytes int64) error {
	if bodyBytes < 0 {
		return errors.New("upstream body byte count is negative")
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.state.upstreamSends++
	ledger.state.upstreamBodyBytes += bodyBytes
	return nil
}

func (ledger *CommitLedger) RecordUpstreamResponse() {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.state.upstreamResponses++
}

func (ledger *CommitLedger) RecordHoldEnvelope() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.state.downstreamHoldEnvelope {
		return errors.New("downstream Hold envelope is already committed")
	}
	if ledger.state.downstreamOrdinaryHeader {
		return errors.New("ordinary downstream headers are already committed")
	}
	ledger.state.downstreamHoldEnvelope = true
	return nil
}

func (ledger *CommitLedger) RecordOrdinaryHeaders() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.state.downstreamOrdinaryHeader {
		return errors.New("ordinary downstream headers are already committed")
	}
	if ledger.state.downstreamHoldEnvelope {
		return errors.New("downstream Hold envelope is already committed")
	}
	ledger.state.downstreamOrdinaryHeader = true
	return nil
}

func (ledger *CommitLedger) RecordSemanticWrite(bytes int) error {
	if bytes <= 0 {
		return errors.New("semantic commit byte count must be positive")
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.state.downstreamTerminal || ledger.state.downstreamFailure {
		return errors.New("cannot commit semantics after a terminal state")
	}
	ledger.state.downstreamSemanticBytes += int64(bytes)
	ledger.state.downstreamSemanticWrites++
	return nil
}

// RecordToolExposure is conservative: any committed byte from a batch that
// contains complete tool calls blocks replay and records every call in that
// decision group.
func (ledger *CommitLedger) RecordToolExposure(toolKeys []string) error {
	if len(toolKeys) == 0 {
		return errors.New("tool exposure contains no tool keys")
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.state.downstreamTerminal || ledger.state.downstreamFailure {
		return errors.New("cannot expose a tool after a terminal state")
	}
	known := make(map[string]struct{}, len(ledger.state.downstreamToolKeys)+len(toolKeys))
	for _, key := range ledger.state.downstreamToolKeys {
		known[key] = struct{}{}
	}
	for _, key := range toolKeys {
		if key == "" {
			return errors.New("tool exposure contains an empty key")
		}
		if _, duplicate := known[key]; duplicate {
			continue
		}
		known[key] = struct{}{}
		ledger.state.downstreamToolKeys = append(
			ledger.state.downstreamToolKeys,
			key,
		)
	}
	return nil
}

func (ledger *CommitLedger) RecordTerminal() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.state.downstreamTerminal || ledger.state.downstreamFailure {
		return errors.New("downstream terminal state is already committed")
	}
	ledger.state.downstreamTerminal = true
	return nil
}

func (ledger *CommitLedger) RecordFailure() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.state.downstreamTerminal || ledger.state.downstreamFailure {
		return errors.New("downstream terminal state is already committed")
	}
	ledger.state.downstreamFailure = true
	return nil
}

func (ledger *CommitLedger) CanTransportResend(
	class ReplayClass,
	explicitlyAllowed bool,
) (bool, RetryBlockReason) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	switch {
	case !explicitlyAllowed:
		return false, RetryBlockedPolicy
	case !class.allowsTransportResend():
		return false, RetryBlockedReplayClass
	case !ledger.state.downstreamHoldEnvelope:
		return false, RetryBlockedEnvelopeMissing
	case ledger.state.downstreamOrdinaryHeader:
		return false, RetryBlockedOrdinaryHeaders
	case ledger.state.downstreamSemanticBytes != 0 ||
		ledger.state.downstreamSemanticWrites != 0:
		return false, RetryBlockedDownstreamSemantics
	case len(ledger.state.downstreamToolKeys) != 0:
		return false, RetryBlockedToolExposure
	case ledger.state.downstreamTerminal:
		return false, RetryBlockedTerminal
	case ledger.state.downstreamFailure:
		return false, RetryBlockedFailure
	default:
		return true, RetryAllowed
	}
}

func (ledger *CommitLedger) Snapshot() LedgerSnapshot {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return LedgerSnapshot{
		UpstreamSends:             ledger.state.upstreamSends,
		UpstreamResponses:         ledger.state.upstreamResponses,
		UpstreamBodyBytes:         ledger.state.upstreamBodyBytes,
		DownstreamHoldEnvelope:    ledger.state.downstreamHoldEnvelope,
		DownstreamOrdinaryHeaders: ledger.state.downstreamOrdinaryHeader,
		DownstreamSemanticBytes:   ledger.state.downstreamSemanticBytes,
		DownstreamSemanticWrites:  ledger.state.downstreamSemanticWrites,
		DownstreamTerminal:        ledger.state.downstreamTerminal,
		DownstreamFailure:         ledger.state.downstreamFailure,
		downstreamToolKeys: cloneStrings(
			ledger.state.downstreamToolKeys,
		),
	}
}
