package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestDeterministicAcceptanceIsolatesConfiguredSecretReference(t *testing.T) {
	t.Parallel()

	input := config{
		deterministicOnly: true,
		secretRef:         "secret://provider/configured-development-key",
	}
	first, err := isolateDeterministicSecret(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := isolateDeterministicSecret(input)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "secret://provider/m0-assembly-"
	if !strings.HasPrefix(first.secretRef, prefix) ||
		!strings.HasPrefix(second.secretRef, prefix) ||
		first.secretRef == input.secretRef ||
		second.secretRef == input.secretRef ||
		first.secretRef == second.secretRef {
		t.Fatalf(
			"isolated references first=%q second=%q configured=%q",
			first.secretRef,
			second.secretRef,
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
