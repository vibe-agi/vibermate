package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestWaitForActiveProviderEgressRequiresAnAdmittedProviderExchange(
	t *testing.T,
) {
	t.Parallel()

	reader := &scriptedOfflineReader{snapshots: []offlinehold.Snapshot{
		{
			State:         offlinehold.StateOnline,
			ActiveActions: 1,
			ActiveEgress:  1,
			ActiveByKind: map[offlinehold.EgressKind]int{
				offlinehold.EgressAuxiliary: 1,
			},
		},
		{
			State:         offlinehold.StateOnline,
			ActiveActions: 1,
			ActiveEgress:  1,
			ActiveByKind: map[offlinehold.EgressKind]int{
				offlinehold.EgressProvider: 1,
			},
		},
	}}
	run := &agentRun{
		done:       make(chan struct{}),
		outputDone: make(chan struct{}),
		clientID:   acceptanceClientCodexCLI,
	}
	if err := waitForActiveProviderEgress(
		context.Background(),
		reader,
		run,
		time.Second,
	); err != nil {
		t.Fatal(err)
	}
	if calls := reader.callCount(); calls != 2 {
		t.Fatalf("offline snapshot calls = %d, want 2", calls)
	}
}

func TestWaitForActiveProviderEgressStopsWhenTheAgentExits(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	close(done)
	outputDone := make(chan struct{})
	close(outputDone)
	run := &agentRun{
		done:       done,
		outputDone: outputDone,
		clientID:   acceptanceClientCodexCLI,
	}
	reader := &scriptedOfflineReader{snapshots: []offlinehold.Snapshot{{
		State: offlinehold.StateOnline,
	}}}
	started := time.Now()
	err := waitForActiveProviderEgress(
		context.Background(),
		reader,
		run,
		time.Second,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"exited before provider egress became active",
	) {
		t.Fatalf("early Agent exit error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("early Agent exit observed after %s", elapsed)
	}
}

type scriptedOfflineReader struct {
	mu        sync.Mutex
	snapshots []offlinehold.Snapshot
	calls     int
}

func (reader *scriptedOfflineReader) offline(
	context.Context,
) (offlinehold.Snapshot, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	index := reader.calls
	reader.calls++
	if index >= len(reader.snapshots) {
		index = len(reader.snapshots) - 1
	}
	return reader.snapshots[index], nil
}

func (reader *scriptedOfflineReader) callCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

func TestToolApprovalSpecUsesTheSelectedClientToolAndBoundedProof(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		clientID acceptanceClientID
		toolName string
	}{
		{
			name:     "Claude",
			clientID: acceptanceClientClaudeCode,
			toolName: "Write",
		},
		{
			name:     "Codex",
			clientID: acceptanceClientCodexCLI,
			toolName: "exec",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			workingDirectory := t.TempDir()
			spec, err := newToolApprovalSpec(
				test.clientID,
				workingDirectory,
			)
			if err != nil {
				t.Fatal(err)
			}
			if spec.toolName != test.toolName {
				t.Fatalf(
					"tool name = %q, want %q",
					spec.toolName,
					test.toolName,
				)
			}
			if filepath.Dir(spec.proofPath) != workingDirectory {
				t.Fatalf("proof path = %q", spec.proofPath)
			}
			if spec.proofContent == "" ||
				!strings.Contains(spec.prompt, spec.proofPath) ||
				!strings.Contains(spec.prompt, spec.proofContent) {
				t.Fatalf("invalid proof specification: %+v", spec)
			}
			if test.clientID == acceptanceClientCodexCLI &&
				(!strings.Contains(spec.prompt, "VIBEMATE_TOOL_EXEC_OK") ||
					!strings.Contains(
						spec.prompt,
						"Treat that output as success and do not call any tool again",
					)) {
				t.Fatalf(
					"Codex proof specification omitted deterministic success output: %+v",
					spec,
				)
			}
			if err := verifyToolApprovalProof(spec); err == nil {
				t.Fatal("missing proof passed verification")
			}
			if err := os.WriteFile(
				spec.proofPath,
				[]byte(spec.proofContent),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if err := verifyToolApprovalProof(spec); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				spec.proofPath,
				[]byte(spec.proofContent+"unexpected"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if err := verifyToolApprovalProof(spec); err == nil {
				t.Fatal("unexpected proof content passed verification")
			}
		})
	}
}

func TestAcceptanceReportDetailsMatchTheObservedClientEvidence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		evidence   toolApprovalEvidence
		wantDetail string
	}{
		{
			name: "Claude Write",
			evidence: toolApprovalEvidence{
				ClientID: acceptanceClientClaudeCode,
				ToolName: "Write",
				Approved: true,
			},
			wantDetail: "Write remained behind the durable allow-once barrier and produced the bounded proof file",
		},
		{
			name: "Codex exec",
			evidence: toolApprovalEvidence{
				ClientID: acceptanceClientCodexCLI,
				ToolName: "exec",
				Approved: true,
			},
			wantDetail: "exec remained behind the durable allow-once barrier and produced the bounded proof file",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			detail, err := test.evidence.reportDetail()
			if err != nil {
				t.Fatal(err)
			}
			if detail != test.wantDetail {
				t.Fatalf("report detail = %q", detail)
			}
		})
	}

	if _, err := (toolApprovalEvidence{
		ClientID: acceptanceClientCodexCLI,
		ToolName: "Write",
		Approved: true,
	}).reportDetail(); err == nil {
		t.Fatal("mismatched Codex tool produced report evidence")
	}

	claudeDetail, err := (heldStreamingEvidence{
		ClientID:     acceptanceClientClaudeCode,
		ClientDeltas: 2,
		Completed:    true,
	}).reportDetail()
	if err != nil {
		t.Fatal(err)
	}
	if claudeDetail !=
		"held request resumed and returned multiple streamed client deltas" {
		t.Fatalf("Claude streaming detail = %q", claudeDetail)
	}

	codexDetail, err := (heldStreamingEvidence{
		ClientID:     acceptanceClientCodexCLI,
		ClientDeltas: 0,
		Completed:    true,
	}).reportDetail()
	if err != nil {
		t.Fatal(err)
	}
	if codexDetail !=
		"held request resumed and completed through the Responses streaming path" {
		t.Fatalf("Codex streaming detail = %q", codexDetail)
	}
	for _, forbidden := range []string{"multiple", "delta", "TUI", "token"} {
		if strings.Contains(codexDetail, forbidden) {
			t.Fatalf("Codex detail overclaimed %q: %q", forbidden, codexDetail)
		}
	}

	if _, err := (heldStreamingEvidence{
		ClientID:     acceptanceClientClaudeCode,
		ClientDeltas: 1,
		Completed:    true,
	}).reportDetail(); err == nil {
		t.Fatal("insufficient Claude deltas produced report evidence")
	}
}

