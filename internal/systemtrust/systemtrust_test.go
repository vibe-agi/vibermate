package systemtrust

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localca"
)

func TestMacOSCurrentUserTargetUsesOnlyTheCurrentLoginScope(t *testing.T) {
	target := MacOSCurrentUserTarget()
	if target.TrustSettingsDomain() != TrustSettingsDomainUser {
		t.Fatalf("unexpected trust domain: %q", target.TrustSettingsDomain())
	}
	if target.CertificateKeychain() != CertificateKeychainUserSearchList {
		t.Fatalf("unexpected certificate search scope: %q", target.CertificateKeychain())
	}
	if target.Usage() != TrustUsageServerTLS || !target.valid() {
		t.Fatalf("unexpected current-user target: %+v", target)
	}
}

type mutableRootSource struct {
	mu   sync.Mutex
	root publicRoot
	err  error
}

func (source *mutableRootSource) currentPublicRoot(
	ctx context.Context,
) (publicRoot, error) {
	if ctx == nil {
		return publicRoot{}, ErrCurrentRootInvalid
	}
	if err := ctx.Err(); err != nil {
		return publicRoot{}, context.Cause(ctx)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.root.clone(), source.err
}

func (source *mutableRootSource) replace(root publicRoot) {
	source.mu.Lock()
	source.root = root.clone()
	source.mu.Unlock()
}

type machineState struct {
	presence ExactPresence
	decision TrustDecision
}

type machineExecutor struct {
	mu sync.Mutex

	root       publicRoot
	state      machineState
	foreignDER [][]byte
	calls      []CommandSpec

	invalidPresence      bool
	invalidTrust         bool
	invalidAfterMutation bool
	mutationResults      map[CommandKind][]CommandOutcome
	skipEffects          map[CommandKind]bool
	effectOnFailure      map[CommandKind]bool

	blockKind        CommandKind
	blockEntered     chan struct{}
	blockRelease     chan struct{}
	blockUntilCancel bool
	blockOnce        sync.Once

	artifactChecked bool
	artifactError   error
}

func newMachineExecutor(root publicRoot, state machineState) *machineExecutor {
	return &machineExecutor{
		root:            root.clone(),
		state:           state,
		mutationResults: make(map[CommandKind][]CommandOutcome),
		skipEffects:     make(map[CommandKind]bool),
		effectOnFailure: make(map[CommandKind]bool),
	}
}

func (executor *machineExecutor) Execute(
	ctx context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	executor.mu.Lock()
	executor.calls = append(executor.calls, CommandSpec{
		kind:       spec.kind,
		executable: spec.executable,
		arguments:  slices.Clone(spec.arguments),
	})
	state := executor.state
	invalidPresence := executor.invalidPresence
	invalidTrust := executor.invalidTrust
	foreign := cloneByteSlices(executor.foreignDER)
	block := spec.kind == executor.blockKind
	blockEntered := executor.blockEntered
	blockRelease := executor.blockRelease
	blockUntilCancel := executor.blockUntilCancel
	if block {
		executor.blockOnce.Do(func() {
			if blockEntered != nil {
				close(blockEntered)
			}
		})
	}
	executor.mu.Unlock()

	if block {
		if blockUntilCancel {
			<-ctx.Done()
			return commandResult(CommandOutcomeIndeterminate, nil), context.Cause(ctx)
		}
		select {
		case <-blockRelease:
		case <-ctx.Done():
			return commandResult(CommandOutcomeIndeterminate, nil), context.Cause(ctx)
		}
	}

	switch spec.kind {
	case CommandInspectExactPresence:
		if invalidPresence {
			return commandResult(CommandOutcomeSucceeded, []byte("invalid fixture")), nil
		}
		var output []byte
		if state.presence == ExactPresencePresent {
			output = append(output, certificatePEM(executor.root.certificateDER)...)
		}
		for _, der := range foreign {
			output = append(output, certificatePEM(der)...)
		}
		return commandResult(CommandOutcomeSucceeded, output), nil
	case CommandInspectUserTrust:
		if invalidTrust {
			return commandResult(CommandOutcomeSucceeded, []byte(`{"schema":"unknown"}`)), nil
		}
		return commandResult(
			CommandOutcomeSucceeded,
			trustFixtureOutput(executor.root, state.decision),
		), nil
	case CommandEnsureExactTrust,
		CommandRemoveExactTrust,
		CommandDeleteExactObject:
		executor.checkMutationSpec(spec)
		executor.mu.Lock()
		outcome := CommandOutcomeSucceeded
		queued := executor.mutationResults[spec.kind]
		if len(queued) != 0 {
			outcome = queued[0]
			executor.mutationResults[spec.kind] = queued[1:]
		}
		apply := !executor.skipEffects[spec.kind] &&
			(outcome == CommandOutcomeSucceeded || executor.effectOnFailure[spec.kind])
		if apply {
			switch spec.kind {
			case CommandEnsureExactTrust:
				executor.state = machineState{
					presence: ExactPresencePresent,
					decision: TrustDecisionTrusted,
				}
			case CommandRemoveExactTrust:
				executor.state = machineState{
					presence: ExactPresencePresent,
					decision: TrustDecisionUntrusted,
				}
			case CommandDeleteExactObject:
				executor.state = machineState{
					presence: ExactPresenceAbsent,
					decision: TrustDecisionUntrusted,
				}
			}
		}
		if executor.invalidAfterMutation {
			executor.invalidTrust = true
		}
		executor.mu.Unlock()
		return commandResult(outcome, nil), nil
	default:
		return CommandResult{}, ErrCommandInvalid
	}
}

func (executor *machineExecutor) checkMutationSpec(spec CommandSpec) {
	arguments := spec.Arguments()
	var artifactPath string
	switch spec.kind {
	case CommandEnsureExactTrust:
		if len(arguments) == 6 &&
			slices.Equal(arguments[:5], []string{
				"add-trusted-cert",
				"-r",
				"trustRoot",
				"-p",
				"ssl",
			}) {
			artifactPath = arguments[5]
		}
	case CommandRemoveExactTrust:
		if len(arguments) == 2 {
			artifactPath = arguments[1]
		}
	case CommandDeleteExactObject:
		if len(arguments) != 3 || arguments[0] != "delete-certificate" ||
			arguments[1] != "-Z" ||
			arguments[2] != strings.ToUpper(executor.root.identity.Digest().String()) {
			executor.mu.Lock()
			executor.artifactError = errors.New("delete command is not exact")
			executor.mu.Unlock()
		}
		return
	}
	if artifactPath == "" {
		executor.mu.Lock()
		executor.artifactError = errors.New("certificate artifact path is absent")
		executor.mu.Unlock()
		return
	}
	info, err := os.Stat(artifactPath)
	material, readErr := os.ReadFile(artifactPath)
	if err != nil || readErr != nil || info.Mode().Perm() != 0o600 ||
		sha256.Sum256(material) != sha256.Sum256(executor.root.certificateDER) {
		executor.mu.Lock()
		executor.artifactError = errors.New("certificate artifact is not exact and private")
		executor.mu.Unlock()
		return
	}
	executor.mu.Lock()
	executor.artifactChecked = true
	executor.mu.Unlock()
}

func (executor *machineExecutor) setState(state machineState) {
	executor.mu.Lock()
	executor.state = state
	executor.mu.Unlock()
}

func (executor *machineExecutor) callKinds() []CommandKind {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	kinds := make([]CommandKind, len(executor.calls))
	for index, call := range executor.calls {
		kinds[index] = call.kind
	}
	return kinds
}

func commandResult(outcome CommandOutcome, stdout []byte) CommandResult {
	result, err := NewCommandResult(outcome, stdout, nil)
	if err != nil {
		panic(err)
	}
	return result
}

func certificatePEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func trustFixtureOutput(root publicRoot, decision TrustDecision) []byte {
	entries := []trustFixtureEntry{}
	if decision == TrustDecisionTrusted || decision == TrustDecisionUntrusted {
		entries = append(entries, trustFixtureEntry{
			DERDigest: root.identity.Digest().String(),
			Usage:     string(TrustUsageServerTLS),
			Decision:  string(decision),
		})
	}
	output, err := json.Marshal(trustFixture{
		Schema:   trustFixtureSchemaV1,
		Complete: true,
		Entries:  entries,
	})
	if err != nil {
		panic(err)
	}
	return output
}

func cloneByteSlices(values [][]byte) [][]byte {
	cloned := make([][]byte, len(values))
	for index, value := range values {
		cloned[index] = bytes.Clone(value)
	}
	return cloned
}

func testPublicRoot(t *testing.T) publicRoot {
	t.Helper()
	owner, cancelOwner := context.WithCancel(context.Background())
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		cancelOwner()
		t.Fatal(err)
	}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(directory, owner),
	)
	if err != nil {
		cancelOwner()
		t.Fatal(err)
	}
	source, err := NewLocalAuthorityRootSource(authority)
	if err != nil {
		cancelOwner()
		t.Fatal(err)
	}
	root, err := source.currentPublicRoot(context.Background())
	if err != nil {
		cancelOwner()
		t.Fatal(err)
	}
	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	if err := authority.Shutdown(shutdownContext); err != nil {
		cancelShutdown()
		cancelOwner()
		t.Fatal(err)
	}
	cancelShutdown()
	cancelOwner()
	return root
}

