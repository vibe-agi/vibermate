package systemtrust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMacOSAdapterUsesOnlyFixedCommandShapesAndCleansArtifacts(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	calls := append([]CommandSpec(nil), executor.calls...)
	executor.mu.Unlock()
	var artifactPath string
	for _, call := range calls {
		if call.Executable() != macOSSecurityExecutable {
			t.Fatalf("unexpected executable: %q", call.Executable())
		}
		arguments := call.Arguments()
		if call.Kind() == CommandEnsureExactTrust {
			if len(arguments) != 9 || !slices.Equal(arguments[:8], []string{
				"add-trusted-cert",
				"-d",
				"-r",
				"trustRoot",
				"-p",
				"ssl",
				"-k",
				macOSSystemKeychain,
			}) {
				t.Fatalf("unexpected install arguments: %v", arguments)
			}
			artifactPath = arguments[8]
			arguments[0] = "injected"
			if call.Arguments()[0] != "add-trusted-cert" {
				t.Fatal("command arguments were mutable through their getter")
			}
		}
	}
	if artifactPath == "" {
		t.Fatal("install command did not receive an adapter-owned artifact")
	}
	if filepath.Base(artifactPath) != "root.cer" ||
		!strings.HasPrefix(filepath.Dir(artifactPath), os.TempDir()) {
		t.Fatalf("artifact was not adapter-owned: %q", artifactPath)
	}
	if _, err := os.Stat(artifactPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("public certificate artifact was not cleaned: %v", err)
	}
	for _, value := range []string{
		string(plan.CertificateDER()),
		string(plan.Steps()[0]),
	} {
		if strings.Contains(value, artifactPath) {
			t.Fatal("ephemeral path escaped into the immutable plan")
		}
	}
}

func TestMacOSFixturePresenceParserUsesOnlyExactDERDigest(t *testing.T) {
	root := testPublicRoot(t)
	foreign := testPublicRoot(t)
	tests := []struct {
		name   string
		output []byte
		want   ExactPresence
		bad    bool
	}{
		{name: "empty", want: ExactPresenceAbsent},
		{
			name:   "foreign",
			output: certificatePEM(foreign.certificateDER),
			want:   ExactPresenceAbsent,
		},
		{
			name:   "exact",
			output: certificatePEM(root.certificateDER),
			want:   ExactPresencePresent,
		},
		{
			name: "exact among foreign",
			output: bytes.Join([][]byte{
				certificatePEM(foreign.certificateDER),
				certificatePEM(root.certificateDER),
			}, nil),
			want: ExactPresencePresent,
		},
		{
			name: "duplicate exact is ambiguous",
			output: bytes.Join([][]byte{
				certificatePEM(root.certificateDER),
				certificatePEM(root.certificateDER),
			}, nil),
			bad: true,
		},
		{name: "malformed", output: []byte("not PEM"), bad: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presence, err := parseExactPresence(
				test.output,
				root.identity.Digest().String(),
			)
			if test.bad {
				if !errors.Is(err, ErrObservationUnknown) ||
					presence != ExactPresenceUnknown {
					t.Fatalf("invalid fixture was accepted: presence=%s error=%v", presence, err)
				}
				return
			}
			if err != nil || presence != test.want {
				t.Fatalf("unexpected presence: presence=%s error=%v", presence, err)
			}
		})
	}
}

func TestMacOSFixtureTrustParserRejectsUnboundedOrAmbiguousEvidence(t *testing.T) {
	root := testPublicRoot(t)
	valid := trustFixture{
		Schema:   trustFixtureSchemaV1,
		Complete: true,
		Entries: []trustFixtureEntry{{
			DERDigest: root.identity.Digest().String(),
			Usage:     string(TrustUsageServerTLS),
			Decision:  string(TrustDecisionTrusted),
		}},
	}
	encode := func(value any) []byte {
		output, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if decision, err := parseFixtureTrustDecision(
		encode(valid),
		root.identity.Digest().String(),
	); err != nil || decision != TrustDecisionTrusted {
		t.Fatalf("valid fixture failed: decision=%s error=%v", decision, err)
	}
	tests := []struct {
		name   string
		output []byte
	}{
		{name: "unknown schema", output: []byte(`{"schema":"future","complete":true,"entries":[]}`)},
		{name: "incomplete", output: []byte(`{"schema":"vibermate-macos-admin-trust-fixture-v1","complete":false,"entries":[]}`)},
		{name: "unknown field", output: []byte(`{"schema":"vibermate-macos-admin-trust-fixture-v1","complete":true,"entries":[],"extra":true}`)},
		{name: "trailing value", output: append(encode(valid), []byte(` {}`)...)},
		{name: "duplicate field", output: []byte(`{"schema":"vibermate-macos-admin-trust-fixture-v1","schema":"vibermate-macos-admin-trust-fixture-v1","complete":true,"entries":[]}`)},
		{
			name: "non hexadecimal digest",
			output: encode(trustFixture{
				Schema:   trustFixtureSchemaV1,
				Complete: true,
				Entries: []trustFixtureEntry{{
					DERDigest: strings.Repeat("z", 64),
					Usage:     string(TrustUsageServerTLS),
					Decision:  string(TrustDecisionTrusted),
				}},
			}),
		},
		{
			name: "uppercase digest",
			output: encode(trustFixture{
				Schema:   trustFixtureSchemaV1,
				Complete: true,
				Entries: []trustFixtureEntry{{
					DERDigest: strings.ToUpper(root.identity.Digest().String()),
					Usage:     string(TrustUsageServerTLS),
					Decision:  string(TrustDecisionTrusted),
				}},
			}),
		},
		{
			name: "duplicate exact decision",
			output: encode(trustFixture{
				Schema:   trustFixtureSchemaV1,
				Complete: true,
				Entries: []trustFixtureEntry{
					{
						DERDigest: root.identity.Digest().String(),
						Usage:     string(TrustUsageServerTLS),
						Decision:  string(TrustDecisionTrusted),
					},
					{
						DERDigest: root.identity.Digest().String(),
						Usage:     string(TrustUsageServerTLS),
						Decision:  string(TrustDecisionUntrusted),
					},
				},
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := parseFixtureTrustDecision(
				test.output,
				root.identity.Digest().String(),
			)
			if !errors.Is(err, ErrObservationUnknown) ||
				decision != TrustDecisionUnknown {
				t.Fatalf("invalid evidence was accepted: decision=%s error=%v", decision, err)
			}
		})
	}
}

func TestCoordinatorEnforcesCommandDeadlineAndStillReconciles(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.blockKind = CommandEnsureExactTrust
	executor.blockEntered = make(chan struct{})
	executor.blockUntilCancel = true
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = 20 * time.Millisecond
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(
		&mutableRootSource{root: root.clone()},
		adapter,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Execute(context.Background(), plan)
	if err == nil || result.Completed() || result.Reason() != ReasonCommandTimeout {
		t.Fatalf("deadline was not classified: result=%+v error=%v", result, err)
	}
	calls := executor.callKinds()
	mutationIndex := slices.Index(calls, CommandEnsureExactTrust)
	if mutationIndex < 0 || mutationIndex+2 >= len(calls) ||
		calls[mutationIndex+1] != CommandInspectExactPresence ||
		calls[mutationIndex+2] != CommandInspectAdminTrust {
		t.Fatalf("timeout skipped reconciliation: %v", calls)
	}
}