func TestToolApprovalSpecRejectsRelativeWorkingDirectory(t *testing.T) {
	t.Parallel()

	if _, err := newToolApprovalSpec(
		acceptanceClientClaudeCode,
		"relative",
	); err == nil {
		t.Fatal("relative working directory was accepted")
	}
}

func TestHeldPreflightUsesTheClientSpecificEgressBoundary(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		client acceptanceClientID
		kind   offlinehold.EgressKind
	}{
		{
			client: acceptanceClientClaudeCode,
			kind:   offlinehold.EgressOpaque,
		},
		{
			client: acceptanceClientCodexCLI,
			kind:   offlinehold.EgressProvider,
		},
	} {
		if kind, err := heldPreflightEgressKind(test.client); err != nil ||
			kind != test.kind {
			t.Fatalf(
				"client %q kind=%q error=%v",
				test.client,
				kind,
				err,
			)
		}
	}
	if _, err := heldPreflightEgressKind(
		acceptanceClientID("unknown"),
	); err == nil {
		t.Fatal("unknown client preflight boundary was accepted")
	}
}

func TestSuccessfulExchangeEvidenceCountsDistinctPostBaselineSubjects(
	t *testing.T,
) {
	t.Parallel()

	records := []activity.Record{
		{
			Sequence:  9,
			Kind:      activity.KindExchangeCompleted,
			AccessID:  "Acc-001",
			SubjectID: "exchange-before",
			Status:    activity.StatusSucceeded,
		},
		{
			Sequence:  10,
			Kind:      activity.KindExchangeCompleted,
			AccessID:  "Acc-001",
			SubjectID: "exchange-first",
			Status:    activity.StatusSucceeded,
		},
		{
			Sequence:  11,
			Kind:      activity.KindExchangeCompleted,
			AccessID:  "Acc-001",
			SubjectID: "exchange-first",
			Status:    activity.StatusSucceeded,
		},
		{
			Sequence:  12,
			Kind:      activity.KindExchangeCompleted,
			AccessID:  "Acc-002",
			SubjectID: "exchange-other",
			Status:    activity.StatusSucceeded,
		},
		{
			Sequence:  13,
			Kind:      activity.KindExchangeCompleted,
			AccessID:  "Acc-001",
			SubjectID: "exchange-failed",
			Status:    activity.StatusFailed,
		},
		{
			Sequence:  14,
			Kind:      activity.KindExchangeCompleted,
			AccessID:  "Acc-001",
			SubjectID: "exchange-second",
			Status:    activity.StatusSucceeded,
		},
	}
	subjects := successfulExchangeSubjectsAfter(
		records,
		"Acc-001",
		9,
	)
	if len(subjects) != 2 ||
		subjects[0] != "exchange-first" ||
		subjects[1] != "exchange-second" {
		t.Fatalf("successful Exchange subjects = %v", subjects)
	}
}