func newTestCoordinator(
	t *testing.T,
	root publicRoot,
	executor *machineExecutor,
) (*Coordinator, *mutableRootSource) {
	t.Helper()
	source := &mutableRootSource{root: root.clone()}
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(source, adapter, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(shutdownContext); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	return coordinator, source
}

func TestPlanTruthTableAndImmutableCopies(t *testing.T) {
	root := testPublicRoot(t)
	tests := []struct {
		name      string
		operation Operation
		state     machineState
		steps     []Step
		desired   machineState
	}{
		{
			name:      "install absent",
			operation: OperationInstall,
			state: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			},
			steps: []Step{
				StepEnsureExactCertificateAndUserTrust,
				StepInspectExactRoot,
			},
			desired: machineState{ExactPresencePresent, TrustDecisionTrusted},
		},
		{
			name:      "install already trusted",
			operation: OperationInstall,
			state: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
			desired: machineState{ExactPresencePresent, TrustDecisionTrusted},
		},
		{
			name:      "install restores trust",
			operation: OperationInstall,
			state: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionUntrusted,
			},
			steps: []Step{
				StepEnsureExactCertificateAndUserTrust,
				StepInspectExactRoot,
			},
			desired: machineState{ExactPresencePresent, TrustDecisionTrusted},
		},
		{
			name:      "remove trusted",
			operation: OperationRemove,
			state: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
			steps: []Step{
				StepRemoveExactUserTrustSettings,
				StepInspectExactRoot,
				StepDeleteExactCertificate,
				StepInspectExactRoot,
			},
			desired: machineState{ExactPresenceAbsent, TrustDecisionUntrusted},
		},
		{
			name:      "remove residual object",
			operation: OperationRemove,
			state: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionUntrusted,
			},
			steps: []Step{
				StepDeleteExactCertificate,
				StepInspectExactRoot,
			},
			desired: machineState{ExactPresenceAbsent, TrustDecisionUntrusted},
		},
		{
			name:      "remove already absent",
			operation: OperationRemove,
			state: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			},
			desired: machineState{ExactPresenceAbsent, TrustDecisionUntrusted},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newMachineExecutor(root, test.state)
			coordinator, _ := newTestCoordinator(t, root, executor)
			plan, err := coordinator.Plan(context.Background(), test.operation)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Valid() || !slices.Equal(plan.Steps(), test.steps) {
				t.Fatalf("unexpected plan steps: %v", plan.Steps())
			}
			if plan.DesiredObservation().Presence() != test.desired.presence ||
				plan.DesiredObservation().TrustDecision() != test.desired.decision {
				t.Fatalf("unexpected desired observation: %+v", plan.DesiredObservation())
			}
			if plan.RequiresOSAuthorization() != (len(test.steps) != 0) {
				t.Fatal("authorization requirement does not match mutation steps")
			}
			der := plan.CertificateDER()
			steps := plan.Steps()
			der[0] ^= 0xff
			if len(steps) != 0 {
				steps[0] = StepInspectExactRoot
			}
			if !bytes.Equal(plan.CertificateDER(), root.certificateDER) ||
				!slices.Equal(plan.Steps(), test.steps) {
				t.Fatal("plan getter allowed output alias mutation")
			}
		})
	}
}

