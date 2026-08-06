package main

import (
	"context"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acceptancereport"
	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestProducerMatchesTheCurrentVerifierCheckContract(t *testing.T) {
	t.Parallel()
	for _, mode := range []acceptancereport.Mode{
		acceptancereport.ModeDeterministic,
		acceptancereport.ModeCredentialed,
	} {
		mode := mode
		for _, client := range []acceptanceClient{
			{ID: acceptanceClientClaudeCode, Version: "2.1.220"},
			{ID: acceptanceClientCodexCLI, Version: "0.145.0"},
		} {
			client := client
			t.Run(string(mode)+"/"+string(client.ID), func(t *testing.T) {
				t.Parallel()
				report := newReport(time.Unix(1, 0), client)
				required, err := acceptancereport.RequiredCheckIDs(
					mode,
					string(client.ID),
					client.Version,
				)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range required {
					report.add(id, checkPassed, "passed")
				}
				if err := requireCurrentCheckContract(report, mode); err != nil {
					t.Fatal(err)
				}

				report.Checks = report.Checks[:len(report.Checks)-1]
				if err := requireCurrentCheckContract(report, mode); err == nil {
					t.Fatal("incomplete producer check set was accepted")
				}
			})
		}
	}
}

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
				(!strings.Contains(spec.prompt, "await tools.exec_command") ||
					!strings.Contains(spec.prompt, "workdir:") ||
					!strings.Contains(spec.prompt, "VIBEMATE_TOOL_EXEC_OK") ||
					!strings.Contains(
						spec.prompt,
						"Treat its output as success and do not call any tool again",
					)) {
				t.Fatalf(
					"Codex proof specification omitted typed exec orchestration: %+v",
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
				ClientID:  acceptanceClientClaudeCode,
				ToolName:  "Write",
				Approved:  true,
				Completed: true,
			},
			wantDetail: "Write remained behind the durable allow-once barrier and produced the bounded proof file before the client completed normally",
		},
		{
			name: "Codex exec",
			evidence: toolApprovalEvidence{
				ClientID:              acceptanceClientCodexCLI,
				ToolName:              "exec",
				Approved:              true,
				InterruptedAfterProof: true,
			},
			wantDetail: "exec remained behind the durable allow-once barrier and produced the bounded proof file before the captured client was deliberately interrupted",
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
		ClientID:              acceptanceClientCodexCLI,
		ToolName:              "Write",
		Approved:              true,
		InterruptedAfterProof: true,
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
			kind:   offlinehold.EgressProvider,
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

func TestClaudeDeferredStartupRequiresExactBodylessControlPair(
	t *testing.T,
) {
	t.Parallel()

	held := offlinehold.Snapshot{
		State:            offlinehold.StateHeld,
		SafeToDisconnect: true,
		ActiveActions:    2,
		QueuedRequests:   2,
		QueuedByKind: map[offlinehold.EgressKind]int{
			offlinehold.EgressOpaque: 2,
		},
	}
	if err := requireClaudeStartupControlEgress(held, true); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*offlinehold.Snapshot){
		"third request": func(snapshot *offlinehold.Snapshot) {
			snapshot.ActiveActions = 3
			snapshot.QueuedRequests = 3
			snapshot.QueuedByKind[offlinehold.EgressOpaque] = 3
		},
		"request body": func(snapshot *offlinehold.Snapshot) {
			snapshot.HeldBytes = 1
		},
		"provider request": func(snapshot *offlinehold.Snapshot) {
			snapshot.QueuedByKind[offlinehold.EgressOpaque] = 1
			snapshot.QueuedByKind[offlinehold.EgressProvider] = 1
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			candidate := held
			candidate.QueuedByKind = maps.Clone(held.QueuedByKind)
			mutate(&candidate)
			if err := requireClaudeStartupControlEgress(
				candidate,
				true,
			); err == nil {
				t.Fatalf("unexpected startup snapshot accepted: %+v", candidate)
			}
		})
	}

	releasing := offlinehold.Snapshot{
		State:         offlinehold.StateReleasing,
		ActiveActions: 2,
		ActiveEgress:  2,
		ActiveByKind: map[offlinehold.EgressKind]int{
			offlinehold.EgressOpaque: 2,
		},
	}
	if err := requireClaudeStartupControlRelease(releasing); err != nil {
		t.Fatal(err)
	}
	releasing.QueuedRequests = 1
	releasing.QueuedByKind = map[offlinehold.EgressKind]int{
		offlinehold.EgressOpaque: 1,
	}
	if err := requireClaudeStartupControlRelease(releasing); err == nil {
		t.Fatal("extra queued Claude startup request was accepted")
	}
}

