package productruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

// "My client cannot connect" is the report this product will receive most
// often. Answering it used to mean rebuilding the runtime with a print
// statement, because the only thing stored was a reason code.
//
// A rejected request now records where in the request's shape it failed.
func TestARejectedRequestRecordsWhereItFailed(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-diagnosable")
	if err != nil {
		t.Fatal(err)
	}
	runtime := startTestRuntime(
		t,
		testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}),
	)
	defer shutdownRuntime(t, runtime)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Diagnosable",
			),
		},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	// A role this dialect cannot express, in a known position.
	request, err := exchange.NewClientRequest(
		"exchange-diagnosable",
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":32,
			"messages":[
				{"role":"user","content":"hello"},
				{"role":"narrator","content":"a role that does not exist"}
			]
		}`),
		exchange.ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		request,
		&runtimeDownstream{},
	); err == nil {
		t.Fatal("an impossible role was accepted")
	}

	records, err := runtime.Activities().List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Items) != 1 {
		t.Fatalf("activity records = %+v", records.Items)
	}
	record := records.Items[0]
	if record.Status != activity.StatusFailed {
		t.Fatalf("status = %q", record.Status)
	}
	// The reason stays one stable code rather than a sentence with the
	// evidence glued onto its end.
	if strings.Contains(record.ReasonCode, "_client_field_") ||
		strings.Contains(record.ReasonCode, "_http_") {
		t.Fatalf("the reason carries its evidence: %q", record.ReasonCode)
	}
	if record.Diagnosis == nil {
		t.Fatalf("a failed request recorded no diagnosis: %+v", record)
	}
	if record.Diagnosis.ClientPath != "$.messages[1].role" {
		t.Fatalf("path = %q", record.Diagnosis.ClientPath)
	}
	// Structure only. The role the client actually sent, and everything else
	// it said, stays out of the record.
	if strings.Contains(record.Diagnosis.ClientPath, "narrator") ||
		strings.Contains(record.ReasonCode, "narrator") {
		t.Fatalf("the record repeated request content: %+v", record)
	}
}

// A request that succeeds has nothing to diagnose, and must not carry an
// empty structure pretending otherwise.
func TestASuccessfulRequestRecordsNoDiagnosis(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-no-diagnosis")
	if err != nil {
		t.Fatal(err)
	}
	provider := &pipelineProviderRuntime{
		responseBody: []byte(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1,
			"model":"gpt-4.1-mini",
			"choices":[{
				"index":0,
				"finish_reason":"stop",
				"message":{"role":"assistant","content":"Runtime path."}
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`),
	}
	builders := productionBuilders()
	builders.provider = fixedProviderBuilder{component: provider}
	runtime, err := startWithBuilders(
		context.Background(),
		testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}),
		builders,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, runtime)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate:        runtimeAccessAggregate(t, accessID, 1, "No Diagnosis"),
		},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-no-diagnosis",
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":32,
			"messages":[{"role":"user","content":"hello"}]
		}`),
		exchange.ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		request,
		&runtimeDownstream{},
	); err != nil {
		t.Fatal(err)
	}
	records, err := runtime.Activities().List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Items) != 1 || records.Items[0].Diagnosis != nil {
		t.Fatalf("a successful request carried a diagnosis: %+v", records.Items)
	}
}