func TestObservationRejectsUnknownAndContradictoryEvidence(t *testing.T) {
	root := testPublicRoot(t)
	tests := []struct {
		name  string
		state machineState
		setup func(*machineExecutor)
	}{
		{
			name: "absent but trusted",
			state: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionTrusted,
			},
		},
		{
			name: "presence unknown",
			state: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
			setup: func(executor *machineExecutor) {
				executor.invalidPresence = true
			},
		},
		{
			name: "trust unknown",
			state: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
			setup: func(executor *machineExecutor) {
				executor.invalidTrust = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newMachineExecutor(root, test.state)
			if test.setup != nil {
				test.setup(executor)
			}
			coordinator, _ := newTestCoordinator(t, root, executor)
			_, err := coordinator.Plan(context.Background(), OperationInstall)
			if !errors.Is(err, ErrObservationUnknown) {
				t.Fatalf("expected unknown observation, got %v", err)
			}
			for _, kind := range executor.callKinds() {
				if kind == CommandEnsureExactTrust || kind == CommandRemoveExactTrust ||
					kind == CommandDeleteExactObject {
					t.Fatalf("unknown evidence invoked mutation %s", kind)
				}
			}
		})
	}
}

func TestExactPresenceIgnoresForeignCertificateWithSameSubject(t *testing.T) {
	root := testPublicRoot(t)
	foreign := testPublicRoot(t)
	rootCertificate, err := x509.ParseCertificate(root.certificateDER)
	if err != nil {
		t.Fatal(err)
	}
	foreignCertificate, err := x509.ParseCertificate(foreign.certificateDER)
	if err != nil {
		t.Fatal(err)
	}
	if rootCertificate.Subject.String() != foreignCertificate.Subject.String() {
		t.Fatal("test roots do not share a subject")
	}
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.foreignDER = [][]byte{foreign.certificateDER}
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationRemove)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps()) != 0 ||
		plan.Precondition().Presence() != ExactPresenceAbsent {
		t.Fatalf("foreign same-subject Root matched exact identity: %v", plan.Steps())
	}
}

func TestCommandResultDefensivelyCopiesAndBoundsEvidence(t *testing.T) {
	stdout := []byte("evidence")
	result, err := NewCommandResult(CommandOutcomeSucceeded, stdout, nil)
	if err != nil {
		t.Fatal(err)
	}
	stdout[0] = 'X'
	if string(result.stdout) != "evidence" {
		t.Fatal("command result retained caller output alias")
	}
	if _, err := NewCommandResult(
		CommandOutcomeSucceeded,
		make([]byte, maxCommandOutputBytes+1),
		nil,
	); !errors.Is(err, ErrCommandInvalid) {
		t.Fatalf("oversized output was accepted: %v", err)
	}
}