func TestHeldPreflightRequiresExactlyOneClientSpecificEgress(t *testing.T) {
	t.Parallel()

	held := offlinehold.Snapshot{
		State:            offlinehold.StateHeld,
		SafeToDisconnect: true,
		QueuedRequests:   1,
		HeldBytes:        128,
		QueuedByKind: map[offlinehold.EgressKind]int{
			offlinehold.EgressProvider: 1,
		},
	}
	if err := requireSingleHeldQueuedEgress(
		held,
		offlinehold.EgressProvider,
	); err != nil {
		t.Fatal(err)
	}
	released := offlinehold.Snapshot{
		State:        offlinehold.StateReleasing,
		ActiveEgress: 1,
		ActiveByKind: map[offlinehold.EgressKind]int{
			offlinehold.EgressProvider: 1,
		},
	}
	if err := requireSingleReleasedEgress(
		released,
		offlinehold.EgressProvider,
	); err != nil {
		t.Fatal(err)
	}

	held.QueuedRequests = 2
	held.QueuedByKind[offlinehold.EgressOpaque] = 1
	if err := requireSingleHeldQueuedEgress(
		held,
		offlinehold.EgressProvider,
	); err == nil {
		t.Fatal("multiple held egresses passed preflight isolation")
	}
	released.ActiveEgress = 2
	released.ActiveByKind[offlinehold.EgressOpaque] = 1
	if err := requireSingleReleasedEgress(
		released,
		offlinehold.EgressProvider,
	); err == nil {
		t.Fatal("multiple released egresses passed preflight isolation")
	}
}

func TestExpectedExchangeFailureRequiresExactlyOneTerminal(t *testing.T) {
	t.Parallel()

	expected := exchangeAuditRecord{
		Sequence:   10,
		AccessID:   "Acc-001",
		ExchangeID: "exchange-expected",
		Status:     activity.StatusFailed,
		ReasonCode: string(exchange.ReasonProviderCredentialUnavailable),
	}
	record, exists, err := singleExpectedExchangeFailure(
		[]exchangeAuditRecord{expected},
		"Acc-001",
		exchange.ReasonProviderCredentialUnavailable,
	)
	if err != nil || !exists || record != expected {
		t.Fatalf("single expected failure=%+v exists=%t err=%v", record, exists, err)
	}

	for name, records := range map[string][]exchangeAuditRecord{
		"extra success": {
			expected,
			{
				Sequence:   11,
				AccessID:   "Acc-001",
				ExchangeID: "exchange-extra",
				Status:     activity.StatusSucceeded,
			},
		},
		"wrong terminal": {{
			Sequence:   10,
			AccessID:   "Acc-001",
			ExchangeID: "exchange-wrong",
			Status:     activity.StatusCanceled,
			ReasonCode: "exchange_canceled",
		}},
	} {
		records := records
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := singleExpectedExchangeFailure(
				records,
				"Acc-001",
				exchange.ReasonProviderCredentialUnavailable,
			); err == nil {
				t.Fatal("non-unique or mismatched Exchange terminal was accepted")
			}
		})
	}
}