func TestAcceptancePhasesAlwaysIsolateConfiguredSecretReference(t *testing.T) {
	t.Parallel()

	input := config{
		deterministicOnly: false,
		secretRef:         "secret://provider/configured-development-key",
	}
	first, err := splitAcceptancePhases(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := splitAcceptancePhases(input)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "secret://provider/m0-assembly-"
	if !strings.HasPrefix(first.deterministic.secretRef, prefix) ||
		!strings.HasPrefix(second.deterministic.secretRef, prefix) ||
		first.deterministic.secretRef == input.secretRef ||
		second.deterministic.secretRef == input.secretRef ||
		first.deterministic.secretRef == second.deterministic.secretRef ||
		first.credentialed != input ||
		second.credentialed != input {
		t.Fatalf(
			"isolated references first=%q second=%q configured=%q",
			first.deterministic.secretRef,
			second.deterministic.secretRef,
			input.secretRef,
		)
	}
}

func TestQueuedKindSummaryIsStableAndRequiresCompleteAccounting(t *testing.T) {
	t.Parallel()

	summary, err := queuedKindSummary(offlinehold.Snapshot{
		QueuedRequests: 3,
		QueuedByKind: map[offlinehold.EgressKind]int{
			offlinehold.EgressOpaque:   1,
			offlinehold.EgressProvider: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "provider:2,opaque:1" {
		t.Fatalf("summary = %q", summary)
	}

	for name, snapshot := range map[string]offlinehold.Snapshot{
		"missing count": {
			QueuedRequests: 2,
			QueuedByKind: map[offlinehold.EgressKind]int{
				offlinehold.EgressProvider: 1,
			},
		},
		"unknown kind": {
			QueuedRequests: 1,
			QueuedByKind: map[offlinehold.EgressKind]int{
				offlinehold.EgressKind("unknown"): 1,
			},
		},
		"negative count": {
			QueuedRequests: 1,
			QueuedByKind: map[offlinehold.EgressKind]int{
				offlinehold.EgressProvider: -1,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, summaryErr := queuedKindSummary(snapshot); summaryErr == nil {
				t.Fatalf("snapshot %+v produced no error", snapshot)
			}
		})
	}
}

func TestProviderConnectionAuditRequiresCompleteImmutableTimeline(
	t *testing.T,
) {
	t.Parallel()

	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := access.NewProviderOrigin("https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	timeline := providerAuditTimeline(t)
	ready, err := providerConnectionAuditReady(
		timeline,
		clientOrigin,
		providerOrigin,
		"assembly-001-account",
	)
	if err != nil || !ready {
		t.Fatalf("complete timeline ready=%t error=%v", ready, err)
	}

	nonterminal := timeline
	nonterminal.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events[:len(timeline.Events)-1]...,
	)
	ready, err = providerConnectionAuditReady(
		nonterminal,
		clientOrigin,
		providerOrigin,
		"assembly-001-account",
	)
	if err != nil || ready {
		t.Fatalf("nonterminal timeline ready=%t error=%v", ready, err)
	}

	wrongCredential := timeline
	wrongCredential.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events...,
	)
	wrongCredential.Events[3].CredentialBindingID = "other-account"
	if ready, err = providerConnectionAuditReady(
		wrongCredential,
		clientOrigin,
		providerOrigin,
		"assembly-001-account",
	); err == nil || ready {
		t.Fatalf("wrong credential timeline ready=%t error=%v", ready, err)
	}
}

func TestResponsesHTTPFallbackAuditRequiresBoundedNegotiationAndActiveHTTP(
	t *testing.T,
) {
	t.Parallel()

	clientOrigin, err := access.NewClientOrigin("https://api.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	records := responsesHTTPFallbackAuditRecords(t)
	ready, err := responsesHTTPFallbackAuditReady(records, clientOrigin)
	if err != nil || !ready {
		t.Fatalf("complete fallback ready=%t error=%v", ready, err)
	}

	onlyNegotiation := append(
		[]connectionevent.Record(nil),
		records[:4]...,
	)
	ready, err = responsesHTTPFallbackAuditReady(
		onlyNegotiation,
		clientOrigin,
	)
	if err != nil || ready {
		t.Fatalf("negotiation-only ready=%t error=%v", ready, err)
	}

	wrongRun := append([]connectionevent.Record(nil), records...)
	wrongRun[6].IngressID = "run-other"
	if ready, err = responsesHTTPFallbackAuditReady(
		wrongRun,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf("cross-run fallback ready=%t error=%v", ready, err)
	}

	credentialedNegotiation := append(
		[]connectionevent.Record(nil),
		records...,
	)
	credentialedNegotiation[3].CredentialBindingID = "provider-account"
	if ready, err = responsesHTTPFallbackAuditReady(
		credentialedNegotiation,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf(
			"credentialed negotiation ready=%t error=%v",
			ready,
			err,
		)
	}
}

func TestCodexHTTPFallbackEvidenceRequiresClientOutcomeAndConnectionAudit(
	t *testing.T,
) {
	t.Parallel()

	clientEvent := &agentRun{
		clientID: acceptanceClientCodexCLI,
		failure: agentFailureEvidence{
			agentStatus: http.StatusBadGateway,
		},
		changed: make(chan struct{}),
	}
	evidence, err := completeCodexHTTPFallbackEvidence(
		context.Background(),
		clientEvent,
		codexHTTPFallbackEvidence{
			RuntimeReason:   exchange.ReasonProviderCredentialUnavailable,
			ConnectionAudit: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ClientHTTPStatus != http.StatusBadGateway ||
		evidence.RuntimeReason !=
			exchange.ReasonProviderCredentialUnavailable ||
		!evidence.ConnectionAudit {
		t.Fatalf("fallback evidence = %+v", evidence)
	}
	detail, err := evidence.reportDetail()
	if err != nil {
		t.Fatal(err)
	}
	if detail != "fixed Codex reported HTTP 502 for the fallback request, runtime Activity bound it to provider_credential_unavailable, and the proxy audit proved the bounded 426-to-HTTP transition" {
		t.Fatalf("fallback detail = %q", detail)
	}

	evidence, err = completeCodexHTTPFallbackEvidence(
		context.Background(),
		clientEvent,
		codexHTTPFallbackEvidence{},
	)
	if err == nil || evidence.ClientHTTPStatus != 0 ||
		evidence.RuntimeReason != "" || evidence.ConnectionAudit {
		t.Fatalf("partial fallback evidence = %+v error=%v", evidence, err)
	}

	missingEvent := &agentRun{
		clientID: acceptanceClientCodexCLI,
		changed:  make(chan struct{}),
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	evidence, err = completeCodexHTTPFallbackEvidence(
		cancelled,
		missingEvent,
		codexHTTPFallbackEvidence{
			RuntimeReason:   exchange.ReasonProviderCredentialUnavailable,
			ConnectionAudit: true,
		},
	)
	if err == nil || evidence.ClientHTTPStatus != 0 ||
		evidence.RuntimeReason !=
			exchange.ReasonProviderCredentialUnavailable ||
		!evidence.ConnectionAudit {
		t.Fatalf(
			"missing client event evidence = %+v error=%v",
			evidence,
			err,
		)
	}
}

func responsesHTTPFallbackAuditRecords(
	t *testing.T,
) []connectionevent.Record {
	t.Helper()
	started := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	connection := func(
		identifier string,
		offset time.Duration,
		terminal bool,
	) []connectionevent.Event {
		attempt := connectionevent.Event{
			ConnectionID:     identifier,
			SourceConfidence: connectionevent.SourceConfidenceUnknown,
			RequestedHost:    "api.openai.com:443",
			Port:             443,
			Decryption:       connectionevent.DecryptionNone,
			Phase:            connectionevent.PhaseAttempted,
			StartedAt:        started.Add(offset),
		}
		decided := attempt
		decided.IngressID = "run-001"
		decided.SourceLabel = "codex.js"
		decided.SourceConfidence = connectionevent.SourceConfidenceConfigured
		decided.RouteHost = "api.openai.com"
		decided.Decision = connectionevent.DecisionAllow
		decided.RuleID = "agent_endpoint_exact"
		decided.EgressScope = connectionevent.EgressScopeAccess
		decided.EgressSource = connectionevent.EgressSourceAccessDefault
		decided.EgressPolicyRevision = 1
		decided.Decryption = connectionevent.DecryptionMITM
		decided.Phase = connectionevent.PhaseDecided
		connected := decided
		connected.ObservedSNI = "api.openai.com"
		connected.Phase = connectionevent.PhaseConnected
		events := []connectionevent.Event{attempt, decided, connected}
		if terminal {
			closed := connected
			closed.BytesUp = 2048
			closed.BytesDown = 2048
			closed.Phase = connectionevent.PhaseClosed
			closed.Outcome = connectionevent.OutcomeCompleted
			closed.EndedAt = started.Add(offset + time.Second)
			events = append(events, closed)
		}
		return events
	}
	events := append(
		connection("connection-negotiation", 0, true),
		connection("connection-http", 2*time.Second, false)...,
	)
	records := make([]connectionevent.Record, len(events))
	for index, event := range events {
		record := connectionevent.Record{
			Sequence: int64(index + 1),
			Event:    event,
		}
		if err := record.Validate(); err != nil {
			t.Fatalf("fixture event %d: %v", index, err)
		}
		records[index] = record
	}
	return records
}

func providerAuditTimeline(t *testing.T) connectionevent.Timeline {
	t.Helper()
	started := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	attempt := connectionevent.Event{
		ConnectionID:     "connection-001",
		SourceConfidence: connectionevent.SourceConfidenceUnknown,
		RequestedHost:    "api.anthropic.com:443",
		Port:             443,
		Decryption:       connectionevent.DecryptionNone,
		Phase:            connectionevent.PhaseAttempted,
		StartedAt:        started,
	}
	decided := attempt
	decided.IngressID = "run-001"
	decided.SourceLabel = "claude"
	decided.SourceConfidence = connectionevent.SourceConfidenceConfigured
	decided.RouteHost = "api.anthropic.com"
	decided.Decision = connectionevent.DecisionAllow
	decided.RuleID = "m0.agent_endpoint_exact"
	decided.EgressScope = connectionevent.EgressScopeAccess
	decided.EgressSource = connectionevent.EgressSourceAccessDefault
	decided.EgressPolicyRevision = 1
	decided.Decryption = connectionevent.DecryptionMITM
	decided.Phase = connectionevent.PhaseDecided
	clientConnected := decided
	clientConnected.ObservedSNI = "api.anthropic.com"
	clientConnected.Phase = connectionevent.PhaseConnected
	providerConnected := clientConnected
	providerConnected.RouteHost = "api.example.com"
	providerConnected.CredentialBindingID = "assembly-001-account"
	terminal := providerConnected
	terminal.Phase = connectionevent.PhaseClosed
	terminal.Outcome = connectionevent.OutcomeCompleted
	terminal.EndedAt = started.Add(time.Second)
	events := []connectionevent.Event{
		attempt,
		decided,
		clientConnected,
		providerConnected,
		terminal,
	}
	timeline := connectionevent.Timeline{
		ConnectionID: attempt.ConnectionID,
		Events:       make([]connectionevent.Record, len(events)),
	}
	for index, event := range events {
		record := connectionevent.Record{
			Sequence: int64(index + 1),
			Event:    event,
		}
		if err := record.Validate(); err != nil {
			t.Fatalf("fixture event %d: %v", index, err)
		}
		timeline.Events[index] = record
	}
	return timeline
}
