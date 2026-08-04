package toolapproval_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// samplePath is where the desktop window reads the shapes it renders. The
// window must not carry a hand-typed idea of what the runtime sends, because a
// window that renders a shape the runtime stopped sending shows a person
// nothing while claiming to show them everything.
func samplePath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "api", "samples", "approvals.json")
}

func sampleViews(t *testing.T) []toolapproval.View {
	t.Helper()

	created := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	network := toolapproval.Record{
		ID:            "approval-network-sample",
		Revision:      1,
		Kind:          toolapproval.KindNetworkAsk,
		AggregateKey:  "aggregate-network-sample",
		SubjectRefs:   []string{"api.example.com:443"},
		SubjectLabels: []string{"api.example.com"},
		Target: toolapproval.Target{
			Host: "api.example.com",
			Port: 443,
		},
		RequestCount: 3,
		WaiterCount:  3,
		State:        toolapproval.StatePending,
		CreatedAt:    created,
		ExpiresAt:    created.Add(time.Minute),
	}
	if err := network.Validate(); err != nil {
		t.Fatal(err)
	}
	intent := toolapproval.Record{
		ID:            "approval-tool-sample",
		Revision:      1,
		Kind:          toolapproval.KindToolIntent,
		AggregateKey:  "aggregate-tool-sample",
		SubjectRefs:   []string{"call-1", "call-2"},
		SubjectLabels: []string{"read_file", "list_directory"},
		RequestCount:  1,
		WaiterCount:   1,
		ExchangeID:    "exchange-sample",
		AccessID:      sampleAccessID(t),
		PlanRevision:  4,
		PlanHash:      samplePlanHash(t),
		State:         toolapproval.StatePending,
		CreatedAt:     created,
		ExpiresAt:     created.Add(time.Minute),
	}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	clientRoot := toolapproval.Record{
		ID:            "approval-client-root-sample",
		Revision:      1,
		Kind:          toolapproval.KindClientRootAsk,
		AggregateKey:  "aggregate-client-root-sample",
		SubjectRefs:   []string{"client-signer:developer-id-application-anthropic-pbc"},
		SubjectLabels: []string{"developer-id-application-anthropic-pbc"},
		RequestCount:  1,
		WaiterCount:   1,
		State:         toolapproval.StatePending,
		CreatedAt:     created,
		ExpiresAt:     created.Add(time.Minute),
	}
	if err := clientRoot.Validate(); err != nil {
		t.Fatal(err)
	}
	clientRootView := toolapproval.ViewOf(clientRoot)
	// The real authority overlays this no-store evidence only while the
	// question is live. The wire sample describes that live projection without
	// putting the path in the durable Record fixture.
	clientRootView.SubjectLabels = []string{
		"/Applications/Claude.app/Contents/MacOS/claude",
	}
	return []toolapproval.View{
		toolapproval.ViewOf(network),
		toolapproval.ViewOf(intent),
		clientRootView,
	}
}

func sampleAccessID(t *testing.T) access.AccessID {
	t.Helper()

	identifier, err := access.NewAccessID("work")
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func samplePlanHash(t *testing.T) access.PlanHash {
	t.Helper()

	var digest access.PlanHash
	for index := range digest {
		digest[index] = byte(index + 1)
	}
	return digest
}

// The sample is generated, not maintained. Running with VIBERMATE_UPDATE=1
// rewrites it; every other run checks that it still describes what the runtime
// would actually send.
func TestApprovalSamplesDescribeWhatTheRuntimeSends(t *testing.T) {
	encoded, err := json.MarshalIndent(sampleViews(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := samplePath(t)
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read approval samples: %v", err)
	}
	if string(current) != string(encoded) {
		t.Fatalf(
			"the approval samples the window renders are stale; "+
				"rerun with VIBERMATE_UPDATE=1\n--- stored\n%s\n--- current\n%s",
			current,
			encoded,
		)
	}
}