func TestSuccessfulExchangeEvidenceCountsDistinctPostBaselineSubjects(
	t *testing.T,
) {
	t.Parallel()

	records := []exchangeAuditRecord{
		{
			Sequence:   9,
			AccessID:   "Acc-001",
			ExchangeID: "exchange-before",
			Status:     activity.StatusSucceeded,
		},
		{
			Sequence:   10,
			AccessID:   "Acc-001",
			ExchangeID: "exchange-first",
			Status:     activity.StatusSucceeded,
		},
		{
			Sequence:   11,
			AccessID:   "Acc-001",
			ExchangeID: "exchange-first",
			Status:     activity.StatusSucceeded,
		},
		{
			Sequence:   12,
			AccessID:   "Acc-002",
			ExchangeID: "exchange-other",
			Status:     activity.StatusSucceeded,
		},
		{
			Sequence:   13,
			AccessID:   "Acc-001",
			ExchangeID: "exchange-failed",
			Status:     activity.StatusFailed,
		},
		{
			Sequence:   14,
			AccessID:   "Acc-001",
			ExchangeID: "exchange-second",
			Status:     activity.StatusSucceeded,
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
		clientID:          acceptanceClientClaudeCode,
		accessID:          "Acc-001",
		deterministicOnly: false,
		providerOrigin:    "https://provider.example.test/v1",
		providerModel:     "fixed-model",
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
	for _, phases := range []acceptancePhases{first, second} {
		deterministic, deterministicErr := assemblyAccess(phases.deterministic, 0)
		if deterministicErr != nil {
			t.Fatal(deterministicErr)
		}
		credentialed, credentialedErr := assemblyAccess(phases.credentialed, 1)
		if credentialedErr != nil {
			t.Fatal(credentialedErr)
		}
		if deterministic.AccountBindings[0].ID != "Acc-001-account" ||
			credentialed.AccountBindings[0].ID != "Acc-001-account" ||
			credentialed.AccountBindings[0].SecretRef != input.secretRef {
			t.Fatalf(
				"phase bindings deterministic=%+v credentialed=%+v",
				deterministic.AccountBindings,
				credentialed.AccountBindings,
			)
		}
	}
}

func TestCodexApprovedToolProofRequiresOneCompletedToolAndExactProof(t *testing.T) {
	t.Parallel()

	workingDirectory := t.TempDir()
	spec, err := newToolApprovalSpec(
		acceptanceClientCodexCLI,
		workingDirectory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		spec.proofPath,
		[]byte(spec.proofContent),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	run := &agentRun{
		clientID: acceptanceClientCodexCLI,
		done:     make(chan struct{}),
		toolUses: 1,
	}
	if err := waitForCodexApprovedToolProof(
		context.Background(),
		run,
		spec,
	); err != nil {
		t.Fatal(err)
	}

	run.toolUses = 2
	if err := waitForCodexApprovedToolProof(
		context.Background(),
		run,
		spec,
	); err == nil {
		t.Fatal("two completed tools passed one allow-once proof")
	}

	run.clientID = acceptanceClientClaudeCode
	if err := waitForCodexApprovedToolProof(
		context.Background(),
		run,
		spec,
	); err == nil {
		t.Fatal("non-Codex run passed Codex proof")
	}
}

func TestToolApprovalSelectionIgnoresOtherKindsAndPreexistingRows(t *testing.T) {
	t.Parallel()

	current := toolapproval.View{
		ID:            "approval-current",
		Kind:          string(toolapproval.KindToolIntent),
		State:         toolapproval.StatePending,
		ExchangeID:    "exchange-current",
		AccessID:      "Acc-001",
		PlanRevision:  1,
		PlanHash:      strings.Repeat("a", 64),
		SubjectLabels: []string{"exec"},
		CreatedAt:     time.Unix(3, 0),
	}
	page := toolapproval.Page{Items: []toolapproval.View{
		current,
		{
			ID:            "approval-network",
			Kind:          string(toolapproval.KindNetworkAsk),
			State:         toolapproval.StatePending,
			SubjectLabels: []string{"chatgpt.com"},
			CreatedAt:     time.Unix(1, 0),
		},
		{
			ID:            "approval-old-tool",
			Kind:          string(toolapproval.KindToolIntent),
			State:         toolapproval.StatePending,
			ExchangeID:    "exchange-old",
			AccessID:      "Acc-001",
			PlanRevision:  1,
			PlanHash:      strings.Repeat("b", 64),
			SubjectLabels: []string{"exec"},
			CreatedAt:     time.Unix(2, 0),
		},
	}}
	selected, found, err := selectToolApproval(
		page,
		map[string]struct{}{"approval-old-tool": {}},
		"Acc-001",
		"exec",
	)
	if err != nil || !found || selected.ID != current.ID {
		t.Fatalf("selected approval = %+v found=%t err=%v", selected, found, err)
	}

	wrong := current
	wrong.SubjectLabels = []string{"shell"}
	if _, _, err := selectToolApproval(
		toolapproval.Page{Items: []toolapproval.View{wrong}},
		nil,
		"Acc-001",
		"exec",
	); err == nil {
		t.Fatal("a new tool approval for a different tool was ignored")
	}
	if _, found, err := selectToolApproval(
		toolapproval.Page{Items: page.Items[1:2]},
		nil,
		"Acc-001",
		"exec",
	); err != nil || found {
		t.Fatalf("network-only page found=%t err=%v", found, err)
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

func TestClientConnectionAuditRequiresCompleteImmutableTimeline(
	t *testing.T,
) {
	t.Parallel()

	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	timeline := clientAuditTimeline(t)
	ready, err := clientConnectionAuditReady(
		timeline,
		clientOrigin,
	)
	if err != nil || !ready {
		t.Fatalf("complete timeline ready=%t error=%v", ready, err)
	}

	nonterminal := timeline
	nonterminal.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events[:len(timeline.Events)-1]...,
	)
	ready, err = clientConnectionAuditReady(
		nonterminal,
		clientOrigin,
	)
	if err != nil || ready {
		t.Fatalf("nonterminal timeline ready=%t error=%v", ready, err)
	}

	providerContaminated := timeline
	providerContaminated.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events...,
	)
	providerContaminated.Events[2].RouteHost = "relay.example.test"
	providerContaminated.Events[2].CredentialBindingID = "assembly-001-account"
	if ready, err = clientConnectionAuditReady(
		providerContaminated,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf("provider-contaminated timeline ready=%t error=%v", ready, err)
	}

	downgraded := timeline
	downgraded.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events...,
	)
	downgraded.Events[1].SourceConfidence =
		connectionevent.SourceConfidenceConfigured
	if ready, err = clientConnectionAuditReady(
		downgraded,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf("downgraded timeline ready=%t error=%v", ready, err)
	}

	wrongRule := timeline
	wrongRule.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events...,
	)
	wrongRule.Events[1].RuleID = "other.rule"
	if ready, err = clientConnectionAuditReady(
		wrongRule,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf("wrong-rule timeline ready=%t error=%v", ready, err)
	}

	contaminatedAttempt := timeline
	contaminatedAttempt.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events...,
	)
	contaminatedAttempt.Events[0].RouteHost = "relay.example.test"
	contaminatedAttempt.Events[0].CredentialBindingID = "assembly-001-account"
	if ready, err = clientConnectionAuditReady(
		contaminatedAttempt,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf("contaminated attempt ready=%t error=%v", ready, err)
	}
}

