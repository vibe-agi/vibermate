package toolapproval

import (
	"strings"
	"testing"
	"time"
)

func genericRecord(t *testing.T) Record {
	t.Helper()

	return Record{
		ID:            "approval-1",
		Revision:      1,
		Kind:          KindNetworkAsk,
		AggregateKey:  "network:files.example.com:443",
		SubjectRefs:   []string{"files.example.com:443"},
		SubjectLabels: []string{"files.example.com"},
		Target:        Target{Host: "files.example.com", Port: 443},
		RequestCount:  1,
		WaiterCount:   1,
		State:         StatePending,
		CreatedAt:     time.Unix(1785600000, 0).UTC(),
		ExpiresAt:     time.Unix(1785600060, 0).UTC(),
	}
}

// A network ask is decided before any Access is resolved, so it has no plan
// binding to supply. Requiring one made connection policy impossible to build
// on this record.
func TestApprovalWithoutAnAccessPlanBindingIsValid(t *testing.T) {
	t.Parallel()

	if err := genericRecord(t).Validate(); err != nil {
		t.Fatalf("an approval without a plan binding was rejected: %v", err)
	}
}

// A tool intent still carries its binding, and a stale plan still cannot
// resolve it.
func TestToolIntentStillRequiresItsAccessPlanBinding(t *testing.T) {
	t.Parallel()

	record := genericRecord(t)
	record.Kind = KindToolIntent
	record.Target = Target{}
	record.SubjectRefs = []string{"call-1"}
	record.SubjectLabels = []string{"Bash"}
	record.ExchangeID = "exchange-1"
	if err := record.Validate(); err == nil {
		t.Fatal("a tool intent without a plan binding was accepted")
	}
}

// Risk, copy keys, and choices belong to the kind rather than to constants, so
// a second kind does not mean forking the record.
func TestPresentationIsDerivedFromTheKind(t *testing.T) {
	t.Parallel()

	network := ViewOf(genericRecord(t))
	if network.Kind != string(KindNetworkAsk) ||
		network.TitleKey == "" ||
		network.SummaryKey == "" ||
		len(network.Choices) == 0 {
		t.Fatalf("network ask view = %+v", network)
	}

	tool := genericRecord(t)
	tool.Kind = KindToolIntent
	toolView := ViewOf(tool)
	if toolView.Kind != string(KindToolIntent) ||
		toolView.TitleKey == network.TitleKey {
		t.Fatalf("tool intent view = %+v", toolView)
	}
}

// A burst of the same question is one entry with counts, not one prompt per
// event.
func TestIdenticalPendingItemsCarryCounts(t *testing.T) {
	t.Parallel()

	record := genericRecord(t)
	record.RequestCount = 7
	record.WaiterCount = 3
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	view := ViewOf(record)
	if view.RequestCount != 7 || view.WaiterCount != 3 {
		t.Fatalf("aggregate counts = %+v", view)
	}

	record.WaiterCount = record.RequestCount + 1
	if err := record.Validate(); err == nil {
		t.Fatal("more waiters than requests was accepted")
	}
}

// The subject is identifiers and labels only.
func TestSubjectCarriesNoContent(t *testing.T) {
	t.Parallel()

	record := genericRecord(t)
	record.SubjectLabels = []string{strings.Repeat("x", 4096)}
	if err := record.Validate(); err == nil {
		t.Fatal("an unbounded subject label was accepted")
	}

	mismatched := genericRecord(t)
	mismatched.SubjectLabels = nil
	if err := mismatched.Validate(); err == nil {
		t.Fatal("a subject without labels was accepted")
	}
}