func TestActiveClientConnectionAuditRequiresVerifiedClientSidePrefix(
	t *testing.T,
) {
	t.Parallel()

	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	timeline := clientAuditTimeline(t)
	active := timeline
	active.Events = append(
		[]connectionevent.Record(nil),
		timeline.Events[:len(timeline.Events)-1]...,
	)
	ready, err := activeClientConnectionAuditReady(active, clientOrigin)
	if err != nil || !ready {
		t.Fatalf("active timeline ready=%t error=%v", ready, err)
	}

	terminalReady, terminalErr := activeClientConnectionAuditReady(
		timeline,
		clientOrigin,
	)
	if terminalErr != nil || terminalReady {
		t.Fatalf(
			"terminal timeline ready=%t error=%v",
			terminalReady,
			terminalErr,
		)
	}

	downgraded := active
	downgraded.Events = append(
		[]connectionevent.Record(nil),
		active.Events...,
	)
	downgraded.Events[2].SourceConfidence =
		connectionevent.SourceConfidenceConfigured
	if ready, err = activeClientConnectionAuditReady(
		downgraded,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf("downgraded timeline ready=%t error=%v", ready, err)
	}

	wrongHostShape := active
	wrongHostShape.Events = append(
		[]connectionevent.Record(nil),
		active.Events...,
	)
	wrongHostShape.Events[2].RequestedHost = "api.anthropic.com:443"
	if ready, err = activeClientConnectionAuditReady(
		wrongHostShape,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf("wrong-host timeline ready=%t error=%v", ready, err)
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

	downgradedAttribution := append(
		[]connectionevent.Record(nil),
		records...,
	)
	for index := range downgradedAttribution {
		if downgradedAttribution[index].SourceConfidence ==
			connectionevent.SourceConfidenceVerified {
			downgradedAttribution[index].SourceConfidence =
				connectionevent.SourceConfidenceConfigured
		}
	}
	if ready, err = responsesHTTPFallbackAuditReady(
		downgradedAttribution,
		clientOrigin,
	); err == nil || ready {
		t.Fatalf(
			"configured fallback ready=%t error=%v",
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
			agentStatus: http.StatusUpgradeRequired,
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
	if evidence.ClientHTTPStatus != http.StatusUpgradeRequired ||
		evidence.RuntimeReason !=
			exchange.ReasonProviderCredentialUnavailable ||
		!evidence.ConnectionAudit {
		t.Fatalf("fallback evidence = %+v", evidence)
	}
	detail, err := evidence.reportDetail()
	if err != nil {
		t.Fatal(err)
	}
	if detail != "fixed Codex surfaced HTTP 426, the proxy audit proved the bounded transition to HTTP, and Runtime Activity bound that request to provider_credential_unavailable" {
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
			RequestedHost:    "api.openai.com",
			Port:             443,
			Decryption:       connectionevent.DecryptionNone,
			Phase:            connectionevent.PhaseAttempted,
			StartedAt:        started.Add(offset),
		}
		decided := attempt
		decided.IngressID = "run-001"
		decided.SourceLabel = "codex.js"
		decided.SourceConfidence = connectionevent.SourceConfidenceVerified
		decided.RouteHost = "api.openai.com"
		decided.Decision = connectionevent.DecisionAllow
		decided.RuleID = "agent_endpoint_exact"
		decided.EgressScope = connectionevent.EgressScopeAccess
		decided.EgressSource = connectionevent.EgressSourceAccessDefault
		decided.EgressPolicyRevision = 1
		decided.Decryption = connectionevent.DecryptionMITM
		decided.AccessID = "assembly-access"
		decided.AccessName = "Assembly Access"
		decided.AccessRevision = 1
		decided.AgentEndpointID = "assembly-agent-endpoint"
		decided.AgentEndpointRevision = 1
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

func clientAuditTimeline(t *testing.T) connectionevent.Timeline {
	t.Helper()
	started := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	attempt := connectionevent.Event{
		ConnectionID:     "connection-001",
		SourceConfidence: connectionevent.SourceConfidenceUnknown,
		RequestedHost:    "api.anthropic.com",
		Port:             443,
		Decryption:       connectionevent.DecryptionNone,
		Phase:            connectionevent.PhaseAttempted,
		StartedAt:        started,
	}
	decided := attempt
	decided.IngressID = "run-001"
	decided.SourceLabel = "claude"
	decided.SourceConfidence = connectionevent.SourceConfidenceVerified
	decided.RouteHost = "api.anthropic.com"
	decided.Decision = connectionevent.DecisionAllow
	decided.RuleID = acceptanceConnectionRuleID
	decided.EgressScope = connectionevent.EgressScopeAccess
	decided.EgressSource = connectionevent.EgressSourceAccessDefault
	decided.EgressPolicyRevision = 1
	decided.Decryption = connectionevent.DecryptionMITM
	decided.AccessID = "assembly-access"
	decided.AccessName = "Assembly Access"
	decided.AccessRevision = 1
	decided.AgentEndpointID = "assembly-agent-endpoint"
	decided.AgentEndpointRevision = 1
	decided.Phase = connectionevent.PhaseDecided
	clientConnected := decided
	clientConnected.ObservedSNI = "api.anthropic.com"
	clientConnected.Phase = connectionevent.PhaseConnected
	terminal := clientConnected
	terminal.Phase = connectionevent.PhaseClosed
	terminal.Outcome = connectionevent.OutcomeCompleted
	terminal.BytesUp = 1024
	terminal.BytesDown = 2048
	terminal.EndedAt = started.Add(time.Second)
	events := []connectionevent.Event{
		attempt,
		decided,
		clientConnected,
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
